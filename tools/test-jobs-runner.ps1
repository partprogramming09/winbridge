#requires -Version 5.1
# Runs the real job runner (bootstrap\jobs\runner.ps1) over a throwaway folder and checks what it does with requests:
# runs, rejects, times out, cancels. Needs no elevation and touches nothing outside a temp folder.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\test-jobs-runner.ps1

[CmdletBinding()]
param()

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$runner = Join-Path (Split-Path -Parent $PSScriptRoot) 'bootstrap\jobs\runner.ps1'
$root = Join-Path ([IO.Path]::GetTempPath()) ('wbjobs-' + [guid]::NewGuid().ToString('N'))
foreach ($d in 'inbox', 'out', 'cancel', 'scripts') { New-Item -ItemType Directory -Force -Path (Join-Path $root $d) | Out-Null }
$failures = New-Object System.Collections.Generic.List[string]

# Writes a job script and returns its manifest entry.
function New-JobScript($name, $body, $params, $timeout, $session) {
    $path = Join-Path $root "scripts\$name"
    Set-Content -Path $path -Value $body -Encoding ASCII
    return [ordered]@{ script = $name; sha256 = (Get-FileHash $path -Algorithm SHA256).Hash; params = $params; timeout_s = $timeout; session = $session }
}

$jobs = [ordered]@{
    echo    = New-JobScript 'echo.ps1' 'param([string]$Word) Write-Output "word=$Word"' @{ Word = '^[a-z ]{1,20}$' } 20 1
    raw     = New-JobScript 'raw.ps1' 'param([string]$Word) Write-Output "raw=$Word"' @{ Word = '^.*$' } 20 1
    loose   = New-JobScript 'loose.ps1' 'param([string]$Word) Write-Output "loose=$Word"' @{ Word = '[a-z]+' } 20 1
    fail    = New-JobScript 'fail.ps1' 'exit 3' @{} 20 1
    sleeper = New-JobScript 'sleeper.ps1' 'Start-Sleep -Seconds 60' @{} 2 1
    longer  = New-JobScript 'longer.ps1' 'Start-Sleep -Seconds 60' @{} 120 1
    svc     = New-JobScript 'svc.ps1' 'exit 0' @{} 20 0
    envdump = New-JobScript 'envdump.ps1' 'Write-Output "temp=$env:TEMP"; Write-Output "tmp=$env:TMP"; Write-Output "path=$env:PATH"; Write-Output "mods=$env:PSModulePath"; Write-Output "prof=[$env:COR_ENABLE_PROFILING$env:COR_PROFILER$env:COR_PROFILER_PATH$env:CORECLR_PROFILER$env:DOTNET_STARTUP_HOOKS]"' @{} 20 1
    envjob  = New-JobScript 'envjob.ps1' 'Write-Output "env=$env:WB_TEST_ENV"' @{} 20 1
    swapenv = New-JobScript 'swapenv.ps1' '$p = Join-Path (Split-Path -Parent $PSScriptRoot) "manifest.json"; $m = Get-Content $p -Raw | ConvertFrom-Json; $m.env = [pscustomobject]@{}; [IO.File]::WriteAllText($p, ($m | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding $false)); Write-Output "swapped"' @{} 20 1
    envafter = New-JobScript 'envafter.ps1' 'Write-Output "after=[$env:WB_TEST_ENV]"' @{} 20 1
    tamper  = New-JobScript 'tamper.ps1' 'exit 0' @{} 20 1
}
[ordered]@{ jobs = $jobs; env = [ordered]@{ WB_TEST_ENV = 'from-manifest' } } | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $root 'manifest.json') -Encoding UTF8
Add-Content (Join-Path $root 'scripts\tamper.ps1') 'Write-Output changed'

# Writes a request file the way the agent does (temp file, then rename).
function Send-Job($id, $job, $jobArgs) {
    $body = @{ job = $job; args = $jobArgs; queue = $false } | ConvertTo-Json -Compress
    Set-Content -Path (Join-Path $root "inbox\$id.tmp") -Value $body -Encoding ASCII
    Rename-Item (Join-Path $root "inbox\$id.tmp") "$id.json"
}

