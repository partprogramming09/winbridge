# OpenSSH Server setup for WinBridge.
# SSH is the administrative path and is restricted to Tailscale by firewall.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgeSshdExe {
    $path = Join-Path $env:SystemRoot 'System32\OpenSSH\sshd.exe'
    if (Test-Path -LiteralPath $path) { return $path }
    return $null
}

function Install-WinBridgeOpenSshServer {
    Assert-WinBridgeAdmin
    $sshd = Get-WinBridgeSshdExe
    if (-not $sshd) {
        Write-WinBridgeStep 'Installing the OpenSSH Server capability.'
        $capability = Get-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0'
        if ($null -eq $capability -or $capability.State -ne 'Installed') {
            Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0' | Out-Null
        }
    }
    $sshd = Get-WinBridgeSshdExe
    if (-not $sshd) {
        $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
        if ($winget) {
            & $winget.Source install --id Microsoft.OpenSSH.Server -e --silent --accept-package-agreements --accept-source-agreements --disable-interactivity 2>&1 |
                ForEach-Object { Write-WinBridgeLog -Message ([string]$_) -Name 'bootstrap' -Level 'INFO' }
        }
        $sshd = Get-WinBridgeSshdExe
    }
    if (-not $sshd) {
        throw 'OpenSSH Server could not be installed.'
    }
    Set-Service -Name sshd -StartupType Automatic
    if ((Get-Service -Name sshd).Status -ne 'Running') {
        Start-Service -Name sshd
    }
    Write-WinBridgeStep 'OpenSSH Server is running.'
}

function Set-WinBridgeSshdConfig {
    Assert-WinBridgeAdmin
    $sshDir = Join-Path $env:ProgramData 'ssh'
    Ensure-WinBridgeDirectory -Path $sshDir
    $configPath = Join-Path $sshDir 'sshd_config'
    $includePath = Join-Path $sshDir 'winbridge_sshd.conf'
    $originalPath = Join-Path $sshDir 'sshd_config.winbridge-original'
    if (-not (Test-Path -LiteralPath $configPath)) {
        throw 'sshd_config was not found after installing OpenSSH Server.'
    }
    if (-not (Test-Path -LiteralPath $originalPath)) {
        Copy-Item -LiteralPath $configPath -Destination $originalPath -Force
    }

    $includeContent = @'
# Managed by WinBridge. Do not edit by hand.
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PermitUserEnvironment no
AllowAgentForwarding no
AllowTcpForwarding no
GatewayPorts no
PermitTunnel no
X11Forwarding no
StrictModes yes
MaxAuthTries 3
LoginGraceTime 30
ClientAliveInterval 60
ClientAliveCountMax 3
'@
    $encoding = New-Object System.Text.UTF8Encoding($false)

    # The managed block goes first because sshd uses the first obtained value. It lives inside sshd_config
    # (not an Include file) because OpenSSH for Windows did not apply the directives of an included file.
    $managed = @('# BEGIN WINBRIDGE') + @($includeContent -split "`r?`n" | Where-Object { $_ -and -not $_.StartsWith('#') }) + @('# END WINBRIDGE')
    $kept = New-Object System.Collections.Generic.List[string]
    $inBlock = $false
    foreach ($line in @(Get-Content -LiteralPath $configPath)) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '# BEGIN WINBRIDGE') { $inBlock = $true; continue }
        if ($trimmed -eq '# END WINBRIDGE') { $inBlock = $false; continue }
        if ($inBlock) { continue }
        if ($trimmed -like 'Include*winbridge_sshd.conf') { continue }
        $kept.Add($line)
    }
    [IO.File]::WriteAllLines($configPath, (@($managed) + @($kept)), $encoding)
    if (Test-Path -LiteralPath $includePath) { Remove-Item -LiteralPath $includePath -Force }

    $sshd = Get-WinBridgeSshdExe
    $validation = @(& $sshd -t 2>&1)
    $validationExit = $LASTEXITCODE
    foreach ($line in $validation) {
        Write-WinBridgeLog -Message ([string]$line) -Name 'bootstrap' -Level 'INFO'
    }
    if ($validationExit -ne 0) {
        Copy-Item -LiteralPath $originalPath -Destination $configPath -Force
        throw 'sshd_config validation failed; the original configuration was restored.'
    }
    Restart-Service -Name sshd
    Write-WinBridgeStep 'OpenSSH is configured for key-only authentication.'
}

