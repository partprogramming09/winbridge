<#
.SYNOPSIS
    Installs or updates the WinBridge job runner folder and its scheduled task. Needs an elevated session.
.DESCRIPTION
    Creates the protected folder (SYSTEM and Administrators full control, Users read; the agent account may write only
    inbox\ and cancel\), copies runner.ps1 and the job packages, and writes manifest.json with the SHA-256 of every job
    script. Without -Update it also registers the task WinBridgeJobs-Run (interactive, highest privileges, starts at
    logon, restarts on failure) and starts it. Permissions are set by SID so they work on any Windows language.
    A package is a folder under -Packages with manifest.json ({"jobs":{name:{script,params,timeout_s,session,description}},
    "env":{NAME:"value with {root}"}}) and a scripts\ folder. To remove: Unregister-ScheduledTask WinBridgeJobs-Run and
    delete the folder.
.PARAMETER Packages
    Folder whose subfolders are job packages.
.PARAMETER AgentUser
    Account the WinBridge agent runs as; it gets write access to inbox\ and cancel\ only.
.PARAMETER RunnerSha256
    SHA-256 of runner.ps1 as recorded in the verified bundle (manifest.json, file bootstrap/jobs/runner.ps1; also printed
    as runner_sha256 by the update). The runner runs elevated and this script sits in a folder the agent account can
    write, so the runner is read once, compared with this value and only then written to the protected folder.
.PARAMETER Update
    Copy files and rebuild the manifest without touching the task or its permissions.
#>
param(
    [Parameter(Mandatory = $true)][string]$Packages,
    [string]$Root = 'C:\ProgramData\WinBridgeJobs',
    [string]$AgentUser = $env:USERNAME,
    [Parameter(Mandatory = $true)][ValidatePattern('^[A-Fa-f0-9]{64}$')][string]$RunnerSha256,
    [switch]$Update
)

$ErrorActionPreference = 'Stop'

# Read the runner once and hash those same bytes: a file swapped after the check cannot be the one that gets installed.
$runnerBytes = [IO.File]::ReadAllBytes((Join-Path $PSScriptRoot 'runner.ps1'))
$runnerActual = ([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($runnerBytes))).Replace('-', '')
if ($runnerActual -ne $RunnerSha256.ToUpperInvariant()) { throw 'runner hash mismatch: runner.ps1 is not the one in the verified bundle; nothing was installed.' }

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { throw 'Needs an administrator.' }

# Runs icacls and fails loudly: a silent permission error would leave the folder writable.
function Invoke-Icacls {
    & icacls @args | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ('icacls failed: ' + ($args -join ' ')) }
}

# A folder another user created first would keep its owner and permissions, so a fresh install refuses an existing one.
if (-not $Update -and (Test-Path -LiteralPath $Root) -and @(Get-ChildItem -LiteralPath $Root -Force).Count -gt 0) {
    throw "$Root already exists and is not empty. Remove it first, or use -Update to refresh an install you made."
}
foreach ($d in 'inbox', 'out', 'cancel', 'scripts', 'tmp') { New-Item -ItemType Directory -Force -Path (Join-Path $Root $d) | Out-Null }

if ($Update) {
    $owner = (Get-Acl -LiteralPath $Root).GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -notin 'S-1-5-32-544', 'S-1-5-18') { throw "$Root is owned by $owner, not by Administrators or SYSTEM; reinstall it." }
}

if (-not $Update) {
    $agentSid = (New-Object Security.Principal.NTAccount($AgentUser)).Translate([Security.Principal.SecurityIdentifier]).Value
    Invoke-Icacls $Root '/setowner' '*S-1-5-32-544' '/T'
    Invoke-Icacls $Root '/inheritance:r'
    # The agent account reads manifest and results; other local users get nothing.
    Invoke-Icacls $Root '/grant:r' '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${agentSid}:(OI)(CI)RX"
    foreach ($d in 'inbox', 'cancel') { Invoke-Icacls (Join-Path $Root $d) '/grant' "*${agentSid}:(OI)(CI)M" }
}

[IO.File]::WriteAllBytes((Join-Path $Root 'runner.ps1'), $runnerBytes)

