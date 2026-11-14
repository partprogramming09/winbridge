param([Parameter(Mandatory = $true)][string]$App)

# Stops the application that app-run started, and what it started. The pid is checked against its start time first, so a
# reused pid is never killed. The record lives in the administrators-only root, so the agent cannot edit it.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if ($App -cnotmatch '^[A-Za-z0-9_-]{1,40}$') { throw 'invalid app name' }
$record = Join-Path $root "state\apps\$App.json"
if (-not (Test-Path -LiteralPath $record)) { throw "$App was not started by app-run" }
$old = Get-Content $record -Raw | ConvertFrom-Json
$p = Get-Process -Id $old.pid -ErrorAction SilentlyContinue
if (-not $p -or $p.StartTime.ToUniversalTime().ToString('o') -ne $old.started) {
    Remove-Item -LiteralPath $record -Force
    Write-Output "$App was not running (its record is cleared)"
    exit 0
}
& taskkill /PID $old.pid /T /F | Out-Null
Remove-Item -LiteralPath $record -Force
Write-Output "stopped $App (pid $($old.pid))"