function Get-WinBridgeAuthorizedKeysPath {
    if (Test-WinBridgeUserIsAdmin) {
        return (Join-Path $env:ProgramData 'ssh\administrators_authorized_keys')
    }
    return (Join-Path $env:USERPROFILE '.ssh\authorized_keys')
}

function Test-WinBridgePublicKeyLine {
    param([Parameter(Mandatory = $true)][string]$Line)
    $pattern = '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|sk-ssh-ed25519@openssh.com|sk-ecdsa-sha2-nistp256@openssh.com)\s+[A-Za-z0-9+/=]+(\s+.*)?$'
    return ($Line -match $pattern)
}

function Get-WinBridgePublicKeyFingerprint {
    param([Parameter(Mandatory = $true)][string]$Line)
    $parts = $Line.Trim() -split '\s+'
    if ($parts.Count -lt 2) { return $null }
    try {
        $blob = [Convert]::FromBase64String($parts[1])
        $sha = [Security.Cryptography.SHA256]::Create()
        try {
            $hash = $sha.ComputeHash($blob)
            # Match `ssh-keygen -lf`: SHA256:<base64 without padding>.
            return ('SHA256:' + [Convert]::ToBase64String($hash).TrimEnd('='))
        } finally {
            $sha.Dispose()
        }
    } catch {
        return $null
    }
}

function Install-WinBridgeAuthorizedKey {
    param(
        [string]$PublicKey,
        [string]$SourcePath
    )
    Assert-WinBridgeAdmin
    $lines = @()
    if ($SourcePath) {
        if (-not (Test-Path -LiteralPath $SourcePath)) {
            throw ('SSH public key file not found: ' + $SourcePath)
        }
        $lines = @(Get-Content -LiteralPath $SourcePath | Where-Object { $_.Trim() -and -not $_.TrimStart().StartsWith('#') })
    } elseif ($PublicKey) {
        $lines = @($PublicKey.Trim() -split "`r?`n" | Where-Object { $_.Trim() })
    }
    if ($lines.Count -eq 0) {
        throw 'No SSH public key was provided.'
    }
    foreach ($line in $lines) {
        if ($line -match 'PRIVATE KEY') {
            throw 'Refusing to install a private key. Provide the Mac public key only.'
        }
        if (-not (Test-WinBridgePublicKeyLine -Line $line.Trim())) {
            throw 'The SSH public key is not a supported OpenSSH public key line.'
        }
    }

    $path = Get-WinBridgeAuthorizedKeysPath
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $path)
    $existing = @()
    if (Test-Path -LiteralPath $path) {
        $existing = @(Get-Content -LiteralPath $path)
    }
    $added = 0
    $encoding = New-Object System.Text.UTF8Encoding($false)
    foreach ($line in $lines) {
        $trimmed = $line.Trim()
        $present = $false
        foreach ($old in $existing) {
            if ($old.Trim() -eq $trimmed) { $present = $true }
        }
        if (-not $present) {
            [IO.File]::AppendAllText($path, $trimmed + "`r`n", $encoding)
            $existing += $trimmed
            $added++
        }
    }

    $adminSid = '*S-1-5-32-544'
    $systemSid = '*S-1-5-18'
    if (Test-WinBridgeUserIsAdmin) {
        & icacls.exe $path /inheritance:r /grant:r ($adminSid + ':F') ($systemSid + ':F') | Out-Null
    } else {
        $userSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        & icacls.exe $path /inheritance:r /grant:r ('*' + $userSid + ':F') ($systemSid + ':F') | Out-Null
    }
    if ($LASTEXITCODE -ne 0) {
        throw 'Could not set the authorized_keys ACL.'
    }

    $fingerprint = Get-WinBridgePublicKeyFingerprint -Line $lines[0].Trim()
    Write-WinBridgeStep ('SSH public key installed in ' + $path + ' (added ' + $added + ').')
    return [pscustomobject]@{
        Path = $path
        Added = $added
        Fingerprint = $fingerprint
    }
}

