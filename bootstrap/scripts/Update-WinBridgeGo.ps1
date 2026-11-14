# Updates a Go-only WinBridge install from a reviewed bundle ZIP: the agent executable, the scripts, the libraries and
# the config template. It runs as the owner and needs no elevation: everything it touches is under %LOCALAPPDATA%\winbridge.
# On any failure it puts the previous executable back.

param(
    [Parameter(Mandatory = $true)][string]$BundlePath,
    [Parameter(Mandatory = $true)][string]$Sha256,
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

$goDir = Get-WinBridgePath 'app\go'
$goExe = Join-Path $goDir 'winbridge-agent.exe'
$backup = $goExe + '.previous'
$work = Join-Path (Get-WinBridgePath 'updates') ([guid]::NewGuid().ToString('N'))
$swapped = $false
$maintenanceFlag = Get-WinBridgePath 'state\maintenance.flag'
$supervisorScript = Join-Path $binRoot 'supervisor.ps1'
$result = [pscustomobject]@{
    ok = $false
    version = $null
    build_id = $null
    exe_sha256 = $null
    previous_exe_sha256 = $null
    runner_sha256 = $null
    rolled_back = $false
    error = $null
}

# Copies one file, retrying while Windows still holds the old process image.
function Copy-WinBridgeFileWithRetry {
    param([string]$Source, [string]$Destination)
    for ($attempt = 1; ; $attempt++) {
        try {
            Copy-Item -LiteralPath $Source -Destination $Destination -Force -ErrorAction Stop
            return
        } catch {
            if ($attempt -ge 20) { throw }
            Start-Sleep -Milliseconds 500
        }
    }
}

# Asks the supervisor to stop the agent and hold off restarting it, and waits until the agent process is gone.
function Enter-WinBridgeMaintenance {
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $maintenanceFlag)
    [IO.File]::WriteAllText($maintenanceFlag, (Get-Date).ToString('o'), (New-Object System.Text.UTF8Encoding($false)))
    $pidPath = Get-WinBridgePath 'state\server.pid'
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline -and $null -ne (Get-WinBridgeProcessFromPidFile -Path $pidPath)) { Start-Sleep -Milliseconds 500 }
    Stop-WinBridgeStaleServer | Out-Null
}

# Lets the supervisor start the agent again; starts the supervisor if it is not running.
function Exit-WinBridgeMaintenance {
    Remove-Item -LiteralPath $maintenanceFlag -Force -ErrorAction SilentlyContinue
    Start-WinBridgeTask | Out-Null
}

