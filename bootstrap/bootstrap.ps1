#requires -Version 5.1
# WinBridge one-script installer for the Windows PC.
#
# This script never asks for, accepts or stores the WinBridge auth key. The
# key is generated on the Mac and stored later by Set-WinBridgeKey.ps1 over
# SSH, directly into Windows Credential Manager.

[CmdletBinding()]
param(
    [string]$PublicKeyPath,
    [string]$TailscaleAccount,
    [ValidateRange(1024, 65535)][int]$Port = 8091,
    [string[]]$ClientIp = @(),
    [switch]$NonInteractive,
    [switch]$RecreateVenv,
    [switch]$SkipSshKey,
    [switch]$SkipFirewallAudit,
    [Parameter(DontShow = $true)][switch]$SkipSsh,
    [Parameter(DontShow = $true)][switch]$NoElevate
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
    # Older hosts may not allow changing the console encoding.
}

$script:ScriptRoot = $PSScriptRoot
# Inside a function $PSBoundParameters is the function's own (empty) set, so keep the script's here.
$script:BoundParameters = $PSBoundParameters
$script:InstallInfo = @{ Version = $null; BuildId = $null; ExeSha256 = $null }
$libDir = Join-Path $PSScriptRoot 'lib'
if (-not (Test-Path -LiteralPath (Join-Path $libDir 'Common.ps1'))) {
    $libDir = Join-Path $PSScriptRoot 'bootstrap\lib'
}
if (-not (Test-Path -LiteralPath (Join-Path $libDir 'Common.ps1'))) {
    throw 'WinBridge bootstrap libraries are missing. Extract the whole bundle before running bootstrap.ps1.'
}
$assetsDir = Split-Path -Parent $libDir

. (Join-Path $libDir 'Common.ps1')
. (Join-Path $libDir 'CredentialManager.ps1')
. (Join-Path $libDir 'Tailscale.ps1')
. (Join-Path $libDir 'Firewall.ps1')
. (Join-Path $libDir 'OpenSsh.ps1')
. (Join-Path $libDir 'PythonRuntime.ps1')
. (Join-Path $libDir 'ScheduledTask.ps1')
. (Join-Path $libDir 'WinBridgeConfig.ps1')
. (Join-Path $libDir 'Doctor.ps1')

function Resolve-WinBridgeBundleRoot {
    if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'manifest.json')) {
        return $PSScriptRoot
    }
    $parent = Split-Path -Parent $PSScriptRoot
    if ($parent -and (Test-Path -LiteralPath (Join-Path $parent 'manifest.json'))) {
        return $parent
    }
    return $PSScriptRoot
}

function Restart-WinBridgeElevated {
    param([hashtable]$Bound)
    $arguments = @(
        '-NoProfile',
        '-ExecutionPolicy',
        'Bypass',
        '-File',
        (Quote-WinBridgeArg $PSCommandPath)
    )
    foreach ($name in $Bound.Keys) {
        if ($name -eq 'NoElevate') { continue }
        $value = $Bound[$name]
        if ($value -is [switch] -or $value -is [bool]) {
            if ([bool]$value) { $arguments += ('-' + $name) }
        } elseif ($value -is [array]) {
            foreach ($item in $value) {
                $arguments += @(('-' + $name), (Quote-WinBridgeArg ([string]$item)))
            }
        } else {
            $arguments += @(('-' + $name), (Quote-WinBridgeArg ([string]$value)))
        }
    }
    $arguments += '-NoElevate'
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    try {
        $process = Start-Process -FilePath $powershell -Verb RunAs -ArgumentList $arguments -Wait -PassThru
        return $process.ExitCode
    } catch {
        throw 'Elevation failed. Open an administrative PowerShell window on the PC and run bootstrap.ps1 locally.'
    }
}

