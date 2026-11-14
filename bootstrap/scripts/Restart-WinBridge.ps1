# Restart the WinBridge server through the supervisor. No elevation needed.

param(
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

$flag = $null
try {
    # An SSH session has no Credential Manager; the pairing check is skipped there, as the doctor does.
    try {
        $credential = Test-WinBridgeCredential
    } catch {
        if (-not $env:SSH_CONNECTION) { throw }
        $credential = $null
    }
    if ($null -ne $credential -and -not $credential.Present) {
        throw 'WinBridge is not paired. Run adopt from the Mac first.'
    }
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) {
        throw 'Tailscale is not connected.'
    }
    $port = Get-WinBridgePort
    $flag = Get-WinBridgePath 'state\restart.flag'
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $flag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($flag, (Get-Date).ToString('o'), $encoding)
    Start-WinBridgeTask | Out-Null
    # The supervisor deletes the flag when it begins the restart; waiting for that proves it acted.
    $consumeDeadline = (Get-Date).AddSeconds([Math]::Min($TimeoutSeconds, 30))
    while ((Test-Path -LiteralPath $flag) -and (Get-Date) -lt $consumeDeadline) { Start-Sleep -Milliseconds 500 }
    if (Test-Path -LiteralPath $flag) { throw 'The supervisor did not pick up the restart request; it may not be running.' }
    $health = Wait-WinBridgeHttp -Uri ('http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $port + '/health') -TimeoutSeconds $TimeoutSeconds
    $result = [pscustomobject]@{
        ok = $true
        version = [string]$health.version
        profile = [string]$health.profile
        tailscale_ip = $tailscaleIp
        port = $port
    }
    if ($Json) {
        Write-Output ($result | ConvertTo-Json -Compress)
    } else {
        Write-Output $result
    }
} catch {
    $message = $_.Exception.Message
    if ($flag -and (Test-Path -LiteralPath $flag)) {
        Remove-Item -LiteralPath $flag -Force -ErrorAction SilentlyContinue
    }
    Write-WinBridgeError $message
    if ($Json) {
        Write-Output ((@{ ok = $false; error = $message } | ConvertTo-Json -Compress))
    }
    exit 1
}