function ConvertFrom-WinBridgeSshdOutput {
    param([AllowEmptyCollection()][string[]]$Lines = @())
    $settings = @{}
    foreach ($line in $Lines) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2) {
            $settings[$parts[0].ToLowerInvariant()] = $parts[1]
        }
    }
    return $settings
}

# Returns the effective sshd settings, or $null; the first line of the reason goes to $Failure when given.
function Get-WinBridgeSshdSettings {
    param([ref]$Failure)
    $sshd = Get-WinBridgeSshdExe
    if (-not $sshd) {
        if ($Failure) { $Failure.Value = 'sshd.exe was not found' }
        return $null
    }
    # Windows PowerShell 5.1 turns native stderr into a terminating error while the preference is Stop.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $effective = @(& $sshd -T 2>&1)
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($LASTEXITCODE -ne 0) {
        if ($Failure) {
            $first = @($effective | ForEach-Object { [string]$_ } | Where-Object { $_.Trim() } | Select-Object -First 1)
            $Failure.Value = if ($first.Count -gt 0) { $first[0].Trim() } else { 'sshd -T exited with code ' + $LASTEXITCODE }
        }
        return $null
    }
    return ConvertFrom-WinBridgeSshdOutput -Lines @($effective | Where-Object { $_ -is [string] })
}

function New-WinBridgeSshdSettingsCheck {
    param($Settings, [string]$Failure = '')
    if ($null -eq $Settings) {
        $cause = if ($Failure) { ': ' + $Failure } else { '' }
        return New-WinBridgeCheck -Name 'sshd_settings' -Status 'SKIP' -Detail ('could not read the effective sshd config' + $cause)
    }
    $parts = foreach ($key in @('maxstartups', 'maxauthtries', 'logingracetime')) {
        $value = if ($Settings.ContainsKey($key)) { [string]$Settings[$key] } else { 'not reported' }
        $key + '=' + $value
    }
    return New-WinBridgeCheck -Name 'sshd_settings' -Status 'OK' -Detail ($parts -join ' ')
}

# One refused attempt can show up in both counts: they are reported apart, not added.
function Measure-WinBridgeSshdFailures {
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$Messages)
    return [pscustomobject]@{
        # -cmatch: 'Connection closed by invalid user' (lowercase) is not an 'Invalid user' line.
        # The prefix is not pinned to 'sshd:': newer OpenSSH logs from 'sshd-session'.
        PreauthClosed = @($Messages | Where-Object { $_ -cmatch '\[preauth\]' }).Count
        InvalidUsers = @($Messages | Where-Object { $_ -cmatch '(^|: )Invalid user ' }).Count
    }
}

# Reads the newest $MaxEvents of the OpenSSH log since $Since (a brute-force burst can be huge, and reading
# each message is slow). No events is a count of zero; any other error is returned as Failure.
function Get-WinBridgeSshdFailureCounts {
    param(
        [Parameter(Mandatory = $true)][datetime]$Since,
        [int]$MaxEvents = 5000,
        [scriptblock]$Reader = {
            param($since, $max)
            @(Get-WinEvent -FilterHashtable @{ LogName = 'OpenSSH/Operational'; StartTime = $since } -MaxEvents $max -ErrorAction Stop |
                ForEach-Object { [string]$_.Message })
        }
    )
    try {
        $messages = @(& $Reader $Since $MaxEvents)
        return [pscustomobject]@{
            Counts = (Measure-WinBridgeSshdFailures -Messages $messages)
            Failure = ''
            Capped = ($messages.Count -ge $MaxEvents)
            MaxEvents = $MaxEvents
        }
    } catch {
        if ([string]$_.FullyQualifiedErrorId -like 'NoMatchingEventsFound*') {
            return [pscustomobject]@{ Counts = (Measure-WinBridgeSshdFailures -Messages @()); Failure = ''; Capped = $false; MaxEvents = $MaxEvents }
        }
        $first = @(([string]$_.Exception.Message) -split "`r?`n" | Where-Object { $_.Trim() } | Select-Object -First 1)
        $reason = if ($first.Count -gt 0) { $first[0].Trim() } else { 'unknown error' }
        return [pscustomobject]@{ Counts = $null; Failure = $reason; Capped = $false; MaxEvents = $MaxEvents }
    }
}