# Returns the status object of a finished job.
function Get-State($id) { Get-Content (Join-Path $root "out\$id.status.json") -Raw | ConvertFrom-Json }

# Records one check.
function Expect($name, $condition) {
    if ($condition) { Write-Host "ok   $name" } else { Write-Host "FAIL $name"; $failures.Add($name) }
}

Send-Job 'a-echo' 'echo' @{ Word = 'hello' }
Send-Job 'b-badvalue' 'echo' @{ Word = 'x; calc.exe' }
Send-Job 'c-quote' 'raw' @{ Word = 'a"b' }
Send-Job 'c2-backslash' 'raw' @{ Word = 'C:\dir\' }
Send-Job 'c3-case' 'echo' @{ Word = 'ABC' }
Send-Job 'c4-dash' 'raw' @{ Word = '-Other' }
Send-Job 'c5-intrinsic' 'echo' @{ Count = '1' }
Send-Job 'c6-substring' 'loose' @{ Word = 'abc1' }
Send-Job 'c7-intrinsic-job' 'Count' @{}
Send-Job 'd-unknownparam' 'echo' @{ Evil = 'x' }
Send-Job 'e-unknownjob' 'calc' @{}
Send-Job 'f-fail' 'fail' @{}
Send-Job 'g-timeout' 'sleeper' @{}
Send-Job 'h-tamper' 'tamper' @{}
Send-Job 'i-session0' 'svc' @{}
Send-Job 'j-cancelled-early' 'echo' @{ Word = 'x' }
New-Item -ItemType File -Path (Join-Path $root 'cancel\j-cancelled-early') | Out-Null
# Leftovers the runner finds at start: old finished jobs (to prune), an old and a recent one it must keep, a job that was
# running when the runner died, stale cancel markers and old TEMP files.
function New-Leftover($id, $state, $daysOld) {
    $when = (Get-Date).AddDays(-$daysOld)
    $statusPath = Join-Path $root "out\$id.status.json"
    @{ id = $id; job = 'echo'; state = $state } | ConvertTo-Json | Set-Content -Path $statusPath -Encoding ASCII
    foreach ($suffix in '.log', '.err.log', '.request.json') { Set-Content -Path (Join-Path $root "out\$id$suffix") -Value 'x' -Encoding ASCII }
    foreach ($p in (Get-ChildItem (Join-Path $root 'out') -Filter "$id.*")) { $p.LastWriteTime = $when }
}
New-Leftover 'old-done' 'done' 40
New-Leftover 'old-running' 'running' 40
New-Leftover 'recent-done' 'done' 2
Set-Content -Path (Join-Path $root 'out\notes.txt') -Value 'not a job file' -Encoding ASCII
(Get-Item (Join-Path $root 'out\notes.txt')).LastWriteTime = (Get-Date).AddDays(-40)
New-Item -ItemType Directory -Force -Path (Join-Path $root 'tmp') | Out-Null
Set-Content -Path (Join-Path $root 'cancel\stale-marker') -Value '' -Encoding ASCII
Set-Content -Path (Join-Path $root 'tmp\old.tmp') -Value 'x' -Encoding ASCII
Set-Content -Path (Join-Path $root 'tmp\new.tmp') -Value 'x' -Encoding ASCII
(Get-Item (Join-Path $root 'cancel\stale-marker')).LastWriteTime = (Get-Date).AddDays(-3)
# cancel\ is written by the agent: a subfolder in it must never be entered by the (elevated) cleanup.
New-Item -ItemType Directory -Force -Path (Join-Path $root 'cancel\sub') | Out-Null
Set-Content -Path (Join-Path $root 'cancel\sub\nested-old.txt') -Value 'x' -Encoding ASCII
(Get-Item (Join-Path $root 'cancel\sub\nested-old.txt')).LastWriteTime = (Get-Date).AddDays(-3)
Set-Content -Path (Join-Path $root 'cancel\fresh-marker') -Value '' -Encoding ASCII
# Names Win32 normalises (trailing dot or space) cannot be removed by a plain path; one of them must not stop the runner.
foreach ($hostile in 'shadow.', 'spaced ', '...') {
    $hostilePath = '\\?\' + (Join-Path $root "cancel\$hostile")
    [IO.File]::WriteAllText($hostilePath, 'x')
    [IO.File]::SetLastWriteTime($hostilePath, (Get-Date).AddDays(-3))
}
(Get-Item (Join-Path $root 'tmp\old.tmp')).LastWriteTime = (Get-Date).AddDays(-3)

Send-Job 'l-env' 'envjob' @{}
# n1 removes the manifest env; n2 runs after it in the same runner process and must not still see the variable.
Send-Job 'n1-swap' 'swapenv' @{}
Send-Job 'n2-after' 'envafter' @{}
Send-Job 'l2-envdump' 'envdump' @{}
Send-Job '..bad' 'echo' @{ Word = 'x' }

# The runner is started with a hostile PATH, module path and TEMP, as a process of the same account could arrange.
$saved = @{ PATH = $env:PATH; PSModulePath = $env:PSModulePath; TEMP = $env:TEMP; TMP = $env:TMP }
$hookNames = 'COR_ENABLE_PROFILING', 'COR_PROFILER', 'COR_PROFILER_PATH', 'CORECLR_PROFILER', 'DOTNET_STARTUP_HOOKS'
foreach ($n in $hookNames) { Set-Item -Path "Env:$n" -Value 'wb-evil-hook' }
$env:PATH = 'C:\wb-evil-path;' + $env:PATH
$env:PSModulePath = 'C:\wb-evil-modules'
$env:TEMP = 'C:\wb-evil-temp'
$env:TMP = 'C:\wb-evil-temp'
& powershell -NoProfile -ExecutionPolicy Bypass -File $runner -Root $root -Once
$runnerExit = $LASTEXITCODE
foreach ($k in $saved.Keys) { Set-Item -Path "Env:$k" -Value $saved[$k] }
foreach ($n in $hookNames) { Remove-Item -Path "Env:$n" }
Expect 'runner exits after the inbox is empty' ($runnerExit -eq 0)

$s = Get-State 'a-echo'
Expect 'echo job is done with exit code 0' ($s.state -eq 'done' -and $s.exit_code -eq 0)
Expect 'echo job log holds the parameter' ((Get-Content (Join-Path $root 'out\a-echo.log') -Raw) -match 'word=hello')
Expect 'a value that breaks the regex is rejected' ((Get-State 'b-badvalue').state -eq 'rejected')
Expect 'a quote in a value is rejected even when the regex allows it' ((Get-State 'c-quote').state -eq 'rejected' -and (Get-State 'c-quote').error -match 'unsafe')
Expect 'a trailing backslash is rejected even when the regex allows it' ((Get-State 'c2-backslash').state -eq 'rejected' -and (Get-State 'c2-backslash').error -match 'unsafe')
Expect 'manifest regexes are case sensitive' ((Get-State 'c3-case').state -eq 'rejected')
Expect 'a value that starts with a dash is rejected' ((Get-State 'c4-dash').state -eq 'rejected')
Expect 'an intrinsic member name is not a parameter' ((Get-State 'c5-intrinsic').state -eq 'rejected' -and (Get-State 'c5-intrinsic').error -match 'not allowed')
Expect 'a regex must match the whole value' ((Get-State 'c6-substring').state -eq 'rejected')
Expect 'an intrinsic member name is not a job' ((Get-State 'c7-intrinsic-job').state -eq 'rejected' -and (Get-State 'c7-intrinsic-job').error -match 'not allowed')
Expect 'a parameter the manifest does not list is rejected' ((Get-State 'd-unknownparam').state -eq 'rejected')
Expect 'a job the manifest does not list is rejected' ((Get-State 'e-unknownjob').state -eq 'rejected')
Expect 'a failing script is failed with its exit code' ((Get-State 'f-fail').state -eq 'failed' -and (Get-State 'f-fail').exit_code -eq 3)
Expect 'a job past its limit is timeout' ((Get-State 'g-timeout').state -eq 'timeout')
Expect 'a script changed after install is rejected by hash' ((Get-State 'h-tamper').state -eq 'rejected' -and (Get-State 'h-tamper').error -match 'hash')
Expect 'a session 0 job is rejected' ((Get-State 'i-session0').state -eq 'rejected')
Expect 'a job cancelled before it starts is cancelled' ((Get-State 'j-cancelled-early').state -eq 'cancelled')
Expect 'an id that is not a plain name leaves no status file' (-not (Test-Path (Join-Path $root 'out\..bad.status.json')))
Expect 'requests leave the inbox' (@(Get-ChildItem (Join-Path $root 'inbox') -Filter '*.json').Count -eq 0)
Expect 'finished jobs older than the retention are removed with all their files' (-not (Test-Path (Join-Path $root 'out\old-done.status.json')) -and -not (Test-Path (Join-Path $root 'out\old-done.log')) -and -not (Test-Path (Join-Path $root 'out\old-done.request.json')))
Expect 'recent jobs are kept' (Test-Path (Join-Path $root 'out\recent-done.status.json'))
Expect 'a job left running by a dead runner is marked failed, not left running, and kept' ((Get-State 'old-running').state -eq 'failed' -and (Get-State 'old-running').error -match 'stopped')
Expect 'files that are not job files are never removed' (Test-Path (Join-Path $root 'out\notes.txt'))
Expect 'stale cancel markers are removed' (-not (Test-Path (Join-Path $root 'cancel\stale-marker')))
Expect 'a hostile file name in cancel\ does not stop the runner' ($runnerExit -eq 0)
Expect 'cleanup removes old files in cancel\ but never enters subfolders' (-not (Test-Path (Join-Path $root 'cancel\stale-marker')) -and (Test-Path (Join-Path $root 'cancel\sub\nested-old.txt')) -and (Test-Path (Join-Path $root 'cancel\fresh-marker')))
Expect 'old TEMP files are removed and recent ones kept' (-not (Test-Path (Join-Path $root 'tmp\old.tmp')) -and (Test-Path (Join-Path $root 'tmp\new.tmp')))
$dump = Get-Content (Join-Path $root 'out\l2-envdump.log') -Raw
Expect 'a job gets a TEMP inside the protected root' ($dump -match [regex]::Escape("temp=$root\tmp") -and $dump -match [regex]::Escape("tmp=$root\tmp"))
Expect 'a job does not inherit the hostile PATH' ($dump -notmatch 'wb-evil-path' -and $dump -match 'path=[^\r\n]*System32')
Expect 'runner clears profiler and hook variables' ($dump -match 'prof=\[\]')
Expect 'a job gets the system module path only' ($dump -notmatch 'wb-evil-modules' -and $dump -match 'mods=[^\r\n]*WindowsPowerShell\\v1\.0\\Modules')
Expect 'an env variable a manifest no longer sets does not leak into later jobs of the same runner' ((Get-Content (Join-Path $root 'out\n2-after.log') -Raw) -match 'after=\[\]')
Expect 'the manifest env reaches the job' ((Get-Content (Join-Path $root 'out\l-env.log') -Raw) -match 'env=from-manifest')

# Cancel a running job: start the runner, wait for "running", drop the marker.
Send-Job 'k-running' 'longer' @{}
$proc = Start-Process powershell -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', ('"{0}"' -f $runner), '-Root', ('"{0}"' -f $root), '-Once') -PassThru -WindowStyle Hidden
$deadline = (Get-Date).AddSeconds(30)
$reached = $false
while ((Get-Date) -lt $deadline -and -not $reached) {
    Start-Sleep -Milliseconds 200
    try { $reached = ((Get-State 'k-running').state -eq 'running') } catch { }  # the file may be mid-rename
}
Expect 'the long job reached running before the cancel' $reached
Start-Sleep -Seconds 2  # lets the child process start
New-Item -ItemType File -Path (Join-Path $root 'cancel\k-running') | Out-Null
Expect 'the runner ends after the cancel' ($proc.WaitForExit(30000))
Expect 'a running job becomes cancelled' ((Get-State 'k-running').state -eq 'cancelled')
Start-Sleep -Seconds 1
Expect 'the cancelled job left no powershell child' (@(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object { $_.CommandLine -like '*longer.ps1*' }).Count -eq 0)

Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
if ($failures.Count -gt 0) { Write-Host ("{0} check(s) failed" -f $failures.Count); exit 1 }
Write-Host 'job runner checks passed'
