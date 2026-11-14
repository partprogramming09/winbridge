# Shared WinBridge bootstrap helpers.
# This file is dot-sourced by bootstrap.ps1 and by the installed management
# scripts. It must stay compatible with Windows PowerShell 5.1.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$script:WinBridgeProduct = 'winbridge-mcp'
$script:WinBridgeTaskPath = '\'
$script:WinBridgeTaskName = 'winbridge-mcp'
$script:WinBridgeTaskFullName = 'winbridge-mcp'
$script:WinBridgeFirewallGroup = 'WinBridge'
$script:WinBridgeSshRule = 'WinBridge-SSH-Tailscale'
$script:WinBridgeMcpRule = 'WinBridge-MCP-Tailscale'
$script:WinBridgeTailscaleV4 = '100.64.0.0/10'
$script:WinBridgeTailscaleV6 = 'fd7a:115c:a1e0::/48'
$script:WinBridgeDefaultPort = 8091
$script:WinBridgeCredentialTarget = 'winbridge-mcp'
$script:WinBridgeCredentialUser = 'winbridge'

function Get-WinBridgeRoot {
    if ($env:LOCALAPPDATA) {
        return (Join-Path $env:LOCALAPPDATA 'winbridge')
    }
    return (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'winbridge')
}

function Get-WinBridgePath {
    param([Parameter(Mandatory = $true)][string]$Child)
    return (Join-Path (Get-WinBridgeRoot) $Child)
}

function Ensure-WinBridgeDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Redact-WinBridgeText {
    param([AllowNull()][string]$Text)
    if ($null -eq $Text) { return '' }
    $value = [string]$Text
    $value = [regex]::Replace($value, '(?i)(Authorization\s*:\s*Bearer\s+)[^\s]+', '$1[REDACTED]')
    $value = [regex]::Replace($value, '(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]{16,}', '$1[REDACTED]')
    $value = [regex]::Replace(
        $value,
        '(?i)((?:auth[_-]?key|api[_-]?key|token|secret|password)\s*[=:]\s*)[^\s,;]+',
        '$1[REDACTED]'
    )
    return $value
}

$script:WinBridgeQuietConsole = $false

function Set-WinBridgeConsoleQuiet {
    param([bool]$Quiet = $true)
    $script:WinBridgeQuietConsole = $Quiet
}

# Windows PowerShell writes the OEM code page into a redirected stdout (an SSH pipe), so accents in a
# Spanish host arrive broken. Send UTF-8 without a BOM; the Mac CLI reads UTF-8.
function Set-WinBridgeUtf8Output {
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    try {
        [Console]::OutputEncoding = $utf8
    } catch {
        # A host without a console handle rejects the change; the output keeps the OEM code page.
        Write-Verbose ('could not set the console encoding: ' + $_.Exception.Message)
    }
}

function Write-WinBridgeLog {
    param(
        [Parameter(Mandatory = $true)][string]$Message,
        [string]$Name = 'bootstrap',
        [ValidateSet('INFO', 'WARN', 'ERROR')][string]$Level = 'INFO'
    )
    $logDir = Get-WinBridgePath 'logs'
    Ensure-WinBridgeDirectory -Path $logDir
    $line = '{0:o} [{1}] {2}' -f (Get-Date), $Level, (Redact-WinBridgeText $Message)
    Add-Content -LiteralPath (Join-Path $logDir ($Name + '.log')) -Value $line -Encoding UTF8
}

function Write-WinBridgeStep {
    param([Parameter(Mandatory = $true)][string]$Message)
    if (-not $script:WinBridgeQuietConsole) {
        Write-Host ('[winbridge] ' + $Message)
    }
    Write-WinBridgeLog -Message $Message -Name 'bootstrap' -Level 'INFO'
}

