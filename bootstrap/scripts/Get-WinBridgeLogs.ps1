# Read WinBridge logs with defense-in-depth redaction.

param(
    [int]$Lines = 200,
    [ValidateSet('all', 'server', 'supervisor', 'bootstrap')][string]$Kind = 'all',
    [switch]$Follow,
    [switch]$Json
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')

Set-WinBridgeUtf8Output
if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

$logDir = Get-WinBridgePath 'logs'
$files = @()
if ($Kind -eq 'all' -or $Kind -eq 'server') {
    $files += @(
        (Join-Path $logDir 'server.out.log'),
        (Join-Path $logDir 'server.err.log')
    )
}
if ($Kind -eq 'all' -or $Kind -eq 'supervisor') {
    $files += (Join-Path $logDir 'supervisor.log')
}
if ($Kind -eq 'all' -or $Kind -eq 'bootstrap') {
    $files += (Join-Path $logDir 'bootstrap.log')
}
$existing = @($files | Where-Object { Test-Path -LiteralPath $_ })

if ($Follow) {
    $latest = @($existing | ForEach-Object { Get-Item -LiteralPath $_ } | Sort-Object LastWriteTime | Select-Object -Last 1)
    if ($latest.Count -eq 0) { throw 'No WinBridge logs exist yet.' }
    Get-Content -LiteralPath $latest[0].FullName -Tail $Lines -Wait | ForEach-Object {
        Write-Output (Redact-WinBridgeText ([string]$_))
    }
    exit 0
}

$output = @()
foreach ($file in $existing) {
    $output += ('--- ' + (Split-Path -Leaf $file) + ' ---')
    foreach ($line in (Get-WinBridgeLogTail -Path $file -Lines $Lines)) {
        $output += (Redact-WinBridgeText ([string]$line))
    }
}

if ($Json) {
    Write-Output ([pscustomobject]@{
        ok = $true
        files = @($existing | ForEach-Object { Split-Path -Leaf $_ })
        lines = $output
    } | ConvertTo-Json -Depth 5 -Compress)
} else {
    foreach ($line in $output) { Write-Output $line }
}