$manifestJobs = [ordered]@{}
$manifestEnv = [ordered]@{}
$manifestApps = [ordered]@{}
$scriptOwner = @{}
$packageDirs = @(Get-ChildItem -LiteralPath $Packages -Directory -ErrorAction Stop | Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'manifest.json') })
if ($packageDirs.Count -eq 0) { throw "$Packages holds no package (a folder with manifest.json); -Packages is the folder that contains the packages" }
foreach ($pkg in $packageDirs) {
    $pm = Get-Content (Join-Path $pkg.FullName 'manifest.json') -Raw | ConvertFrom-Json
    foreach ($f in @(Get-ChildItem (Join-Path $pkg.FullName 'scripts') -File -ErrorAction SilentlyContinue)) {
        if ($scriptOwner.ContainsKey($f.Name)) { throw "script $($f.Name) is in both $($scriptOwner[$f.Name]) and $($pkg.Name)" }
        $scriptOwner[$f.Name] = $pkg.Name
        Copy-Item $f.FullName (Join-Path $Root "scripts\$($f.Name)") -Force
    }
    foreach ($prop in @($pm.jobs.PSObject.Properties)) {
        if ($manifestJobs.Contains($prop.Name)) { throw "job $($prop.Name) is defined twice" }
        $j = $prop.Value
        $path = Join-Path $Root "scripts\$($j.script)"
        if (-not (Test-Path -LiteralPath $path)) { throw "job $($prop.Name): script $($j.script) is not in the package" }
        $params = [ordered]@{}
        if ($j.params) { foreach ($q in @($j.params.PSObject.Properties)) { $params[$q.Name] = $q.Value } }
        $manifestJobs[$prop.Name] = [ordered]@{
            script = $j.script; sha256 = (Get-FileHash $path -Algorithm SHA256).Hash; params = $params
            timeout_s = [int]$j.timeout_s; session = $(if ($j.session -ne $null) { [int]$j.session } else { 1 })
            description = [string]$j.description; package = $pkg.Name
        }
    }
    if ($pm.env) { foreach ($e in @($pm.env.PSObject.Properties)) { $manifestEnv[$e.Name] = ([string]$e.Value).Replace('{root}', $Root) } }
    if ($pm.apps) {
        foreach ($prop in @($pm.apps.PSObject.Properties)) {
            if ($manifestApps.Contains($prop.Name)) { throw "app $($prop.Name) is defined twice" }
            if ($prop.Name -cnotmatch '\A[A-Za-z0-9_-]{1,40}\z') { throw "invalid app name: $($prop.Name)" }
            if (@($manifestApps.Keys | Where-Object { $_ -ieq $prop.Name }).Count -gt 0) { throw "app $($prop.Name) differs from another app only by case" }
            $a = $prop.Value
            $appPath = ([string]$a.path).Replace('{root}', $Root)
            if ($appPath -notmatch '^[A-Za-z]:\\') { throw "app $($prop.Name): the path must start with a drive letter (no UNC paths)" }
            $appPath = [IO.Path]::GetFullPath($appPath)
            # An executable the agent account could replace would run elevated: outside trusted folders it must be pinned by hash.
            $trusted = @($env:SystemRoot, $env:ProgramFiles, ${env:ProgramFiles(x86)}, $Root) | Where-Object { $_ }
            $inTrusted = @($trusted | Where-Object { $appPath.StartsWith($_.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) }).Count -gt 0
            if (-not $inTrusted -and -not $a.sha256) { throw "app $($prop.Name): outside Windows, Program Files and the jobs folder it needs a sha256" }
            if ([string]$a.args -match '\.[*+]|\\\\|//') { throw "app $($prop.Name): the args regex must list what is allowed (no .* or .+, no path or URL starts)" }
            $runAs = if ($a.run_as) { [string]$a.run_as } else { 'user' }
            if ($runAs -cnotmatch '\A(user|admin)\z') { throw "app $($prop.Name): run_as is user (default, not elevated) or admin" }
            $manifestApps[$prop.Name] = [ordered]@{
                path = $appPath; sha256 = [string]$a.sha256; args = [string]$a.args; run_as = $runAs
                window_title = [string]$a.window_title; description = [string]$a.description; package = $pkg.Name
            }
        }
    }
}
$manifestJson = [ordered]@{ installed = (Get-Date -Format o); jobs = $manifestJobs; apps = $manifestApps; env = $manifestEnv } | ConvertTo-Json -Depth 6
[IO.File]::WriteAllText((Join-Path $Root 'manifest.json'), $manifestJson, (New-Object Text.UTF8Encoding $false))

if (-not $Update) {
    $runner = Join-Path $Root 'runner.ps1'
    $principal = New-ScheduledTaskPrincipal -UserId $AgentUser -RunLevel Highest -LogonType Interactive
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "{0}" -Root "{1}"' -f $runner, $Root)
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $AgentUser
    Register-ScheduledTask -TaskName 'WinBridgeJobs-Run' -Action $action -Principal $principal -Settings $settings -Trigger $trigger -Force | Out-Null
    Start-ScheduledTask -TaskName 'WinBridgeJobs-Run'
}

[pscustomobject]@{
    root = $Root; jobs = @($manifestJobs.Keys); update = [bool]$Update
    runner_sha256 = (Get-FileHash (Join-Path $Root 'runner.ps1') -Algorithm SHA256).Hash
}
