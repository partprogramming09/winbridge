# Points the agent at the job runner folder: writes the [jobs] table to config\local.toml (kept across updates),
# checks the result with the agent and restarts it. -Remove deletes the table. It runs as the owner and needs no elevation.

param(
    [string]$Root = 'C:\ProgramData\WinBridgeJobs',
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

$result = [pscustomobject]@{ ok = $false; root = $Root; action = $null; error = $null }

try {
    if (-not $Remove -and -not [IO.Path]::IsPathRooted($Root)) { throw '-Root must be an absolute path.' }
    if ($Root -match "['\r\n]") { throw '-Root cannot hold a single quote or a line break.' }
    $configDir = Split-Path -Parent (Get-WinBridgeConfigPath)
    $localPath = Join-Path $configDir 'local.toml'
    $goExe = Get-WinBridgePath 'app\go\winbridge-agent.exe'
    if (-not (Test-Path -LiteralPath $goExe)) { throw 'This is not a Go install; use the installer.' }
    Ensure-WinBridgeDirectory -Path $configDir

    $existing = @()
    if (Test-Path -LiteralPath $localPath) { $existing = @(Get-Content -LiteralPath $localPath) }
    $previous = $existing
    $lines = New-Object System.Collections.Generic.List[string]
    $skip = $false
    foreach ($line in $existing) {
        $t = $line.Trim()
        if ($t.StartsWith('[')) { $skip = ($t -eq '[jobs]') }
        if (-not $skip) { $lines.Add($line) }
    }
    if ($Remove) {
        $result.action = 'removed'
    } else {
        $lines.Add('')
        $lines.Add('[jobs]')
        $lines.Add("root = '" + $Root + "'")
        $result.action = 'set'
    }

    $text = ((@($lines) -join "`r`n").Trim()) + "`r`n"
    Assert-WinBridgeLocalConfig -Text $text
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($localPath, $text, $encoding)

    $state = Get-WinBridgeState
    $configPath = Write-WinBridgeConfig -State $state
    $check = & $goExe --config $configPath --print-config 2>&1
    if ($LASTEXITCODE -ne 0) {
        [IO.File]::WriteAllText($localPath, ((@($previous) -join "`r`n") + "`r`n"), $encoding)
        Write-WinBridgeConfig -State $state | Out-Null
        throw ('The agent rejected the configuration, so it was put back: ' + (Redact-WinBridgeText (($check | Out-String).Trim())))
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
