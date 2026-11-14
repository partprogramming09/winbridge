# Selects which WinBridge server the supervisor runs: the Python agent or the Go agent.
# Everything it touches is under %LOCALAPPDATA%\winbridge, so no elevation is needed.
# Going back is `-Agent python`; the supervisor also goes back by itself when the Go agent fails /health.

param(
    [Parameter(Mandatory = $true)][ValidateSet('go', 'python')][string]$Agent,
    [string]$ExePath,
    [string]$ExpectedSha256,
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

$utf8 = New-Object System.Text.UTF8Encoding($false)
$stateDir = Get-WinBridgePath 'state'
$agentFlag = Join-Path $stateDir 'agent.txt'
$maintenanceFlag = Join-Path $stateDir 'maintenance.flag'
$goDir = Get-WinBridgePath 'app\go'
$goExe = Join-Path $goDir 'winbridge-agent.exe'

try {
    Ensure-WinBridgeDirectory -Path $stateDir
    if ($Agent -eq 'go' -and $ExePath) {
        if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) { throw ('Missing file: ' + $ExePath) }
        if (-not $ExpectedSha256) { throw '-ExpectedSha256 is required with -ExePath.' }
        $actual = (Get-FileHash -LiteralPath $ExePath -Algorithm SHA256).Hash
        if ($actual -ne $ExpectedSha256.ToUpperInvariant()) { throw ('SHA-256 mismatch: got ' + $actual) }
        Ensure-WinBridgeDirectory -Path $goDir
        [IO.File]::WriteAllText($maintenanceFlag, (Get-Date).ToString('o'), $utf8)
        try {
            Start-Sleep -Seconds 4
            Copy-Item -LiteralPath $ExePath -Destination $goExe -Force
        } finally {
            Remove-Item -LiteralPath $maintenanceFlag -Force -ErrorAction SilentlyContinue
        }
    }
    if ($Agent -eq 'go' -and -not (Test-Path -LiteralPath $goExe)) {
        throw 'The Go agent is not installed; pass -ExePath and -ExpectedSha256.'
    }

    [IO.File]::WriteAllText($agentFlag, $Agent, $utf8)
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) { throw 'Tailscale is not connected.' }
    $port = Get-WinBridgePort
    $restartFlag = Join-Path $stateDir 'restart.flag'
    [IO.File]::WriteAllText($restartFlag, (Get-Date).ToString('o'), $utf8)
    Start-WinBridgeTask | Out-Null
    $healthUri = 'http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $port + '/health'
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $health = Wait-WinBridgeHttp -Uri $healthUri -TimeoutSeconds $TimeoutSeconds
        $isGo = ([string]$health.upstream -eq 'native-go')
        if ($isGo -eq ($Agent -eq 'go')) { break }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    $running = (Get-Content -LiteralPath $agentFlag -Raw).Trim()
    $result = [pscustomobject]@{
        ok = ($running -eq $Agent)
        requested = $Agent
        running = $running
        upstream = [string]$health.upstream
        version = [string]$health.version
    }
    if ($Json) { Write-Output ($result | ConvertTo-Json -Compress) } else { Write-Output $result }
    if (-not $result.ok) { exit 1 }
} catch {
    $message = $_.Exception.Message
    Write-WinBridgeError $message
    if ($Json) { Write-Output ((@{ ok = $false; error = $message } | ConvertTo-Json -Compress)) }
    exit 1
}
