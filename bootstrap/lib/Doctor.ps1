# Read-only WinBridge health checks used by Test-WinBridgeInstall.ps1 and the
# Mac CLI. The doctor never prints the auth key.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$script:WinBridgeExpectedTools = @(
    'Snapshot',
    'Click',
    'Type',
    'Move',
    'Scroll',
    'Shortcut',
    'FocusWindow',
    'Wait',
    'GetSystemInfo'
)

function New-WinBridgeCheck {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Status,
        [string]$Detail = ''
    )
    return [pscustomobject]@{
        name = $Name
        status = $Status
        detail = $Detail
    }
}

function Add-WinBridgeResultCheck {
    param(
        [Parameter(Mandatory = $true)][AllowEmptyCollection()]$Checks,
        [Parameter(Mandatory = $true)]$Result
    )
    $statusValue = 'FAIL'
    if ($Result.Ok) {
        $statusValue = 'OK'
    } elseif ($Result.Warn) {
        $statusValue = 'WARN'
    }
    return $checks.Add((New-WinBridgeCheck -Name $Result.Name -Status $statusValue -Detail $Result.Detail))
}

function Invoke-WinBridgeDoctor {
    param([switch]$PrePairing, [switch]$SkipSsh)

    $checks = [System.Collections.Generic.List[object]]::new()
    $root = Get-WinBridgeRoot
    $configPath = Get-WinBridgeConfigPath
    $venvPython = Join-Path $root 'app\venv\Scripts\python.exe'
    $port = Get-WinBridgePort

    if (Test-Path -LiteralPath $root) {
        $checks.Add((New-WinBridgeCheck -Name 'install_root' -Status 'OK' -Detail $root))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'install_root' -Status 'FAIL' -Detail ('missing: ' + $root)))
    }

    $goExe = Join-Path $root 'app\go\winbridge-agent.exe'
    $goOnly = (-not (Test-Path -LiteralPath $venvPython)) -and (Test-Path -LiteralPath $goExe)
    if ($goOnly) {
        $checks.Add((New-WinBridgeCheck -Name 'go_agent' -Status 'OK' -Detail $goExe))
    } elseif (Test-Path -LiteralPath $venvPython) {
        $checks.Add((New-WinBridgeCheck -Name 'python_environment' -Status 'OK' -Detail $venvPython))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'python_environment' -Status 'FAIL' -Detail ('missing: ' + $venvPython)))
    }

    $lastInstallPath = Join-Path $root 'state\last-install.json'
    if (Test-Path -LiteralPath $lastInstallPath) {
        try {
            $lastInstall = Get-Content -LiteralPath $lastInstallPath -Raw | ConvertFrom-Json
            if ($lastInstall.ok) {
                $checks.Add((New-WinBridgeCheck -Name 'last_install' -Status 'OK' -Detail ('build ' + $lastInstall.build_id + ' at ' + $lastInstall.installed_at)))
            } else {
                $checks.Add((New-WinBridgeCheck -Name 'last_install' -Status 'FAIL' -Detail ('the last install failed: ' + $lastInstall.error)))
            }
        } catch {
            $checks.Add((New-WinBridgeCheck -Name 'last_install' -Status 'WARN' -Detail 'last-install.json is not readable'))
        }
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'last_install' -Status 'WARN' -Detail 'no install result recorded (installed before this record existed)'))
    }

    $version = Get-WinBridgeInstalledVersion
    if ($version) {
        $checks.Add((New-WinBridgeCheck -Name 'package_version' -Status 'OK' -Detail $version))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'package_version' -Status 'FAIL' -Detail 'the installed agent does not report a version'))
    }

    $configSummary = $null
    if ($goOnly) {
        $configSummary = Get-WinBridgeGoConfigSummary -AgentExe $goExe -ConfigPath $configPath
    } elseif (Test-Path -LiteralPath $venvPython -PathType Leaf) {
        $configSummary = Get-WinBridgeConfigSummary -VenvPython $venvPython -ConfigPath $configPath
    }
    if ($null -eq $configSummary) {
        $checks.Add((New-WinBridgeCheck -Name 'config' -Status 'FAIL' -Detail ('missing or unreadable: ' + $configPath)))
    } else {
        $tools = @($configSummary.tools)
        if ($tools.Count -eq 0) { $tools = @($configSummary.exclude) }
        $toolsOk = ($tools.Count -eq 9)
        foreach ($expected in $script:WinBridgeExpectedTools) {
            if ($tools -notcontains $expected) { $toolsOk = $false }
        }
        $configStatus = 'OK'
        $configDetail = ('host=' + $configSummary.host + ' port=' + $configSummary.port + ' tools=' + $tools.Count)
        if (-not $toolsOk) {
            $configStatus = 'FAIL'
            $configDetail = 'tool profile is not the reviewed 9-tool set'
        } elseif ($configSummary.enable_tier3) {
            $configStatus = 'FAIL'
            $configDetail = 'enable_tier3 is true'
        } elseif (-not (Test-WinBridgeTailscaleAddress -Address ([string]$configSummary.host))) {
            $configStatus = 'FAIL'
            $configDetail = ('config host is not a Tailscale address: ' + $configSummary.host)
        } else {
            $currentIp = Get-WinBridgeTailscaleIPv4
            if ($currentIp -and ([string]$configSummary.host -ne $currentIp)) {
                $configStatus = 'WARN'
                $configDetail = ('config host ' + $configSummary.host + ' differs from current Tailscale IP ' + $currentIp)
            }
        }
        $checks.Add((New-WinBridgeCheck -Name 'config' -Status $configStatus -Detail $configDetail))
    }

    $credentialUnreadable = $false
    try {
        $credential = Test-WinBridgeCredential
    } catch {
        $credential = [pscustomobject]@{ Present = $false; Fingerprint = $null; UserName = $null; Target = $null }
        $credentialUnreadable = [bool]$env:SSH_CONNECTION
        $credentialStatus = if ($env:SSH_CONNECTION) { 'SKIP' } else { 'FAIL' }
        $credentialNote = if ($env:SSH_CONNECTION) { ' (an SSH session has no Credential Manager; run the doctor on the PC to check it)' } else { '' }
        $checks.Add((New-WinBridgeCheck -Name 'credential' -Status $credentialStatus -Detail ('could not read Credential Manager: ' + $_.Exception.Message + $credentialNote)))
    }
    if ($credential.Present) {
        $checks.Add((New-WinBridgeCheck -Name 'credential' -Status 'OK' -Detail ('present; fingerprint ' + $credential.Fingerprint)))
    } elseif ($credentialUnreadable) {
        # The SKIP above already explains it; absence cannot be judged from this session.
    } elseif ($PrePairing) {
        $checks.Add((New-WinBridgeCheck -Name 'credential' -Status 'WARN' -Detail 'not paired yet (expected before adopt)'))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'credential' -Status 'FAIL' -Detail 'auth key is not in Credential Manager'))
    }

    $task = Get-WinBridgeTaskSummary
    if (-not $task.Present) {
        $checks.Add((New-WinBridgeCheck -Name 'scheduled_task' -Status 'FAIL' -Detail 'task is missing'))
    } else {
        $taskStatus = 'OK'
        if ($task.State -notin @('Ready', 'Running')) { $taskStatus = 'WARN' }
        $checks.Add((New-WinBridgeCheck -Name 'scheduled_task' -Status $taskStatus -Detail ('state=' + $task.State + ' lastResult=' + $task.LastTaskResult)))
    }

    $supervisor = Get-WinBridgeProcessFromPidFile -Path (Join-Path $root 'state\supervisor.pid')
    if ($null -eq $supervisor) {
        $checks.Add((New-WinBridgeCheck -Name 'supervisor' -Status 'WARN' -Detail 'not running (it starts at logon or after pairing)'))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'supervisor' -Status 'OK' -Detail ('pid ' + $supervisor.ProcessId)))
    }

    $server = Get-WinBridgeProcessFromPidFile -Path (Join-Path $root 'state\server.pid')
    if ($null -eq $server) {
        $serverStatus = if ($PrePairing) { 'WARN' } else { 'FAIL' }
        $checks.Add((New-WinBridgeCheck -Name 'server_process' -Status $serverStatus -Detail 'not running'))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'server_process' -Status 'OK' -Detail ('pid ' + $server.ProcessId)))
    }

    $tailscaleStatus = Get-WinBridgeTailscaleStatus
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if ($null -ne $tailscaleStatus -and $tailscaleStatus.BackendState -eq 'Running' -and $tailscaleIp) {
        $account = Get-WinBridgeTailscaleAccount
        $checks.Add((New-WinBridgeCheck -Name 'tailscale' -Status 'OK' -Detail ($tailscaleIp + ' account=' + $account)))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'tailscale' -Status 'FAIL' -Detail 'Tailscale is not running or has no IPv4 address'))
    }

    $health = $null
    if ($tailscaleIp) {
        try {
            $health = Get-WinBridgeHealth -Address $tailscaleIp -Port $port
        } catch {
            $health = $null
        }
    }
    if ($null -eq $health) {
        $healthStatus = if ($PrePairing) { 'WARN' } else { 'FAIL' }
        $checks.Add((New-WinBridgeCheck -Name 'health' -Status $healthStatus -Detail 'no /health response'))
    } else {
        $healthProblems = @()
        $isGoAgent = ([string]$health.upstream -eq 'native-go')
        if (-not $isGoAgent -and [string]$health.version -ne [string]$version) { $healthProblems += ('version=' + $health.version) }
        if ([string]$health.profile -ne 'winbridge') { $healthProblems += ('profile=' + $health.profile) }
        if (-not $isGoAgent -and [string]$health.upstream -ne '0.4.24') { $healthProblems += ('upstream=' + $health.upstream) }
        if ($healthProblems.Count -eq 0) {
            $checks.Add((New-WinBridgeCheck -Name 'health' -Status 'OK' -Detail ('version=' + $health.version + ' profile=' + $health.profile)))
        } else {
            $checks.Add((New-WinBridgeCheck -Name 'health' -Status 'FAIL' -Detail ($healthProblems -join '; ')))
        }
    }

    if ($credential.Present -and $null -ne $health) {
        try {
            $stored = Get-WinBridgeCredential
            $status = Get-WinBridgeStatus -Address $tailscaleIp -Key $stored.Secret -Port $port
            $statusChecks = @()
            if ($status.profile -ne 'winbridge' -or -not $status.strict) { $statusChecks += 'not strict' }
            if ([int]$status.tools_count -ne 9) { $statusChecks += ('tools=' + $status.tools_count) }
            if ([string]$status.version -ne [string]$version) { $statusChecks += ('version=' + $status.version) }
            if ($statusChecks.Count -eq 0) {
                $checks.Add((New-WinBridgeCheck -Name 'authenticated_status' -Status 'OK' -Detail ('profile=winbridge tools=' + $status.tools_count)))
            } else {
                $checks.Add((New-WinBridgeCheck -Name 'authenticated_status' -Status 'FAIL' -Detail ($statusChecks -join '; ')))
            }
        } catch {
            $checks.Add((New-WinBridgeCheck -Name 'authenticated_status' -Status 'FAIL' -Detail $_.Exception.Message))
        }
    } elseif ($credentialUnreadable) {
        $checks.Add((New-WinBridgeCheck -Name 'authenticated_status' -Status 'SKIP' -Detail 'skipped: no Credential Manager in an SSH session; `winbridge status` on the Mac checks it'))
    } elseif (-not $PrePairing) {
        $checks.Add((New-WinBridgeCheck -Name 'authenticated_status' -Status 'FAIL' -Detail 'no credential to authenticate'))
    }

    $listeners = @(Get-WinBridgeTcpListener -Port $port)
    if ($listeners.Count -gt 0) {
        $addresses = @($listeners | ForEach-Object { [string]$_.LocalAddress })
        $bad = @($addresses | Where-Object { $_ -in @('0.0.0.0', '::') })
        if ($bad.Count -gt 0) {
            $checks.Add((New-WinBridgeCheck -Name 'listener_address' -Status 'FAIL' -Detail ('listening on ' + ($addresses -join ', '))))
        } else {
            $checks.Add((New-WinBridgeCheck -Name 'listener_address' -Status 'OK' -Detail (($addresses -join ', ') + ':' + $port)))
        }
    } else {
        $listenerStatus = if ($PrePairing) { 'SKIP' } else { 'WARN' }
        $checks.Add((New-WinBridgeCheck -Name 'listener_address' -Status $listenerStatus -Detail 'no TCP listener reported'))
    }

    if (-not $SkipSsh) {
        foreach ($result in @(Test-WinBridgeSsh)) {
            Add-WinBridgeResultCheck -Checks $checks -Result $result | Out-Null
        }
        $sshdFailure = ''
        $sshdSettings = $null
        try {
            $sshdSettings = Get-WinBridgeSshdSettings -Failure ([ref]$sshdFailure)
        } catch {
            $sshdFailure = [string]$_.Exception.Message
        }
        $checks.Add((New-WinBridgeSshdSettingsCheck -Settings $sshdSettings -Failure $sshdFailure))
        $checks.Add((New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since (Get-Date).AddHours(-1))))
    }
    foreach ($result in @(Test-WinBridgeFirewall -SshPort 22 -McpPort $port)) {
        if ($SkipSsh -and $result.Name -match 'ssh|port_22') { continue }
        Add-WinBridgeResultCheck -Checks $checks -Result $result | Out-Null
    }

    try {
        $defender = Get-MpComputerStatus -ErrorAction Stop
        $defenderOk = [bool]$defender.AntivirusEnabled -and [bool]$defender.RealTimeProtectionEnabled
        $defenderStatus = if ($defenderOk) { 'OK' } else { 'WARN' }
        $checks.Add((New-WinBridgeCheck -Name 'defender' -Status $defenderStatus -Detail ('antivirus=' + $defender.AntivirusEnabled + ' realtime=' + $defender.RealTimeProtectionEnabled)))
    } catch {
        $checks.Add((New-WinBridgeCheck -Name 'defender' -Status 'WARN' -Detail 'could not read Defender status'))
    }

    try {
        $profiles = @(Get-NetFirewallProfile -ErrorAction Stop)
        $disabled = @($profiles | Where-Object { -not $_.Enabled })
        $profileStatus = if ($disabled.Count -eq 0) { 'OK' } else { 'FAIL' }
        $checks.Add((New-WinBridgeCheck -Name 'firewall_profiles' -Status $profileStatus -Detail (($profiles | ForEach-Object { $_.Name + '=' + $_.Enabled }) -join '; ')))
    } catch {
        $checks.Add((New-WinBridgeCheck -Name 'firewall_profiles' -Status 'WARN' -Detail 'could not read firewall profiles'))
    }

    # session_lock: the input desktop only exists in an interactive session, so from inside SSH this check cannot say anything.
    if ($env:SSH_CONNECTION) {
        $checks.Add((New-WinBridgeCheck -Name 'session_lock' -Status 'SKIP' -Detail 'skipped: an SSH session has no desktop; call GetSystemInfo (it reports locked or unlocked) or run the doctor on the PC'))
    } elseif (Test-WinBridgeSessionUnlocked) {
        $checks.Add((New-WinBridgeCheck -Name 'session_lock' -Status 'OK' -Detail 'interactive desktop is available'))
    } else {
        $checks.Add((New-WinBridgeCheck -Name 'session_lock' -Status 'WARN' -Detail 'the Windows session is locked or has no interactive desktop; Snapshot/Click need it unlocked'))
    }

    $oldTask = Get-ScheduledTask -TaskName 'WinRemoteMCP' -ErrorAction SilentlyContinue
    if ($null -ne $oldTask) {
        $checks.Add((New-WinBridgeCheck -Name 'legacy_task' -Status 'WARN' -Detail 'legacy WinRemoteMCP scheduled task is present; do not run it alongside WinBridge'))
    }

    $failed = @($checks | Where-Object { $_.status -eq 'FAIL' })
    $warned = @($checks | Where-Object { $_.status -eq 'WARN' })
    return [pscustomobject]@{
        ok = ($failed.Count -eq 0)
        generated_at = (Get-Date).ToString('o')
        host = $env:COMPUTERNAME
        user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        pre_pairing = [bool]$PrePairing
        failed = $failed.Count
        warnings = $warned.Count
        checks = @($checks)
    }
}

function Write-WinBridgeDoctorReport {
    param(
        [Parameter(Mandatory = $true)]$Result,
        [switch]$Quiet
    )
    if ($Quiet) { return }
    Write-Host ''
    Write-Host ('WinBridge doctor - ' + $Result.host + ' - ' + $Result.user)
    Write-Host ('Generated: ' + $Result.generated_at)
    Write-Host ''
    foreach ($check in $Result.checks) {
        $label = '[' + $check.status + ']'
        $color = 'Gray'
        if ($check.status -eq 'OK') { $color = 'Green' }
        if ($check.status -eq 'WARN') { $color = 'Yellow' }
        if ($check.status -eq 'FAIL') { $color = 'Red' }
        Write-Host ($label.PadRight(8) + $check.name.PadRight(30) + $check.detail) -ForegroundColor $color
    }
    Write-Host ''
    Write-Host ('Failed: ' + $Result.failed + '  Warnings: ' + $Result.warnings)
}