function Invoke-WinBridgeBootstrap {
    $bundleRoot = Resolve-WinBridgeBundleRoot
    $manifestPath = Join-Path $bundleRoot 'manifest.json'
    if (Test-Path -LiteralPath $manifestPath) {
        $manifest = Test-WinBridgeBundleManifest -Root $bundleRoot
    } elseif ($env:WINBRIDGE_ALLOW_UNVERIFIED -eq '1') {
        Write-WinBridgeWarn 'Running an unverified development checkout; no manifest.json was found.'
        $manifest = [pscustomobject]@{
            version = '0.0.0-unverified'
            build_id = 'unverified'
            created_at = $null
        }
    } else {
        throw 'manifest.json is missing. Use the reviewed bundle produced by tools/build_bundle.py, not a partial copy.'
    }

    if (-not (Test-WinBridgeAdmin)) {
        if ($NoElevate -or $NonInteractive) {
            throw 'This installation needs an elevated Windows PowerShell session. Open PowerShell as administrator and run bootstrap.ps1 again.'
        }
        Write-WinBridgeStep 'Requesting administrator elevation (UAC).'
        # An old result must not pass for the one the elevated run is about to write.
        Remove-Item -LiteralPath (Get-WinBridgePath 'state\last-install.json') -Force -ErrorAction SilentlyContinue
        $exitCode = Restart-WinBridgeElevated -Bound $script:BoundParameters
        $recorded = Show-WinBridgeInstallResult
        if ($exitCode -eq 0 -and -not $recorded) { $exitCode = 1 }
        exit $exitCode
    }

    if ($PublicKeyPath) {
        $resolved = Resolve-Path -LiteralPath $PublicKeyPath -ErrorAction Stop
        $PublicKeyPath = $resolved.Path
    }

    $currentUser = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $interactiveUser = Get-WinBridgeInteractiveUser
    if ($interactiveUser -and $interactiveUser -ne $currentUser) {
        $message = 'This bootstrap is elevated as ' + $currentUser + ' but the interactive Windows owner is ' + $interactiveUser + '. Run it from the PC owner account with UAC approval for that same account.'
        if ($NonInteractive) { throw $message }
        Write-WinBridgeWarn $message
        $answer = Read-Host 'Type yes to continue anyway'
        if ($answer -notmatch '^(?i)yes$') {
            throw 'Aborted so the scheduled task and SSH key are not installed for the wrong account.'
        }
    }

    $root = Get-WinBridgeRoot
    Ensure-WinBridgeDirectory -Path $root
    Protect-WinBridgeRoot -Path $root
    Ensure-WinBridgeDirectory -Path (Join-Path $root 'logs')
    Rotate-WinBridgeLog -Path (Join-Path $root 'logs\bootstrap.log')

    $version = [string]$manifest.version
    $script:InstallInfo.Version = $version
    $script:InstallInfo.BuildId = [string]$manifest.build_id
    Write-WinBridgeStep ('WinBridge bootstrap ' + $version + ' starting (upstream base 0.4.24).')
    if (-not [Environment]::Is64BitOperatingSystem) {
        throw 'WinBridge requires 64-bit Windows 10 or 11.'
    }
    $os = Get-CimInstance Win32_OperatingSystem
    Write-WinBridgeStep ('Windows: ' + $os.Caption + ' build ' + $os.BuildNumber + '.')
    if ([int]$os.BuildNumber -lt 17763) {
        throw 'WinBridge requires Windows 10 version 1809 (build 17763) or later.'
    }

    # ---- Tailscale -------------------------------------------------------
    if (-not (Get-WinBridgeTailscaleExe)) {
        if ($NonInteractive) {
            throw 'Tailscale is not installed. Install Tailscale, log in with the PC owner account, and rerun the bootstrap.'
        }
        Install-WinBridgeTailscale
    }
    Write-WinBridgeStep 'Waiting for Tailscale to connect.'
    $tailscaleStatus = Wait-WinBridgeTailscaleReady -TimeoutSeconds 600
    $account = Confirm-WinBridgeTailscaleAccount -ExpectedAccount $TailscaleAccount -NonInteractive:$NonInteractive
    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) {
        throw 'Tailscale is running but no IPv4 address was found.'
    }
    Ensure-WinBridgeTailscaleStartup
    Write-WinBridgeStep ('Tailscale ready: ' + $tailscaleIp + ' (' + $account + ').')

    # ---- OpenSSH ---------------------------------------------------------
    # -SkipSsh exists for the CI install test, where OpenSSH Server cannot be installed.
    if (-not $SkipSsh) {
        Install-WinBridgeOpenSshServer
        Set-WinBridgeSshdConfig
    }
    if (-not $SkipSshKey -and -not $SkipSsh) {
        $keySource = $PublicKeyPath
        if (-not $keySource) {
            $bundledKey = Join-Path $assetsDir 'authorized_keys'
            if (Test-Path -LiteralPath $bundledKey) {
                $keySource = $bundledKey
            }
        }
        if (-not $keySource) {
            throw 'No SSH public key was found. Rebuild the bundle with --ssh-public-key-file, or pass -PublicKeyPath, or -SkipSshKey to keep the existing key.'
        }
        $keyResult = Install-WinBridgeAuthorizedKey -SourcePath $keySource
        Write-WinBridgeStep ('SSH key fingerprint: ' + $keyResult.Fingerprint + '.')
    }

    # ---- Firewall --------------------------------------------------------
    if (-not $SkipSsh) {
        Disable-WinBridgeBuiltinOpenSshRule | Out-Null
        Ensure-WinBridgeFirewallRule -Name $script:WinBridgeSshRule -Port 22
        if (-not $SkipFirewallAudit) {
            Assert-WinBridgeNoBroadPortRules -Port 22
        }
    }
    Ensure-WinBridgeFirewallRule -Name $script:WinBridgeMcpRule -Port $Port
    if (-not $SkipFirewallAudit) {
        Assert-WinBridgeNoBroadPortRules -Port $Port
    }

    # ---- Agent runtime ---------------------------------------------------
    # A bundle built with --go-agent carries winbridge-agent.exe and no Python or wheels.
    $goBundle = $null -ne $manifest.PSObject.Properties['agent_exe']
    if ($goBundle) {
        $goSource = Join-Path $bundleRoot ([string]$manifest.agent_exe.path)
        if ((Get-WinBridgeFileSha256 -Path $goSource) -ne ([string]$manifest.agent_exe.sha256).ToLowerInvariant()) {
            throw 'The Go agent in the bundle does not match its recorded SHA-256.'
        }
        $installedVersion = [string]$manifest.version
        Write-WinBridgeStep ('Go agent ' + $installedVersion + ' (no Python needed).')
    } else {
        $python = Install-WinBridgePython
        Write-WinBridgeStep ('Python ' + $python.Version + ' at ' + $python.Path + '.')
        $venvPython = New-WinBridgeVenv -PythonPath $python.Path -Recreate:$RecreateVenv
        $wheelDir = Join-Path $bundleRoot 'wheels'
        $lockFile = Join-Path $assetsDir 'requirements\windows-py312-bundle.lock.txt'
        if (-not (Test-Path -LiteralPath $lockFile)) {
            throw ('Bundle install lock not found: ' + $lockFile + '. Rebuild the bundle with tools/build_bundle.py.')
        }
        Install-WinBridgePythonPackages -VenvPython $venvPython -Wheelhouse $wheelDir -LockFile $lockFile
        $wheel = Get-WinBridgeWheelPath -Root $wheelDir
        Install-WinBridgeWheel -VenvPython $venvPython -WheelPath $wheel
        $installedVersion = Get-WinBridgePackageVersion -VenvPython $venvPython
        if ($installedVersion -ne $version) {
            throw ('Installed version ' + $installedVersion + ' does not match bundle version ' + $version + '.')
        }
    }

    # ---- State and config ------------------------------------------------
    $state = Get-WinBridgeState
    if ($null -eq $state) { $state = Get-WinBridgeDefaultState }
    Set-WinBridgeProperty -Object $state -Name 'port' -Value $Port
    Set-WinBridgeProperty -Object $state -Name 'tailscale_ip' -Value $tailscaleIp
    Set-WinBridgeProperty -Object $state -Name 'installed_version' -Value $installedVersion
    Set-WinBridgeProperty -Object $state -Name 'upstream_base' -Value '0.4.24'
    Set-WinBridgeProperty -Object $state -Name 'installed_at' -Value ((Get-Date).ToString('o'))
    Set-WinBridgeProperty -Object $state -Name 'bundle_id' -Value ([string]$manifest.build_id)
    Set-WinBridgeProperty -Object $state -Name 'bundle_created_at' -Value ([string]$manifest.created_at)
    foreach ($client in @($ClientIp)) {
        if ($client) {
            Add-WinBridgeClient -State $state -Client $client | Out-Null
        }
    }
    Save-WinBridgeState -State $state
    $templateDir = Get-WinBridgePath 'bin\templates'
    Ensure-WinBridgeDirectory -Path $templateDir
    Copy-Item -LiteralPath (Join-Path $assetsDir 'config\winbridge.toml.template') -Destination $templateDir -Force
    $configPath = Write-WinBridgeConfig -State $state
    Write-WinBridgeStep ('Configuration written to ' + $configPath + '.')

    # ---- Installed management scripts -----------------------------------
    $bin = Get-WinBridgePath 'bin'
    $binLib = Join-Path $bin 'lib'
    $binTemplates = Join-Path $bin 'templates'
    $binRequirements = Join-Path $bin 'requirements'
    Ensure-WinBridgeDirectory -Path $bin
    Ensure-WinBridgeDirectory -Path $binLib
    Ensure-WinBridgeDirectory -Path $binTemplates
    Ensure-WinBridgeDirectory -Path $binRequirements
    Copy-Item -Path (Join-Path $assetsDir 'scripts\*') -Destination $bin -Force
    Copy-Item -Path (Join-Path $libDir '*') -Destination $binLib -Force
    Copy-Item -LiteralPath (Join-Path $assetsDir 'config\winbridge.toml.template') -Destination $binTemplates -Force
    if (-not $goBundle) { Copy-Item -LiteralPath $lockFile -Destination $binRequirements -Force }
    foreach ($name in @('README.txt')) {
        $source = Join-Path $assetsDir $name
        if (Test-Path -LiteralPath $source) { Copy-Item -LiteralPath $source -Destination $bin -Force }
    }
    foreach ($name in @('NOTICE', 'LICENSE', 'upstream.json')) {
        $source = Join-Path $bundleRoot $name
        if (Test-Path -LiteralPath $source) { Copy-Item -LiteralPath $source -Destination $bin -Force }
    }
    if (-not $goBundle) {
        $packages = Get-WinBridgePath 'packages'
        Ensure-WinBridgeDirectory -Path $packages
        Copy-Item -LiteralPath $wheel -Destination $packages -Force
    }

    # ---- Supervisor task -------------------------------------------------
    Stop-WinBridgeTask | Out-Null
    Stop-WinBridgeStaleServer | Out-Null
    foreach ($name in @('stop.flag', 'maintenance.flag', 'restart.flag')) {
        $flag = Join-Path $root ('state\' + $name)
        if (Test-Path -LiteralPath $flag) {
            Remove-Item -LiteralPath $flag -Force -ErrorAction SilentlyContinue
        }
    }
    if ($goBundle) {
        $goDir = Get-WinBridgePath 'app\go'
        Ensure-WinBridgeDirectory -Path $goDir
        $goTarget = Join-Path $goDir 'winbridge-agent.exe'
        for ($attempt = 1; ; $attempt++) {
            try {
                Copy-Item -LiteralPath $goSource -Destination $goTarget -Force -ErrorAction Stop
                break
            } catch {
                if ($attempt -ge 20) { throw }
                Start-Sleep -Milliseconds 500
            }
        }
        if ((Get-WinBridgeFileSha256 -Path $goTarget) -ne ([string]$manifest.agent_exe.sha256).ToLowerInvariant()) {
            throw 'The installed Go agent does not match the bundle.'
        }
        $script:InstallInfo.ExeSha256 = (Get-WinBridgeFileSha256 -Path $goTarget)
        [IO.File]::WriteAllText((Join-Path $root 'state\agent.txt'), 'go', (New-Object System.Text.UTF8Encoding($false)))
    }
    Register-WinBridgeTask -SupervisorPath (Join-Path $bin 'supervisor.ps1')
    Start-WinBridgeTask | Out-Null

    # ---- Pre-pairing doctor ---------------------------------------------
    $doctor = Invoke-WinBridgeDoctor -PrePairing -SkipSsh:$SkipSsh
    Write-WinBridgeDoctorReport -Result $doctor
    if (-not $doctor.ok) {
        throw 'The pre-pairing doctor reported failures. Review the checks above before using the Mac client.'
    }

    Write-WinBridgeInstallResult -Ok $true -Version $script:InstallInfo.Version -BuildId $script:InstallInfo.BuildId -ExeSha256 $script:InstallInfo.ExeSha256
    Write-Host ''
    Write-Host ('WinBridge ' + $installedVersion + ' is installed and waiting for pairing.') -ForegroundColor Green
    Write-Host ('  Tailscale IP : ' + $tailscaleIp)
    Write-Host ('  SSH          : key-only, TCP 22, Tailscale sources only')
    Write-Host ('  MCP endpoint : http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $Port + '/mcp')
    Write-Host ('  Auth key     : not present yet; it will be stored in Credential Manager during adopt')
    Write-Host ''
    Write-Host 'From the Mac, the reviewed winbridge CLI will pipe the generated key over SSH to:'
    Write-Host ('  ' + (Join-Path $bin 'Set-WinBridgeKey.ps1') + ' -ClientIp <mac-tailscale-ip> -Json')
    Write-Host ''
    Write-Host ('Logs: ' + (Join-Path $root 'logs'))
    Write-Host 'No autologon is configured. Keep the Windows session unlocked while support is active.'
}

try {
    Invoke-WinBridgeBootstrap
    exit 0
} catch {
    Write-WinBridgeInstallResult -Ok $false -ErrorMessage $_.Exception.Message -Version $script:InstallInfo.Version -BuildId $script:InstallInfo.BuildId -ExeSha256 $script:InstallInfo.ExeSha256
    Write-WinBridgeError $_.Exception.Message
    Write-Host ''
    Write-Host ('WinBridge bootstrap failed: ' + $_.Exception.Message) -ForegroundColor Red
    Write-Host ('Logs: ' + (Join-Path (Get-WinBridgeRoot) 'logs'))
    exit 1
}
