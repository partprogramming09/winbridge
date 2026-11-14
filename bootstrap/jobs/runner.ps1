<#
.SYNOPSIS
    Elevated job runner: runs only the jobs listed in manifest.json, one at a time, in the desktop session.
.DESCRIPTION
    Started once by the scheduled task WinBridgeJobs-Run (interactive, highest privileges) and kept running. It reads
    request files from inbox\ ({"job","args","queue"}), validates the job against manifest.json (script SHA-256, one
    regex per parameter), runs it with a time limit and writes out\<id>.status.json and out\<id>.log. A file in cancel\
    named like the job id stops a queued or running job and everything it started.
    States: running, done, failed, timeout, rejected, cancelled. The agent reports "queued" for a request still in inbox\.
.PARAMETER Root
    Protected folder: only administrators and SYSTEM may write manifest.json, runner.ps1 and scripts\.
.PARAMETER Once
    Process what is in the inbox and exit (used by the tests); without it the runner keeps polling.
#>
param(
    [string]$Root = 'C:\ProgramData\WinBridgeJobs',
    [switch]$Once,
    [int]$KeepDays = 14
)

$ErrorActionPreference = 'Stop'
$inbox = Join-Path $Root 'inbox'
$out = Join-Path $Root 'out'
$cancelDir = Join-Path $Root 'cancel'
$scripts = Join-Path $Root 'scripts'

# Appends a timestamped line to runner.log in the protected root (not in out\, where job logs are named by request ids).
# Control characters are replaced so a job name cannot forge a second line.
function Write-Log($msg) { Add-Content -Path (Join-Path $Root 'runner.log') -Value ('{0} {1}' -f (Get-Date -Format o), ([string]$msg -replace '[\x00-\x1f]', ' ')) }

$systemPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'

# The runner shares its account with the agent, so what that account can write must not reach the elevated jobs: they
# get system-only PATH and module paths, and a TEMP that only administrators can write (Add-Type compiles there).
# The manifest's env section, written by an administrator, is applied on top of this for each job.
$tmp = Join-Path $Root 'tmp'
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
$sys32 = Join-Path $env:SystemRoot 'System32'
$env:PATH = @($sys32, $env:SystemRoot, (Join-Path $sys32 'Wbem'), (Join-Path $sys32 'WindowsPowerShell\v1.0')) -join ';'
$env:PSModulePath = Join-Path $sys32 'WindowsPowerShell\v1.0\Modules'
$env:TEMP = $tmp
$env:TMP = $tmp
# Profiler and startup-hook variables of the agent's account would load code into every job. The runner process itself
# was started before this line, so these cannot protect it (known limit, see SECURITY.md); they do protect the jobs.
foreach ($hook in 'COR_ENABLE_PROFILING', 'COR_PROFILER', 'COR_PROFILER_PATH', 'CORECLR_ENABLE_PROFILING', 'CORECLR_PROFILER', 'CORECLR_PROFILER_PATH', 'DOTNET_STARTUP_HOOKS') {
    Remove-Item -LiteralPath "Env:$hook" -ErrorAction SilentlyContinue
}
# The hardened environment is the baseline each job starts from; manifest env names applied by a job are undone after it.
$script:baseEnv = @{}
foreach ($e in [Environment]::GetEnvironmentVariables('Process').GetEnumerator()) { $script:baseEnv[[string]$e.Key] = [string]$e.Value }
$script:appliedEnv = @()

# Stores the state of one job in out\<id>.status.json.
function Save-Status($id, $status) {
    # Written to a temp file and renamed so a reader never sees half a file; no BOM, so every JSON parser reads it.
    $tmp = Join-Path $out "$id.status.tmp"
    [IO.File]::WriteAllText($tmp, ($status | ConvertTo-Json), (New-Object Text.UTF8Encoding $false))
    for ($i = 0; $i -lt 10; $i++) {
        try { Move-Item -LiteralPath $tmp -Destination (Join-Path $out "$id.status.json") -Force; return } catch { Start-Sleep -Milliseconds 50 }
    }
    Move-Item -LiteralPath $tmp -Destination (Join-Path $out "$id.status.json") -Force
}

# Returns true, and removes the marker, when a cancel request exists for the job.
function Test-Cancel($id) {
    $marker = Join-Path $cancelDir $id
    if (Test-Path -LiteralPath $marker) { Remove-Item -LiteralPath $marker -Force -ErrorAction SilentlyContinue; return $true }
    return $false
}