function New-WinBridgeSshdFailuresCheck {
    param([Parameter(Mandatory = $true)]$Result)
    if ($null -eq $Result.Counts) {
        return New-WinBridgeCheck -Name 'sshd_failures' -Status 'SKIP' -Detail ('could not read the OpenSSH/Operational log: ' + $Result.Failure)
    }
    $detail = 'last hour: preauth_closed=' + $Result.Counts.PreauthClosed + ' invalid_user=' + $Result.Counts.InvalidUsers
    if ($Result.Capped) { $detail += ' (capped at ' + $Result.MaxEvents + ' events; the real count is higher)' }
    return New-WinBridgeCheck -Name 'sshd_failures' -Status 'OK' -Detail $detail
}

function Test-WinBridgeSsh {
    $checks = @()
    $service = Get-Service -Name sshd -ErrorAction SilentlyContinue
    $checks += [pscustomobject]@{
        Name = 'ssh_service'
        Ok = ($null -ne $service -and $service.Status -eq 'Running')
        Warn = $false
        Detail = if ($null -eq $service) { 'sshd service is missing' } else { [string]$service.Status }
    }

    $settings = $null
    try {
        $settings = Get-WinBridgeSshdSettings
    } catch {
        $settings = $null
    }
    if ($null -eq $settings) {
        $checks += [pscustomobject]@{
            Name = 'sshd_config'
            Ok = $false
            Warn = $true
            Detail = 'could not read effective sshd config in this session'
        }
    } else {
        $checks += [pscustomobject]@{
            Name = 'sshd_password_auth'
            Ok = ($settings['passwordauthentication'] -eq 'no')
            Warn = $false
            Detail = ('passwordauthentication=' + $settings['passwordauthentication'])
        }
        $checks += [pscustomobject]@{
            Name = 'sshd_pubkey_auth'
            Ok = ($settings['pubkeyauthentication'] -eq 'yes')
            Warn = $false
            Detail = ('pubkeyauthentication=' + $settings['pubkeyauthentication'])
        }
        $checks += [pscustomobject]@{
            Name = 'sshd_tcp_forwarding'
            Ok = ($settings['allowtcpforwarding'] -eq 'no')
            Warn = $false
            Detail = ('allowtcpforwarding=' + $settings['allowtcpforwarding'])
        }
    }

    $sshdConfigPath = Join-Path $env:ProgramData 'ssh\sshd_config'
    $hasBlock = (Test-Path -LiteralPath $sshdConfigPath) -and
        (@(Get-Content -LiteralPath $sshdConfigPath | Where-Object { $_.Trim() -eq '# BEGIN WINBRIDGE' }).Count -eq 1)
    $checks += [pscustomobject]@{
        Name = 'sshd_winbridge_block'
        Ok = $hasBlock
        Warn = $false
        Detail = $sshdConfigPath
    }

    $keysPath = Get-WinBridgeAuthorizedKeysPath
    $exists = Test-Path -LiteralPath $keysPath
    $checks += [pscustomobject]@{
        Name = 'ssh_authorized_keys'
        Ok = $exists
        Warn = $false
        Detail = $keysPath
    }
    if ($exists) {
        try {
            $acl = Get-Acl -LiteralPath $keysPath
            $checks += [pscustomobject]@{
                Name = 'ssh_authorized_keys_acl'
                Ok = $acl.AreAccessRulesProtected
                Warn = $false
                Detail = if ($acl.AreAccessRulesProtected) { 'inheritance disabled' } else { 'inheritance is enabled' }
            }
        } catch {
            $checks += [pscustomobject]@{
                Name = 'ssh_authorized_keys_acl'
                Ok = $false
                Warn = $true
                Detail = ('could not read ACL in this session: ' + $_.Exception.Message)
            }
        }
    }
    return $checks
}
