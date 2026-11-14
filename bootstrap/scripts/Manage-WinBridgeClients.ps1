# Manage the application-level client allowlist. The firewall remains
# Tailscale-only; this list is the narrow, second layer.

param(
    [Parameter(Mandatory = $true)][ValidateSet('list', 'add', 'remove', 'clear', 'tailnet')][string]$Action,
    [string]$Client,
    [int]$TimeoutSeconds = 90,
    [switch]$Json
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Tailscale.ps1')
. (Join-Path $binRoot 'lib\WinBridgeConfig.ps1')
. (Join-Path $binRoot 'lib\ScheduledTask.ps1')

if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

function Restart-WinBridgeAfterChange {
    $credential = Test-WinBridgeCredential
    if (-not $credential.Present) { return $false }
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) { return $false }
    $port = Get-WinBridgePort
    $flag = Get-WinBridgePath 'state\restart.flag'
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $flag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($flag, (Get-Date).ToString('o'), $encoding)
    Start-WinBridgeTask | Out-Null
    try {
        Wait-WinBridgeHttp -Uri ('http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $port + '/health') -TimeoutSeconds $TimeoutSeconds | Out-Null
        return $true
    } finally {
        if (Test-Path -LiteralPath $flag) {
            Remove-Item -LiteralPath $flag -Force -ErrorAction SilentlyContinue
        }
    }
}

try {
    $state = Get-WinBridgeState
    if ($null -eq $state) { $state = Get-WinBridgeDefaultState }

    switch ($Action) {
        'list' { }
        'add' {
            if (-not $Client) { throw '-Client is required for add.' }
            Add-WinBridgeClient -State $state -Client $Client | Out-Null
            Save-WinBridgeState -State $state
            Write-WinBridgeConfig -State $state | Out-Null
        }
        'remove' {
            if (-not $Client) { throw '-Client is required for remove.' }
            Remove-WinBridgeClient -State $state -Client $Client | Out-Null
            Save-WinBridgeState -State $state
            Write-WinBridgeConfig -State $state | Out-Null
        }
        'clear' {
            Set-WinBridgeProperty -Object $state -Name 'allowed_clients' -Value @()
            Save-WinBridgeState -State $state
            Write-WinBridgeConfig -State $state | Out-Null
        }
        'tailnet' {
            Add-WinBridgeClient -State $state -Client $script:WinBridgeTailscaleV4 | Out-Null
            Add-WinBridgeClient -State $state -Client $script:WinBridgeTailscaleV6 | Out-Null
            Save-WinBridgeState -State $state
            Write-WinBridgeConfig -State $state | Out-Null
        }
    }

    $clients = @(Get-WinBridgeClients -State $state)
    $restarted = $false
    if ($Action -ne 'list') {
        $restarted = Restart-WinBridgeAfterChange
    }
    $result = [pscustomobject]@{
        ok = $true
        action = $Action
        clients = $clients
        restarted = $restarted
    }
    if ($Json) {
        Write-Output ($result | ConvertTo-Json -Depth 5 -Compress)
    } else {
        Write-Output $result
    }
} catch {
    $message = $_.Exception.Message
    Write-WinBridgeError $message
    if ($Json) {
        Write-Output ((@{ ok = $false; error = $message } | ConvertTo-Json -Compress))
    }
    exit 1
}