function Write-WinBridgeWarn {
    param([Parameter(Mandatory = $true)][string]$Message)
    if (-not $script:WinBridgeQuietConsole) {
        Write-Host ('[winbridge] WARNING: ' + $Message) -ForegroundColor Yellow
    }
    Write-WinBridgeLog -Message $Message -Name 'bootstrap' -Level 'WARN'
}

function Write-WinBridgeError {
    param([Parameter(Mandatory = $true)][string]$Message)
    if (-not $script:WinBridgeQuietConsole) {
        Write-Host ('[winbridge] ERROR: ' + $Message) -ForegroundColor Red
    }
    Write-WinBridgeLog -Message $Message -Name 'bootstrap' -Level 'ERROR'
}

function Set-WinBridgeProperty {
    param(
        [Parameter(Mandatory = $true)]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        [AllowNull()]$Value
    )
    if ($Object -is [hashtable]) {
        $Object[$Name] = $Value
        return
    }
    if ($null -eq $Object.PSObject.Properties[$Name]) {
        $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value
    } else {
        $Object.$Name = $Value
    }
}

function Set-WinBridgeClientList {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$Clients
    )
    if ($State -is [hashtable]) {
        $State['allowed_clients'] = @($Clients)
    } else {
        $State | Add-Member -NotePropertyName 'allowed_clients' -NotePropertyValue @($Clients) -Force
    }
}

function Get-WinBridgeFileSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    # Do not depend on Get-FileHash: a polluted PSModulePath (for example,
    # when powershell.exe is launched from PowerShell 7) can autoload the
    # PowerShell 7 Microsoft.PowerShell.Utility module and hide the cmdlet.
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $stream = [IO.File]::OpenRead($Path)
        try {
            $hash = $sha.ComputeHash($stream)
            return ([BitConverter]::ToString($hash) -replace '-', '').ToLowerInvariant()
        } finally {
            $stream.Dispose()
        }
    } finally {
        $sha.Dispose()
    }
}

function Test-WinBridgeBundleManifest {
    param([Parameter(Mandatory = $true)][string]$Root)
    $manifestPath = Join-Path $Root 'manifest.json'
    if (-not (Test-Path -LiteralPath $manifestPath)) {
        throw 'Bundle manifest.json is missing.'
    }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($null -eq $manifest.files) {
        throw 'Bundle manifest has no files list.'
    }
    $count = 0
    foreach ($entry in @($manifest.files)) {
        $relative = ([string]$entry.path).Replace('/', [IO.Path]::DirectorySeparatorChar)
        $path = Join-Path $Root $relative
        if (-not (Test-Path -LiteralPath $path)) {
            throw ('Bundle file is missing: ' + $entry.path)
        }
        $actual = Get-WinBridgeFileSha256 -Path $path
        if ($actual -ne ([string]$entry.sha256).ToLowerInvariant()) {
            throw ('Bundle hash mismatch: ' + $entry.path)
        }
        $count++
    }
    Write-WinBridgeStep ('Bundle manifest verified (' + $count + ' files).')
    return $manifest
}

function Test-WinBridgeAdmin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-WinBridgeAdmin {
    if (-not (Test-WinBridgeAdmin)) {
        throw 'This action requires an elevated Windows PowerShell session.'
    }
}

function Test-WinBridgeUserIsAdmin {
    try {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $sid = $identity.User.Value
        $group = Get-LocalGroup -SID 'S-1-5-32-544' -ErrorAction Stop
        $members = @(Get-LocalGroupMember -Group $group.Name -ErrorAction Stop)
        return (@($members | Where-Object { $_.SID.Value -eq $sid }).Count -gt 0)
    } catch {
        $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
        return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    }
}

function Get-WinBridgeInteractiveUser {
    try {
        $system = Get-CimInstance Win32_ComputerSystem -ErrorAction Stop
        if ($system.UserName) { return [string]$system.UserName }
    } catch {
        # The console owner cannot always be read; the caller treats null as unknown.
    }
    return $null
}

