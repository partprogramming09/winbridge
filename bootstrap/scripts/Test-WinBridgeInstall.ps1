# Read-only WinBridge doctor. Safe to run before and after pairing.

param(
    [switch]$Json,
    [switch]$PrePairing,
    [switch]$Quiet
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Tailscale.ps1')
. (Join-Path $binRoot 'lib\WinBridgeConfig.ps1')
. (Join-Path $binRoot 'lib\OpenSsh.ps1')
. (Join-Path $binRoot 'lib\Firewall.ps1')
. (Join-Path $binRoot 'lib\ScheduledTask.ps1')
. (Join-Path $binRoot 'lib\Doctor.ps1')

Set-WinBridgeUtf8Output
if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

$result = Invoke-WinBridgeDoctor -PrePairing:$PrePairing

if ($Json) {
    Write-Output ($result | ConvertTo-Json -Depth 10)
} else {
    Write-WinBridgeDoctorReport -Result $result -Quiet:$Quiet
}

if ($result.ok) { exit 0 }
exit 1
