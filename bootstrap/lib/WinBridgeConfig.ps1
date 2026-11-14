# State, TOML generation and client allowlist helpers.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgeConfigPath {
    return (Get-WinBridgePath 'config\winbridge.toml')
}

function Get-WinBridgeTemplatePath {
    return (Get-WinBridgePath 'bin\templates\winbridge.toml.template')
}

function Get-WinBridgeDefaultState {
    return [pscustomobject]@{
        schema = 1
        port = $script:WinBridgeDefaultPort
        tailscale_ip = $null
        allowed_clients = @()
        installed_version = $null
        upstream_base = '0.4.24'
        installed_at = $null
        bundle_id = $null
        bundle_created_at = $null
    }
}

function Test-WinBridgeClientCidr {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)
    $trimmed = $Value.Trim()
    if (-not $trimmed) { return $null }
    $addressPart = $trimmed
    $prefix = $null
    if ($trimmed -match '^(.+)/(\d{1,3})$') {
        $addressPart = $matches[1]
        $prefix = [int]$matches[2]
    }
    $parsed = $null
    if (-not [System.Net.IPAddress]::TryParse($addressPart, [ref]$parsed)) { return $null }
    $isV4 = ($parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork)
    $maxPrefix = if ($isV4) { 32 } else { 128 }
    if ($null -eq $prefix) { $prefix = $maxPrefix }
    if ($prefix -lt 0 -or $prefix -gt $maxPrefix) { return $null }
    if (-not (Test-WinBridgeTailscaleAddress -Address $parsed.ToString())) { return $null }
    if ($isV4 -and $prefix -lt 10) { return $null }
    if (-not $isV4 -and $prefix -lt 48) { return $null }
    return ($parsed.ToString() + '/' + $prefix)
}

function Get-WinBridgeClients {
    param($State)
    if ($null -eq $State -or $null -eq $State.allowed_clients) { return @() }
    return @($State.allowed_clients)
}

function Add-WinBridgeClient {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)][string]$Client
    )
    $normalized = Test-WinBridgeClientCidr -Value $Client
    if (-not $normalized) {
        throw ('Invalid Tailscale client IP or CIDR: ' + $Client)
    }
    $clients = @(Get-WinBridgeClients -State $State)
    if ($clients -notcontains $normalized) {
        $clients += $normalized
    }
    Set-WinBridgeProperty -Object $State -Name 'allowed_clients' -Value @($clients)
    return $normalized
}

function Remove-WinBridgeClient {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)][string]$Client
    )
    $normalized = Test-WinBridgeClientCidr -Value $Client
    if (-not $normalized) {
        throw ('Invalid Tailscale client IP or CIDR: ' + $Client)
    }
    $clients = @(Get-WinBridgeClients -State $State)
    Set-WinBridgeProperty -Object $State -Name 'allowed_clients' -Value @($clients | Where-Object { $_ -ne $normalized })
    return $normalized
}

function Get-WinBridgeBindAddress {
    param($State)
    if ($null -ne $State -and $null -ne $State.tailscale_ip -and [string]$State.tailscale_ip) {
        $address = [string]$State.tailscale_ip
    } else {
        $address = Get-WinBridgeTailscaleIPv4
    }
    if (-not (Test-WinBridgeTailscaleAddress -Address $address)) {
        throw 'No usable Tailscale IPv4 address is available for the server bind.'
    }
    return $address
}