function Stop-WinBridgeStaleServer {
    $pidPath = Get-WinBridgePath 'state\server.pid'
    $process = Get-WinBridgeProcessFromPidFile -Path $pidPath
    if ($null -eq $process) {
        if (Test-Path -LiteralPath $pidPath) {
            Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
        }
        # A Go agent left by a supervisor that died without its pid file still holds the port.
        $goExe = Get-WinBridgePath 'app\go\winbridge-agent.exe'
        $orphans = @(Get-Process -Name 'winbridge-agent' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $goExe })
        foreach ($orphan in $orphans) {
            Stop-Process -Id $orphan.Id -Force -ErrorAction SilentlyContinue
            Wait-Process -Id $orphan.Id -Timeout 10 -ErrorAction SilentlyContinue
            Write-WinBridgeLog -Message ('Stopped orphan Go agent ' + $orphan.Id + '.') -Name 'supervisor' -Level 'WARN'
        }
        return ($orphans.Count -gt 0)
    }
    $commandLine = [string]$process.CommandLine
    $executable = [string]$process.ExecutablePath
    $ownExecutables = @((Get-WinBridgePath 'app\go\winbridge-agent.exe'), (Get-WinBridgePath 'app\venv\Scripts\python.exe'))
    $isOurs = $false
    foreach ($own in $ownExecutables) { if ($executable -ieq $own) { $isOurs = $true } }
    if (-not $isOurs -or $commandLine -notmatch 'winremote|winbridge-agent') {
        Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
        return $false
    }
    try {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop
        Wait-Process -Id $process.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
        Write-WinBridgeLog -Message ('Stopped stale server process ' + $process.ProcessId + '.') -Name 'supervisor' -Level 'WARN'
    } catch {
        Write-WinBridgeLog -Message ('Could not stop stale server process: ' + $_.Exception.Message) -Name 'supervisor' -Level 'WARN'
    }
    Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
    return $true
}

function Quote-WinBridgeArg {
    param([AllowNull()][string]$Value)
    if ($null -eq $Value) { return '""' }
    if ($Value -match '[\s"]') {
        return '"' + ($Value -replace '"', '\"') + '"'
    }
    return $Value
}

function Format-WinBridgeHttpHost {
    param([Parameter(Mandatory = $true)][string]$Address)
    $value = $Address.Trim()
    if ($value.StartsWith('[') -and $value.EndsWith(']')) {
        $value = $value.Substring(1, $value.Length - 2)
    }
    if ($value.Contains(':')) {
        return ('[' + $value + ']')
    }
    return $value
}

function Test-WinBridgeTailscaleAddress {
    param([AllowNull()][string]$Address)
    if ([string]::IsNullOrWhiteSpace($Address)) { return $false }
    $value = $Address.Trim()
    if ($value.StartsWith('[') -and $value.EndsWith(']')) {
        $value = $value.Substring(1, $value.Length - 2)
    }
    $parsed = $null
    if (-not [System.Net.IPAddress]::TryParse($value, [ref]$parsed)) { return $false }
    $bytes = $parsed.GetAddressBytes()
    if ($parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) {
        return ($bytes[0] -eq 100 -and $bytes[1] -ge 64 -and $bytes[1] -le 127)
    }
    if ($parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetworkV6) {
        return (
            $bytes[0] -eq 0xfd -and
            $bytes[1] -eq 0x7a -and
            $bytes[2] -eq 0x11 -and
            $bytes[3] -eq 0x5c -and
            $bytes[4] -eq 0xa1 -and
            $bytes[5] -eq 0xe0
        )
    }
    return $false
}

function Get-WinBridgeProcessFromPidFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try {
        $raw = (Get-Content -LiteralPath $Path -Raw).Trim()
        $processId = [int]$raw
        return (Get-CimInstance Win32_Process -Filter ('ProcessId=' + $processId) -ErrorAction SilentlyContinue)
    } catch {
        return $null
    }
}