# Validates one request against the manifest, runs the job and archives the request.
function Invoke-Job($file) {
    $id = [IO.Path]::GetFileNameWithoutExtension($file.Name)
    if ($id -cnotmatch '^[A-Za-z0-9_-]{1,40}$') {
        Write-Log "discarded a request with an invalid id: $($file.Name)"
        Remove-Item -LiteralPath $file.FullName -Force
        return
    }
    $status = [ordered]@{ id = $id; job = $null; state = 'running'; started = (Get-Date -Format o); finished = $null; exit_code = $null; error = $null }
    try {
        Save-Status $id $status
        # Archive the request before running it: a crash must not make the runner execute it a second time.
        $archived = Join-Path $out "$id.request.json"
        Move-Item -LiteralPath $file.FullName -Destination $archived -Force
        if (Test-Cancel $id) { $status.state = 'cancelled'; throw 'cancelled before it started' }
        $req = Get-Content $archived -Raw | ConvertFrom-Json
        $status.job = [string]$req.job
        Save-Status $id $status  # so a "busy" answer can name the job that is running
        $manifest = Get-Content (Join-Path $Root 'manifest.json') -Raw | ConvertFrom-Json
        # Exact, case-sensitive lookup: dotted member access would also answer to intrinsic members such as Count.
        $jobProp = $manifest.jobs.PSObject.Properties[[string]$req.job]
        if (-not $jobProp -or $jobProp.Name -cne [string]$req.job) { throw 'job not allowed' }
        $entry = $jobProp.Value
        if ($null -ne $entry.session -and [int]$entry.session -eq 0) { throw 'session 0 jobs are not supported by this runner' }
        $script = Join-Path $scripts $entry.script
        if ((Get-FileHash $script -Algorithm SHA256).Hash -ne $entry.sha256) { throw "the hash of $($entry.script) does not match the manifest" }

        # Parameters become quoted arguments; a quote or a trailing backslash could change what the quoting means.
        $argList = @()
        if ($req.args) {
            foreach ($p in @($req.args.PSObject.Properties)) {
                if ($p.Name -cnotmatch '^[A-Za-z][A-Za-z0-9]{0,40}$') { throw "invalid parameter name: $($p.Name)" }
                $paramProp = $entry.params.PSObject.Properties[$p.Name]
                if (-not $paramProp -or $paramProp.Name -cne $p.Name) { throw "parameter not allowed: $($p.Name)" }
                $value = [string]$p.Value
                if ($value.Length -gt 200 -or $value.StartsWith('-')) { throw "unsafe value for $($p.Name)" }
                if ($value -cnotmatch ('\A(?:' + [string]$paramProp.Value + ')\z')) { throw "invalid value for $($p.Name)" }
                if ($value -match '["\x00-\x1f]' -or $value.EndsWith('\')) { throw "unsafe characters in $($p.Name)" }
                $argList += "-$($p.Name)"
                $argList += ('"{0}"' -f $value)
            }
        }

        # Undo what the previous job's manifest env set (this process is long-lived), then apply this manifest's env.
        foreach ($name in @($script:appliedEnv)) {
            if ($script:baseEnv.ContainsKey($name)) { Set-Item -Path ("Env:" + $name) -Value $script:baseEnv[$name] }
            else { Remove-Item -Path ("Env:" + $name) -ErrorAction SilentlyContinue }
        }
        $script:appliedEnv = @()
        if ($manifest.env) {
            foreach ($pair in @($manifest.env.PSObject.Properties)) {
                Set-Item -Path ("Env:" + $pair.Name) -Value ([string]$pair.Value)
                $script:appliedEnv += $pair.Name
            }
        }
        $log = Join-Path $out "$id.log"
        $err = Join-Path $out "$id.err.log"
        $psArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', ('"{0}"' -f $script)) + $argList
        $proc =Start-Process $systemPowerShell -ArgumentList $psArgs -WindowStyle Hidden -PassThru -RedirectStandardOutput $log -RedirectStandardError $err
        $null = $proc.Handle  # without the cached handle ExitCode can be null after the wait
        $limit = (Get-Date).AddSeconds([int]$entry.timeout_s)
        while (-not $proc.WaitForExit(500)) {
            if (Test-Cancel $id) {
                & taskkill /PID $proc.Id /T /F | Out-Null
                $status.state = 'cancelled'
                break
            }
            if ((Get-Date) -gt $limit) {
                & taskkill /PID $proc.Id /T /F | Out-Null
                $status.state = 'timeout'
                $status.error = "exceeded $($entry.timeout_s) s"
                break
            }
        }
        if ($status.state -eq 'running') {
            $status.exit_code = $proc.ExitCode
            $status.state = if ($proc.ExitCode -eq 0) { 'done' } else { 'failed' }
        }
    } catch {
        if ($status.state -ne 'cancelled') { $status.state = 'rejected' }
        $status.error = $_.Exception.Message
    }
    $status.finished = Get-Date -Format o
    Save-Status $id $status
    Write-Log ('{0} job={1} state={2} {3}' -f $id, $status.job, $status.state, $status.error)
}

# A job still marked running when the runner starts was cut off (reboot, crash): nothing is running yet, so it ended.
# Left alone it would make the agent report "busy" for ever.
function Repair-Interrupted {
    foreach ($f in @(Get-ChildItem -LiteralPath $out -Filter '*.status.json' -File -ErrorAction SilentlyContinue)) {
        try {
            $s = Get-Content -LiteralPath $f.FullName -Raw | ConvertFrom-Json
            if ($s.state -ne 'running') { continue }
            $id = $f.Name -replace '\.status\.json$', ''
            Save-Status $id ([ordered]@{
                id = $id; job = $s.job; state = 'failed'; started = $s.started; finished = (Get-Date -Format o)
                exit_code = $null; error = 'the runner stopped while this job was running'
            })
            Write-Log "$id job=$($s.job) was interrupted: marked failed"
        } catch { Write-Log "could not check $($f.Name): $($_.Exception.Message)" }
    }
}

# Removes what nobody needs any more: the files of jobs that ended more than KeepDays ago (never one that is running),
# cancel markers and TEMP files older than a day. Only files named like the runner's own are touched.
function Remove-OldFiles {
    $cutoff = (Get-Date).AddDays(-$KeepDays)
    $byId = @{}
    foreach ($f in @(Get-ChildItem -LiteralPath $out -File -ErrorAction SilentlyContinue)) {
        if ($f.Name -cmatch '^([A-Za-z0-9_-]{1,40})\.(status\.json|log|err\.log|request\.json)$') {
            $key = $Matches[1]
            if (-not $byId.ContainsKey($key)) { $byId[$key] = @() }
            $byId[$key] += $f
        }
    }
    $removed = 0
    foreach ($id in @($byId.Keys)) {
        $files = $byId[$id]
        if (@($files | Where-Object { $_.LastWriteTime -ge $cutoff }).Count -gt 0) { continue }
        $statusFile = @($files | Where-Object { $_.Name -ceq "$id.status.json" })
        if ($statusFile.Count -gt 0) {
            try { if ((Get-Content -LiteralPath $statusFile[0].FullName -Raw | ConvertFrom-Json).state -eq 'running') { continue } } catch { continue }
        }
        foreach ($f in $files) { Remove-Item -LiteralPath $f.FullName -Force -ErrorAction SilentlyContinue }
        $removed++
    }
    $dayAgo = (Get-Date).AddDays(-1)
    # $cancelDir is written by the agent and this runs elevated: first level only, never through a link, and never a name
    # that ends in a dot or a space (Win32 turns it into another path, for instance '...' into the folder itself).
    # $tmp is writable by administrators only, so it keeps the recursive clean-up.
    $stale = @(Get-ChildItem -LiteralPath $cancelDir -File -ErrorAction SilentlyContinue |
        Where-Object { -not ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -and $_.Name -notmatch '[. ]$' -and $_.LastWriteTime -lt $dayAgo })
    $stale += @(Get-ChildItem -LiteralPath $tmp -File -Recurse -ErrorAction SilentlyContinue | Where-Object { $_.LastWriteTime -lt $dayAgo })
    # A name the agent chose (trailing dot or space, only dots) must not stop the runner: log it and go on.
    foreach ($f in $stale) {
        try { Remove-Item -LiteralPath $f.FullName -Force -Confirm:$false -ErrorAction Stop } catch { Write-Log "could not remove a stale file: $($_.Exception.Message)" }
    }
    if ($removed -gt 0) { Write-Log "cleanup: removed the files of $removed job(s) older than $KeepDays days" }
}

$mutex = New-Object System.Threading.Mutex($false, 'Global\WinBridgeJobsRunner')
if (-not $Once -and -not $mutex.WaitOne(0)) { exit 0 }  # another runner is already serving this folder
Write-Log 'runner started'
Repair-Interrupted
try { Remove-OldFiles } catch { Write-Log "cleanup failed: $($_.Exception.Message)" }
$lastCleanup = Get-Date
try {
    do {
        if (((Get-Date) - $lastCleanup).TotalHours -ge 6) { try { Remove-OldFiles } catch { Write-Log "cleanup failed: $($_.Exception.Message)" }; $lastCleanup = Get-Date }
        $ready = @(Get-ChildItem $inbox -Filter '*.json' -File -ErrorAction SilentlyContinue |
            Where-Object { -not ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) } | Sort-Object LastWriteTime, Name)
        foreach ($f in $ready) {
            # One bad request (a locked file, a full disk) must not stop the runner that serves the next ones.
            try { Invoke-Job $f } catch { Write-Log "error on $($f.Name): $($_.Exception.Message)" }
        }
        if (-not $Once -and $ready.Count -eq 0) { Start-Sleep -Seconds 1 }
    } while (-not $Once)
} finally {
    Write-Log 'runner stopped'
}