try {
    $zip = (Resolve-Path -LiteralPath $BundlePath -ErrorAction Stop).Path
    if ((Get-WinBridgeFileSha256 -Path $zip) -ne $Sha256.ToLowerInvariant()) {
        throw 'The bundle ZIP does not match the expected SHA-256.'
    }
    if (-not (Test-Path -LiteralPath $goExe)) {
        throw 'This is not a Go install (no winbridge-agent.exe); use the installer.'
    }
    Ensure-WinBridgeDirectory -Path $work
    Expand-Archive -LiteralPath $zip -DestinationPath $work -Force
    $manifest = Test-WinBridgeBundleManifest -Root $work
    if ($null -eq $manifest.PSObject.Properties['agent_exe']) {
        throw 'This is not a Go bundle (the manifest has no agent_exe).'
    }
    $newExe = Join-Path $work (([string]$manifest.agent_exe.path).Replace('/', '\'))
    $newSha = Get-WinBridgeFileSha256 -Path $newExe
    if ($newSha -ne ([string]$manifest.agent_exe.sha256).ToLowerInvariant()) {
        throw 'The agent in the bundle does not match its recorded SHA-256.'
    }
    $result.version = [string]$manifest.version
    $result.build_id = [string]$manifest.build_id
    $result.exe_sha256 = $newSha
    $result.previous_exe_sha256 = Get-WinBridgeFileSha256 -Path $goExe
    # Hash of the job runner as verified in the bundle: pass it to Install-WinBridgeJobs.ps1 -RunnerSha256.
    $runnerEntry = @($manifest.files | Where-Object { [string]$_.path -eq 'bootstrap/jobs/runner.ps1' })
    if ($runnerEntry.Count -gt 0) { $result.runner_sha256 = ([string]$runnerEntry[0].sha256).ToLowerInvariant() }

    $supervisorBefore = Get-WinBridgeFileSha256 -Path $supervisorScript
    Enter-WinBridgeMaintenance
    Copy-Item -LiteralPath $goExe -Destination $backup -Force
    Copy-WinBridgeFileWithRetry -Source $newExe -Destination $goExe
    $swapped = $true
    if ((Get-WinBridgeFileSha256 -Path $goExe) -ne $newSha) { throw 'The installed agent does not match the bundle.' }

    Copy-Item -Path (Join-Path $work 'bootstrap\scripts\*') -Destination $binRoot -Force
    Copy-Item -Path (Join-Path $work 'bootstrap\lib\*') -Destination (Join-Path $binRoot 'lib') -Force
    $jobsSource = Join-Path $work 'bootstrap\jobs'
    if (Test-Path -LiteralPath $jobsSource) {
        Copy-Item -LiteralPath $jobsSource -Destination $binRoot -Recurse -Force
    }
    $templateDir = Join-Path $binRoot 'templates'
    Ensure-WinBridgeDirectory -Path $templateDir
    Copy-Item -LiteralPath (Join-Path $work 'bootstrap\config\winbridge.toml.template') -Destination $templateDir -Force

    $state = Get-WinBridgeState
    if ($null -ne $state) {
        Set-WinBridgeProperty -Object $state -Name 'installed_version' -Value $result.version
        Set-WinBridgeProperty -Object $state -Name 'bundle_id' -Value $result.build_id
        Set-WinBridgeProperty -Object $state -Name 'installed_at' -Value ((Get-Date).ToString('o'))
        Save-WinBridgeState -State $state
        Write-WinBridgeConfig -State $state | Out-Null
    }

    if ((Get-WinBridgeFileSha256 -Path $supervisorScript) -ne $supervisorBefore) {
        # A new supervisor script only takes effect in a new supervisor process.
        Stop-WinBridgeTask | Out-Null
    }
    Exit-WinBridgeMaintenance
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) { throw 'Tailscale is not connected.' }
    $uri = 'http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + (Get-WinBridgePort) + '/health'
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $health = $null
    do {
        $health = Wait-WinBridgeHttp -Uri $uri -TimeoutSeconds $TimeoutSeconds
        if ([string]$health.upstream -eq 'native-go') { break }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    if ([string]$health.upstream -ne 'native-go') { throw 'The Go agent did not answer /health after the update.' }
    $result.ok = $true
    Write-WinBridgeInstallResult -Ok $true -Version $result.version -BuildId $result.build_id -ExeSha256 $newSha
} catch {
    $result.error = Redact-WinBridgeText $_.Exception.Message
    if ($swapped -and (Test-Path -LiteralPath $backup)) {
        try {
            Enter-WinBridgeMaintenance
            Copy-WinBridgeFileWithRetry -Source $backup -Destination $goExe
            Exit-WinBridgeMaintenance
            $result.rolled_back = $true
        } catch {
            $result.error = $result.error + ' Rollback failed: ' + (Redact-WinBridgeText $_.Exception.Message)
        }
    }
    Write-WinBridgeInstallResult -Ok $false -ErrorMessage $result.error -Version $result.version -BuildId $result.build_id -ExeSha256 $result.exe_sha256
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $maintenanceFlag -Force -ErrorAction SilentlyContinue
}

if ($Json) { Write-Output ($result | ConvertTo-Json -Compress) } else { Write-Output $result }
if (-not $result.ok) { exit 1 }