function Invoke-WinBridgeHttp {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [hashtable]$Headers = @{},
        [int]$TimeoutSeconds = 5
    )
    $params = @{
        Uri = $Uri
        Method = 'Get'
        TimeoutSec = $TimeoutSeconds
        UseBasicParsing = $true
    }
    if ($Headers.Count -gt 0) {
        $params['Headers'] = $Headers
    }
    return Invoke-RestMethod @params
}

function Wait-WinBridgeHttp {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [int]$TimeoutSeconds = 60,
        [hashtable]$Headers = @{}
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastError = 'no response'
    do {
        try {
            return Invoke-WinBridgeHttp -Uri $Uri -Headers $Headers -TimeoutSeconds 5
        } catch {
            $lastError = $_.Exception.Message
            Start-Sleep -Milliseconds 750
        }
    } while ((Get-Date) -lt $deadline)
    throw ('Timed out waiting for ' + $Uri + ': ' + $lastError)
}

function Get-WinBridgeHealth {
    param(
        [Parameter(Mandatory = $true)][string]$Address,
        [int]$Port = $script:WinBridgeDefaultPort
    )
    $hostPart = Format-WinBridgeHttpHost -Address $Address
    return Invoke-WinBridgeHttp -Uri ('http://' + $hostPart + ':' + $Port + '/health')
}

function Get-WinBridgeStatus {
    param(
        [Parameter(Mandatory = $true)][string]$Address,
        [Parameter(Mandatory = $true)][string]$Key,
        [int]$Port = $script:WinBridgeDefaultPort
    )
    $hostPart = Format-WinBridgeHttpHost -Address $Address
    $headers = @{ Authorization = 'Bearer ' + $Key }
    return Invoke-WinBridgeHttp -Uri ('http://' + $hostPart + ':' + $Port + '/status') -Headers $headers
}

function Get-WinBridgeState {
    param([string]$Path = (Get-WinBridgePath 'state\state.json'))
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    return (Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json)
}

function Save-WinBridgeState {
    param(
        [Parameter(Mandatory = $true)]$State,
        [string]$Path = (Get-WinBridgePath 'state\state.json')
    )
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $Path)
    $json = $State | ConvertTo-Json -Depth 10
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($Path, $json, $encoding)
}

function Get-WinBridgePort {
    $state = Get-WinBridgeState
    if ($null -ne $state -and $null -ne $state.port) {
        return [int]$state.port
    }
    return $script:WinBridgeDefaultPort
}

function Test-WinBridgeTcp {
    param(
        [Parameter(Mandatory = $true)][string]$TargetHost,
        [Parameter(Mandatory = $true)][int]$Port,
        [int]$TimeoutMilliseconds = 1500
    )
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect($TargetHost, $Port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne($TimeoutMilliseconds)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Get-WinBridgeTcpListener {
    param([Parameter(Mandatory = $true)][int]$Port)
    return @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
}

function Get-WinBridgeInstalledVersion {
    $python = Get-WinBridgePath 'app\venv\Scripts\python.exe'
    $go = Get-WinBridgePath 'app\go\winbridge-agent.exe'
    $agentFlag = Get-WinBridgePath 'state\agent.txt'
    $goSelected = (Test-Path -LiteralPath $go) -and (Test-Path -LiteralPath $agentFlag) -and ((Get-Content -LiteralPath $agentFlag -Raw).Trim() -eq 'go')
    if ($goSelected -or -not (Test-Path -LiteralPath $python)) {
        if (-not (Test-Path -LiteralPath $go)) { return $null }
        try {
            $value = & $go --version 2>$null
            if ($LASTEXITCODE -ne 0) { return $null }
            return (([string]$value).Trim() -replace '^winbridge-agent\s+', '')
        } catch {
            return $null
        }
    }
    try {
        $value = & $python -c "import winremote; print(winremote.__version__)" 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return ([string]$value).Trim()
    } catch {
        return $null
    }
}

function Rotate-WinBridgeLog {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [long]$MaxBytes = 10485760,
        [int]$Keep = 5
    )
    if (-not (Test-Path -LiteralPath $Path)) { return }
    $item = Get-Item -LiteralPath $Path
    if ($item.Length -lt $MaxBytes) { return }
    for ($index = $Keep - 1; $index -ge 1; $index--) {
        $source = $Path + '.' + $index
        $target = $Path + '.' + ($index + 1)
        if (Test-Path -LiteralPath $source) {
            Move-Item -LiteralPath $source -Destination $target -Force
        }
    }
    Move-Item -LiteralPath $Path -Destination ($Path + '.1') -Force
}

function Get-WinBridgeLogTail {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [int]$Lines = 200
    )
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    return @(Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue)
}