function Write-WinBridgeConfig {
    param(
        [Parameter(Mandatory = $true)]$State,
        [string]$TemplatePath = (Get-WinBridgeTemplatePath),
        [string]$ConfigPath = (Get-WinBridgeConfigPath)
    )
    if (-not (Test-Path -LiteralPath $TemplatePath)) {
        throw ('Config template not found: ' + $TemplatePath)
    }
    $port = $script:WinBridgeDefaultPort
    if ($null -ne $State.port) { $port = [int]$State.port }
    if ($port -lt 1024 -or $port -gt 65535) {
        throw ('Port must be between 1024 and 65535, got ' + $port + '.')
    }
    $hostIp = Get-WinBridgeBindAddress -State $State
    $clients = @(Get-WinBridgeClients -State $State)
    # The server connects to its own Tailscale address for local health checks,
    # so that address must be in the application allowlist as well.
    $effective = @($hostIp) + $clients
    $effective = @($effective | Select-Object -Unique)
    foreach ($entry in $effective) {
        if (-not (Test-WinBridgeClientCidr -Value $entry)) {
            throw ('Refusing to write a non-Tailscale allowlist entry: ' + $entry)
        }
    }
    $clientText = ($effective | ForEach-Object { '"' + $_ + '"' }) -join ', '
    $template = Get-Content -LiteralPath $TemplatePath -Raw
    $content = $template.Replace('{{TAILSCALE_IP}}', $hostIp)
    $content = $content.Replace('{{PORT}}', [string]$port)
    $content = $content.Replace('{{ALLOWED_CLIENTS}}', $clientText)
    $localPath = Join-Path (Split-Path -Parent $ConfigPath) 'local.toml'
    if (Test-Path -LiteralPath $localPath) {
        $local = Get-Content -LiteralPath $localPath -Raw
        Assert-WinBridgeLocalConfig -Text $local
        $content = $content.TrimEnd() + "`r`n`r`n# local.toml (kept across updates)`r`n" + $local
    }
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $ConfigPath)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($ConfigPath, $content, $encoding)
    return $ConfigPath
}

# Refuses a local.toml that touches anything but [tasks.*] and [servers.*], so it cannot widen the profile.
function Assert-WinBridgeLocalConfig {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Text)
    foreach ($line in ($Text -split "`r?`n")) {
        $trimmed = $line.Trim()
        if ($trimmed.StartsWith('[') -and $trimmed -notmatch '^\[((tasks|servers)\.[A-Za-z0-9._"-]+|jobs)\]\s*(#.*)?$') {
            throw ('local.toml may only hold [tasks.*], [servers.*] and [jobs] tables; found: ' + $trimmed)
        }
    }
}

function Get-WinBridgeGoConfigSummary {
    param(
        [Parameter(Mandatory = $true)][string]$AgentExe,
        [string]$ConfigPath = (Get-WinBridgeConfigPath)
    )
    if (-not (Test-Path -LiteralPath $ConfigPath)) { return $null }
    try {
        $value = & $AgentExe --config $ConfigPath --print-config 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return (($value -join "`n") | ConvertFrom-Json)
    } catch {
        return $null
    }
}

function Get-WinBridgeConfigSummary {
    param(
        [Parameter(Mandatory = $true)][string]$VenvPython,
        [string]$ConfigPath = (Get-WinBridgeConfigPath)
    )
    if (-not (Test-Path -LiteralPath $ConfigPath)) { return $null }
    $script = @'
import json
import sys
import tomllib

with open(sys.argv[1], "rb") as handle:
    cfg = tomllib.load(handle)
print(json.dumps({
    "host": cfg.get("server", {}).get("host"),
    "port": cfg.get("server", {}).get("port"),
    "ip_allowlist": cfg.get("security", {}).get("ip_allowlist", []),
    "enable_tier3": cfg.get("security", {}).get("enable_tier3", False),
    "tools": cfg.get("tools", {}).get("enable", []),
    "exclude": cfg.get("tools", {}).get("exclude", []),
}))
'@
    $temp = Join-Path $env:TEMP ('winbridge-config-check-' + [guid]::NewGuid().ToString('N') + '.py')
    try {
        [IO.File]::WriteAllText($temp, $script, (New-Object System.Text.UTF8Encoding($false)))
        $value = & $VenvPython $temp $ConfigPath 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return (($value -join "`n") | ConvertFrom-Json)
    } finally {
        if (Test-Path -LiteralPath $temp) {
            Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue
        }
    }
}
