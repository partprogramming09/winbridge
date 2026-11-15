# Lets the agent list and read files inside folders the owner names: writes the [files] table to config\local.toml (kept
# across updates), checks the result with the agent (which refuses dangerous folders) and restarts it. -Remove deletes the
# table, and with it the file tools. It runs as the owner and needs no elevation.

param(
    [string[]]$ReadRoot = @(),
    [switch]$Remove,
    [switch]$NoRestart,
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

$result = [pscustomobject]@{ ok = $false; read_roots = @($ReadRoot); action = $null; error = $null }

try {
    if (-not $Remove) {
        if (@($ReadRoot).Count -eq 0) { throw '-ReadRoot needs at least one folder (or use -Remove).' }
        foreach ($root in $ReadRoot) {
            if (-not [IO.Path]::IsPathRooted($root) -or $root -notmatch '^[A-Za-z]:[\\/]') { throw ('-ReadRoot must be an absolute path with a drive letter: ' + $root) }
            if ($root -match "['\r\n]") { throw '-ReadRoot cannot hold a single quote or a line break.' }
        }
    }
    $configDir = Split-Path -Parent (Get-WinBridgeConfigPath)
    $localPath = Join-Path $configDir 'local.toml'
    $goExe = Get-WinBridgePath 'app\go\winbridge-agent.exe'
    if (-not (Test-Path -LiteralPath $goExe)) { throw 'This is not a Go install; use the installer.' }
    Ensure-WinBridgeDirectory -Path $configDir

    $existing = @()
    if (Test-Path -LiteralPath $localPath) { $existing = @(Get-Content -LiteralPath $localPath -Encoding UTF8) }
    $previous = $existing
    $lines = New-Object System.Collections.Generic.List[string]
    $skip = $false
    foreach ($line in $existing) {
        $t = $line.Trim()
        if ($t.StartsWith('[')) { $skip = ($t -match '^\[files\]\s*(#.*)?$') }
        if (-not $skip) { $lines.Add($line) }
    }
    if ($Remove) {
        $result.action = 'removed'
    } else {
        $quoted = @($ReadRoot | ForEach-Object { "'" + $_ + "'" })
        $lines.Add('')
        $lines.Add('[files]')
        $lines.Add('read_roots = [' + ($quoted -join ', ') + ']')
        $result.action = 'set'
    }

    $text = ((@($lines) -join "`r`n").Trim()) + "`r`n"
    Assert-WinBridgeLocalConfig -Text $text
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($localPath, $text, $encoding)

    $state = Get-WinBridgeState
    $configPath = Write-WinBridgeConfig -State $state
    $check = Invoke-WinBridgeNative -FilePath $goExe -Arguments @('--config', $configPath, '--print-config')
    if ($check.ExitCode -ne 0) {
        [IO.File]::WriteAllText($localPath, ((@($previous) -join "`r`n") + "`r`n"), $encoding)
        Write-WinBridgeConfig -State $state | Out-Null
        throw ('The agent rejected the configuration, so it was put back: ' + (Redact-WinBridgeText $check.Output))
    }
    if (-not $NoRestart) {
        & (Join-Path $binRoot 'Restart-WinBridge.ps1') -Json | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'The configuration is saved but the agent did not restart; run restart from the Mac.' }
    }
    $result.ok = $true
} catch {
    $result.error = Redact-WinBridgeText $_.Exception.Message
}

if ($Json) { Write-Output ($result | ConvertTo-Json -Compress) } else { Write-Output $result }
if (-not $result.ok) { exit 1 }