if (-not ('WinBridge.DesktopState' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

namespace WinBridge {
    public static class DesktopState {
        [DllImport("user32.dll", SetLastError = true)]
        private static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint desiredAccess);

        [DllImport("user32.dll", SetLastError = true)]
        private static extern bool CloseDesktop(IntPtr desktop);

        public static bool IsInputDesktopAvailable() {
            IntPtr handle = OpenInputDesktop(0, false, 0x0100);
            if (handle == IntPtr.Zero) { return false; }
            CloseDesktop(handle);
            return true;
        }
    }
}
'@
}

function Test-WinBridgeSessionUnlocked {
    try {
        return [WinBridge.DesktopState]::IsInputDesktopAvailable()
    } catch {
        return $false
    }
}


# Records how the last install ended in state\last-install.json; it never holds a key.
function Write-WinBridgeInstallResult {
    param(
        [Parameter(Mandatory = $true)][bool]$Ok,
        [string]$ErrorMessage,
        [string]$Version,
        [string]$BuildId,
        [string]$ExeSha256
    )
    try {
        $stateDir = Get-WinBridgePath 'state'
        Ensure-WinBridgeDirectory -Path $stateDir
        $record = [ordered]@{
            ok = $Ok
            error = (Redact-WinBridgeText $ErrorMessage)
            version = $Version
            build_id = $BuildId
            exe_sha256 = $ExeSha256
            installed_at = (Get-Date).ToString('o')
        }
        $json = $record | ConvertTo-Json -Compress
        [IO.File]::WriteAllText((Join-Path $stateDir 'last-install.json'), $json, (New-Object System.Text.UTF8Encoding($false)))
    } catch {
        # The result file is a convenience; failing to write it must not hide the install error.
    }
}

# Prints the result of the last install so a closed elevated window never hides a failure; returns $true when it was ok.
function Show-WinBridgeInstallResult {
    $path = Get-WinBridgePath 'state\last-install.json'
    if (-not (Test-Path -LiteralPath $path)) {
        Write-Host 'No install result was recorded.' -ForegroundColor Yellow
        return $false
    }
    $record = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    if ($record.ok) {
        Write-Host ('Install result: OK (version ' + $record.version + ', build ' + $record.build_id + ').') -ForegroundColor Green
        return $true
    }
    Write-Host ('Install result: FAILED - ' + $record.error) -ForegroundColor Red
    return $false
}


# Limits the install root to the owner, SYSTEM and Administrators: the supervisor runs the agent and its scripts from
# there, so no other local account may be able to replace them. A failure is reported but does not stop the install.
function Protect-WinBridgeRoot {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $userSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        $rights = '(OI)(CI)F'
        & icacls.exe $Path /inheritance:r /grant:r ('*' + $userSid + ':' + $rights) ('*S-1-5-18:' + $rights) ('*S-1-5-32-544:' + $rights) | Out-Null
        if ($LASTEXITCODE -ne 0) { throw ('icacls exited with ' + $LASTEXITCODE) }
    } catch {
        Write-WinBridgeWarn ('Could not restrict ' + $Path + ' to the owner: ' + $_.Exception.Message)
    }
}
