#requires -Version 5.1
# Read-only contract tests for the WinBridge bootstrap.
#
# This never installs anything, never touches services, firewall rules,
# Credential Manager, Tailscale or SSH. It dot-sources the libraries with a
# throwaway LOCALAPPDATA and checks pure functions and repository contracts.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\test-bootstrap.ps1
#   pwsh -NoProfile -File tools/test-bootstrap.ps1

[CmdletBinding()]
param()

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$bootstrapRoot = Join-Path $repoRoot 'bootstrap'
$passes = 0
$failures = New-Object System.Collections.Generic.List[string]

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Name,
        [string]$Detail = ''
    )
    if (-not $Condition) {
        if ($Detail) { throw ($Name + ': ' + $Detail) }
        throw $Name
    }
}

function Assert-Equal {
    param(
        [AllowNull()]$Expected,
        [AllowNull()]$Actual,
        [Parameter(Mandatory = $true)][string]$Name
    )
    $expectedText = if ($null -eq $Expected) { '<null>' } else { [string]$Expected }
    $actualText = if ($null -eq $Actual) { '<null>' } else { [string]$Actual }
    if ($expectedText -cne $actualText) {
        throw ($Name + ': expected [' + $expectedText + '] but found [' + $actualText + ']')
    }
}

function Assert-Throws {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Body,
        [Parameter(Mandatory = $true)][string]$Name
    )
    $threw = $false
    try {
        & $Body | Out-Null
    } catch {
        $threw = $true
    }
    if (-not $threw) {
        throw ($Name + ': expected an exception, but none was thrown')
    }
}

function Test-Case {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Body
    )
    try {
        & $Body
        $script:passes++
        Write-Host ('[ OK ] ' + $Name) -ForegroundColor Green
    } catch {
        $script:failures.Add($Name + ' :: ' + $_.Exception.Message)
        Write-Host ('[FAIL] ' + $Name + ' :: ' + $_.Exception.Message) -ForegroundColor Red
    }
}

$originalLocalAppData = $env:LOCALAPPDATA
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('winbridge-tests-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
$env:LOCALAPPDATA = $tempRoot

try {
    $lib = Join-Path $bootstrapRoot 'lib'
    . (Join-Path $lib 'Common.ps1')
    . (Join-Path $lib 'CredentialManager.ps1')
    . (Join-Path $lib 'Tailscale.ps1')
    . (Join-Path $lib 'WinBridgeConfig.ps1')
    . (Join-Path $lib 'OpenSsh.ps1')
    . (Join-Path $lib 'Firewall.ps1')
    . (Join-Path $lib 'PythonRuntime.ps1')
    . (Join-Path $lib 'ScheduledTask.ps1')
    . (Join-Path $lib 'Doctor.ps1')

    $expectedTools = @(
        'Snapshot', 'Click', 'Type', 'Move', 'Scroll',
        'Shortcut', 'FocusWindow', 'Wait', 'GetSystemInfo'
    )

    # ---- Parser ---------------------------------------------------------
    Test-Case 'all PowerShell files parse' {
        $files = @(
            Get-ChildItem -LiteralPath $repoRoot -Recurse -Filter '*.ps1' -File |
                Where-Object { $_.FullName -notmatch '[\\/](\.git|build|dist|node_modules)[\\/]' }
        )
        Assert-True -Condition ($files.Count -ge 20) -Name 'expected at least 20 PowerShell files' -Detail ([string]$files.Count)
        foreach ($file in $files) {
            $tokens = $null
            $errors = $null
            [System.Management.Automation.Language.Parser]::ParseFile($file.FullName, [ref]$tokens, [ref]$errors) | Out-Null
            if ($null -ne $errors -and $errors.Count -gt 0) {
                throw ($file.FullName + ': ' + $errors[0].Message)
            }
        }
    }

    # ---- Firewall audit -------------------------------------------------
    function New-FakeFirewallRule {
        param([string]$Name, [string]$DisplayName, [string]$Protocol = 'TCP', [string[]]$LocalPort = @('22'),
            [string]$Program = 'Any', [string]$Service = 'Any', [string[]]$Remote = @('Any'))
        [pscustomobject]@{
            Rule = [pscustomobject]@{ Name = $Name; DisplayName = $DisplayName; InstanceID = $Name }
            Port = [pscustomobject]@{ InstanceID = $Name; Protocol = $Protocol; LocalPort = $LocalPort }
            Application = [pscustomobject]@{ InstanceID = $Name; Program = $Program }
            Service = [pscustomobject]@{ InstanceID = $Name; Service = $Service }
            Address = [pscustomobject]@{ InstanceID = $Name; RemoteAddress = $Remote }
        }
    }

    function Invoke-FakeFirewallAudit {
        param([object[]]$Fakes, [int]$Port = 22)
        $script:filterReads = @{ Calls = 0 }
        $reads = $script:filterReads
        $ruleReader = { $Fakes | ForEach-Object { $_.Rule } }.GetNewClosure()
        $filterReader = {
            $reads.Calls++
            $index = @{ Port = @{}; Application = @{}; Service = @{}; Address = @{} }
            foreach ($fake in $Fakes) {
                foreach ($kind in $index.Keys) { $index[$kind][[string]$fake.Rule.InstanceID] = $fake.$kind }
            }
            $index
        }.GetNewClosure()
        @(Get-WinBridgeBroadPortRules -Port $Port -RuleReader $ruleReader -FilterReader $filterReader)
    }

    Test-Case 'firewall audit flags only broad TCP rules that can reach the port' {
        $fakes = @(
            (New-FakeFirewallRule -Name 'r1' -DisplayName 'broad ssh' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r2' -DisplayName $script:WinBridgeSshRule -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r3' -DisplayName 'tailscale only' -Remote @('100.64.0.0/10')),
            (New-FakeFirewallRule -Name 'r4' -DisplayName 'udp' -Protocol 'UDP' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r5' -DisplayName 'other program' -Program 'C:\Tools\other.exe' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r6' -DisplayName 'other port' -LocalPort @('80') -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r7' -DisplayName 'any port' -LocalPort @('Any') -Remote @('0.0.0.0/0')),
            (New-FakeFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'builtin' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r9' -DisplayName 'other service' -Service 'Spooler' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r10' -DisplayName 'sshd program' -Program 'C:\Windows\System32\OpenSSH\sshd.exe' -Remote @('Any'))
        )
        $found = Invoke-FakeFirewallAudit -Fakes $fakes
        Assert-Equal -Expected 'r1,r7,r10' -Actual (($found | ForEach-Object { $_.Name }) -join ',') -Name 'broad rules found'
    }

    Test-Case 'firewall audit reads the four filter sets once, not once per rule' {
        $fakes = 1..30 | ForEach-Object { New-FakeFirewallRule -Name ('r' + $_) -DisplayName ('rule ' + $_) -Remote @('100.64.0.0/10') }
        $null = Invoke-FakeFirewallAudit -Fakes $fakes
        Assert-Equal -Expected 1 -Actual $script:filterReads.Calls -Name 'filter reader calls'
    }

    Test-Case 'firewall audit skips one rule whose filters are missing and still checks the others' {
        $fakes = @(
            (New-FakeFirewallRule -Name 'r1' -DisplayName 'vanished' -Remote @('Any')),
            (New-FakeFirewallRule -Name 'r2' -DisplayName 'broad' -Remote @('Any'))
        )
        $ruleReader = { $Fakes | ForEach-Object { $_.Rule } }.GetNewClosure()
        $partialReader = {
            $index = @{ Port = @{}; Application = @{}; Service = @{}; Address = @{} }
            foreach ($kind in $index.Keys) { $index[$kind]['r2'] = $Fakes[1].$kind }
            $index
        }.GetNewClosure()
        $found = @(Get-WinBridgeBroadPortRules -Port 22 -RuleReader $ruleReader -FilterReader $partialReader -WarningAction SilentlyContinue)
        Assert-Equal -Expected 'r2' -Actual (($found | ForEach-Object { $_.Name }) -join ',') -Name 'rules reported'
    }

    Test-Case 'firewall audit fails when the filters cannot be read, never reporting a clean result' {
        $fakes = @((New-FakeFirewallRule -Name 'r1' -DisplayName 'broad' -Remote @('Any')))
        $ruleReader = { $Fakes | ForEach-Object { $_.Rule } }.GetNewClosure()
        Assert-Throws -Name 'filter reader that throws' -Body {
            Get-WinBridgeBroadPortRules -Port 22 -RuleReader $ruleReader -FilterReader { throw 'cim down' } -WarningAction SilentlyContinue
        }
    }

    Test-Case 'firewall audit fails when the filter index comes back empty although rules exist' {
        $fakes = @((New-FakeFirewallRule -Name 'r1' -DisplayName 'broad' -Remote @('Any')))
        $ruleReader = { $Fakes | ForEach-Object { $_.Rule } }.GetNewClosure()
        $emptyReader = { @{ Port = @{}; Application = @{}; Service = @{}; Address = @{} } }
        Assert-Throws -Name 'empty filter index' -Body {
            Get-WinBridgeBroadPortRules -Port 22 -RuleReader $ruleReader -FilterReader $emptyReader -WarningAction SilentlyContinue
        }
    }

    # ---- Tool profile ---------------------------------------------------
    Test-Case 'doctor expects the exact nine tools' {
        Assert-Equal -Expected 9 -Actual @($script:WinBridgeExpectedTools).Count -Name 'doctor tool count'
        foreach ($name in $expectedTools) {
            Assert-True -Condition ($script:WinBridgeExpectedTools -contains $name) -Name ('doctor is missing ' + $name)
        }
    }

    Test-Case 'UTF-8 output helper encodes accents without a BOM' {
        $previous = [Console]::OutputEncoding
        try {
            Set-WinBridgeUtf8Output
            $text = 'Excepci' + [char]0x00F3 + 'n sesi' + [char]0x00F3 + 'n'
            $bytes = [Console]::OutputEncoding.GetBytes($text)
            Assert-Equal -Expected 65001 -Actual ([Console]::OutputEncoding.CodePage) -Name 'console code page'
            Assert-Equal -Expected 0 -Actual ([Console]::OutputEncoding.GetPreamble().Length) -Name 'console BOM'
            Assert-True -Condition (($bytes -join ',') -eq '69,120,99,101,112,99,105,195,179,110,32,115,101,115,105,195,179,110') -Name 'UTF-8 bytes of the accented text'
        } finally {
            [Console]::OutputEncoding = $previous
        }
    }

    # ---- sshd review (read-only) ---------------------------------------
    Test-Case 'sshd -T output is parsed into lowercase keys' {
        $settings = ConvertFrom-WinBridgeSshdOutput -Lines @('maxstartups 10:30:100', 'MaxAuthTries 3', 'noise', '')
        Assert-Equal -Expected '10:30:100' -Actual $settings['maxstartups'] -Name 'maxstartups'
        Assert-Equal -Expected '3' -Actual $settings['maxauthtries'] -Name 'maxauthtries'
    }

    Test-Case 'sshd_settings shows the three effective limits or SKIPs with the cause' {
        $ok = New-WinBridgeSshdSettingsCheck -Settings @{ maxstartups = '10:30:100'; maxauthtries = '3'; logingracetime = '30' } -Failure ''
        Assert-Equal -Expected 'sshd_settings' -Actual $ok.name -Name 'name'
        Assert-Equal -Expected 'OK' -Actual $ok.status -Name 'status'
        Assert-Equal -Expected 'maxstartups=10:30:100 maxauthtries=3 logingracetime=30' -Actual $ok.detail -Name 'detail'
        $partial = New-WinBridgeSshdSettingsCheck -Settings @{ maxauthtries = '3' } -Failure ''
        Assert-Equal -Expected 'maxstartups=not reported maxauthtries=3 logingracetime=not reported' -Actual $partial.detail -Name 'partial detail'
        $skip = New-WinBridgeSshdSettingsCheck -Settings $null -Failure 'access denied'
        Assert-Equal -Expected 'SKIP' -Actual $skip.status -Name 'skip status'
        Assert-True -Condition ($skip.detail -like '*access denied*') -Name 'skip detail names the cause'
    }

    Test-Case 'sshd failures count pre-auth closes and invalid users separately' {
        $messages = @(
            'sshd: Accepted publickey for alice from 100.64.0.2 port 1 ssh2: ED25519 SHA256:abc',
            'sshd: Connection closed by invalid user bob 100.64.0.2 port 2 [preauth]',
            'sshd: Invalid user bob from 100.64.0.2 port 2',
            'sshd: Connection closed by 100.64.0.2 port 3',
            'sshd: Failed password for invalid user dave from 100.64.0.2 port 5 ssh2',
            'sshd-session: Invalid user erin from 100.64.0.2 port 6'
        )
        $counts = Measure-WinBridgeSshdFailures -Messages $messages
        Assert-Equal -Expected 1 -Actual $counts.PreauthClosed -Name 'pre-auth closes'
        Assert-Equal -Expected 2 -Actual $counts.InvalidUsers -Name 'invalid users, whichever process prefix the log uses'
        $none = Measure-WinBridgeSshdFailures -Messages @()
        Assert-Equal -Expected 0 -Actual ($none.PreauthClosed + $none.InvalidUsers) -Name 'no messages'
    }

    Test-Case 'sshd_failures: no events is zero, an unreadable log is SKIP with the cause' {
        $since = (Get-Date).AddHours(-1)
        $messages = @('sshd: Invalid user bob from 100.64.0.2 port 2')
        $found = New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since $since -Reader { param($s) $messages })
        Assert-Equal -Expected 'sshd_failures' -Actual $found.name -Name 'name'
        Assert-Equal -Expected 'OK' -Actual $found.status -Name 'status'
        Assert-Equal -Expected 'last hour: preauth_closed=0 invalid_user=1' -Actual $found.detail -Name 'detail'

        $empty = New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since $since -Reader {
            param($s)
            throw ([System.Management.Automation.ErrorRecord]::new(
                (New-Object System.Exception 'No events were found'),
                'NoMatchingEventsFound,Microsoft.PowerShell.Commands.GetWinEventCommand',
                [System.Management.Automation.ErrorCategory]::ObjectNotFound, $null))
        })
        Assert-Equal -Expected 'OK' -Actual $empty.status -Name 'zero events status'
        Assert-Equal -Expected 'last hour: preauth_closed=0 invalid_user=0' -Actual $empty.detail -Name 'zero events detail'

        $denied = New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since $since -Reader {
            param($s) throw 'Attempted to perform an unauthorized operation.'
        })
        Assert-Equal -Expected 'SKIP' -Actual $denied.status -Name 'denied status'
        Assert-True -Condition ($denied.detail -like '*unauthorized operation*') -Name 'denied detail names the cause'
    }

    Test-Case 'sshd_failures says when the event cap was reached' {
        $three = @('sshd: Invalid user a from 100.64.0.2 port 1', 'sshd: Invalid user b from 100.64.0.2 port 2', 'sshd: Invalid user c from 100.64.0.2 port 3')
        $capped = New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since (Get-Date).AddHours(-1) -MaxEvents 3 -Reader { param($s, $max) $three })
        Assert-True -Condition ($capped.detail -like '*invalid_user=3*capped at 3 events*') -Name ('capped detail: ' + $capped.detail)
        $under = New-WinBridgeSshdFailuresCheck -Result (Get-WinBridgeSshdFailureCounts -Since (Get-Date).AddHours(-1) -MaxEvents 4 -Reader { param($s, $max) $three })
        Assert-True -Condition ($under.detail -notlike '*capped*') -Name 'not capped under the limit'
    }

    Test-Case 'sshd -T failure returns null with the cause and keeps the error preference' {
        $original = ${function:Get-WinBridgeSshdExe}
        $isWindowsHost = ($env:OS -eq 'Windows_NT')
        $fake = Join-Path $tempRoot $(if ($isWindowsHost) { 'fake-sshd.cmd' } else { 'fake-sshd.sh' })
        if ($isWindowsHost) {
            [IO.File]::WriteAllText($fake, "@echo unable to load host key 1>&2`r`n@exit /b 1`r`n")
        } else {
            [IO.File]::WriteAllText($fake, "#!/bin/sh`necho 'unable to load host key' >&2`nexit 1`n")
            & chmod +x $fake
        }
        $before = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Stop'
            Set-Item -Path function:Get-WinBridgeSshdExe -Value { $fake }.GetNewClosure()
            $failure = ''
            $result = Get-WinBridgeSshdSettings -Failure ([ref]$failure)
            Assert-True -Condition ($null -eq $result) -Name 'settings are null when sshd -T fails'
            Assert-True -Condition ($failure -like '*unable to load host key*') -Name ('failure names the cause: ' + $failure)
            Assert-Equal -Expected 'Stop' -Actual ([string]$ErrorActionPreference) -Name 'error preference restored'
        } finally {
            $ErrorActionPreference = $before
            Set-Item -Path function:Get-WinBridgeSshdExe -Value $original
        }
    }

    Test-Case 'TOML template uses the exact nine tools and one placeholder each' {
        $templatePath = Join-Path $bootstrapRoot 'config\winbridge.toml.template'
        $template = Get-Content -LiteralPath $templatePath -Raw
        foreach ($token in @('{{TAILSCALE_IP}}', '{{PORT}}', '{{ALLOWED_CLIENTS}}')) {
            $count = ([regex]::Matches($template, [regex]::Escape($token))).Count
            Assert-Equal -Expected 1 -Actual $count -Name ($token + ' occurrences')
        }
        $enableBlock = [regex]::Match($template, '(?s)enable\s*=\s*\[(.*?)\]').Groups[1].Value
        $toolNames = @([regex]::Matches($enableBlock, '"([^"]+)"') | ForEach-Object { $_.Groups[1].Value })
        Assert-Equal -Expected 9 -Actual $toolNames.Count -Name 'template tool count'
        foreach ($name in $expectedTools) {
            Assert-True -Condition ($toolNames -contains $name) -Name ('template is missing ' + $name)
        }
        Assert-True -Condition ($template -notmatch '(?i)auth[_-]?key\s*=') -Name 'template must not contain an auth key'
        Assert-True -Condition ($template -match '(?m)^\s*enable_tier3\s*=\s*false\s*$') -Name 'template must disable tier 3'
        Assert-True -Condition ($template -match '(?m)^\s*host\s*=\s*"\{\{TAILSCALE_IP\}\}"\s*$') -Name 'template host must be the placeholder'
    }

    # ---- Address validation --------------------------------------------
    Test-Case 'Tailscale address validation' {
        foreach ($address in @('100.64.0.1', '100.127.255.254', 'fd7a:115c:a1e0::1', '[fd7a:115c:a1e0::1]')) {
            Assert-True -Condition (Test-WinBridgeTailscaleAddress -Address $address) -Name ('should accept ' + $address)
        }
        foreach ($address in @('100.63.255.255', '100.128.0.0', '0.0.0.0', '::', '127.0.0.1', '192.168.1.2', '8.8.8.8', 'foo', '')) {
            Assert-True -Condition (-not (Test-WinBridgeTailscaleAddress -Address $address)) -Name ('should reject ' + $address)
        }
    }

    Test-Case 'client allowlist accepts only Tailscale IPs and safe CIDRs' {
        Assert-Equal -Expected '100.64.1.2/32' -Actual (Test-WinBridgeClientCidr -Value '100.64.1.2') -Name 'IPv4 host'
        Assert-Equal -Expected '100.64.0.0/10' -Actual (Test-WinBridgeClientCidr -Value '100.64.0.0/10') -Name 'IPv4 tailnet'
        Assert-Equal -Expected 'fd7a:115c:a1e0::1/128' -Actual (Test-WinBridgeClientCidr -Value 'fd7a:115c:a1e0::1') -Name 'IPv6 host'
        foreach ($value in @('0.0.0.0', '127.0.0.1', '192.168.1.2', '8.8.8.8', '100.64.0.0/8', '100.64.0.1/33', 'fd7a:115c:a1e0::/32', 'nonsense', '')) {
            Assert-True -Condition ($null -eq (Test-WinBridgeClientCidr -Value $value)) -Name ('should reject ' + $value)
        }
    }

    Test-Case 'HTTP host formatting handles IPv6' {
        Assert-Equal -Expected '100.64.1.2' -Actual (Format-WinBridgeHttpHost -Address '100.64.1.2') -Name 'IPv4'
        Assert-Equal -Expected '[fd7a:115c:a1e0::1]' -Actual (Format-WinBridgeHttpHost -Address 'fd7a:115c:a1e0::1') -Name 'IPv6'
        Assert-Equal -Expected '[fd7a:115c:a1e0::1]' -Actual (Format-WinBridgeHttpHost -Address '[fd7a:115c:a1e0::1]') -Name 'bracketed IPv6'
    }

    # ---- Auth key handling ---------------------------------------------
    Test-Case 'auth key validation is strict' {
        $valid = 'A' * 32
        Assert-Equal -Expected $valid -Actual (Assert-WinBridgeKey -Key $valid) -Name 'valid key'
        foreach ($invalid in @('', 'short', ('A' * 31), ('A' * 513), ('A' * 30 + ' ' + 'B'), ('A' * 30 + "`n" + 'B'))) {
            Assert-Throws -Body { Assert-WinBridgeKey -Key $invalid } -Name 'invalid key'
        }
    }

    Test-Case 'key fingerprint is stable and non-reversible' {
        $key = 'test-key-0123456789abcdef0123456789abcdef'
        $first = Get-WinBridgeKeyFingerprint -Secret $key
        $second = Get-WinBridgeKeyFingerprint -Secret $key
        $other = Get-WinBridgeKeyFingerprint -Secret ($key + 'x')
        Assert-Equal -Expected $first -Actual $second -Name 'stable fingerprint'
        Assert-True -Condition ($first -match '^[0-9a-f]{16}$') -Name 'fingerprint shape'
        Assert-True -Condition ($first -ne $other) -Name 'different keys must differ'
        Assert-True -Condition ($first -notlike ('*' + $key + '*')) -Name 'fingerprint must not contain the key'
    }

    Test-Case 'SSH public key fingerprint matches ssh-keygen format' {
        $blob = [byte[]](1..48)
        $line = 'ssh-ed25519 ' + [Convert]::ToBase64String($blob) + ' test@example'
        $sha = [Security.Cryptography.SHA256]::Create()
        try {
            $expected = 'SHA256:' + [Convert]::ToBase64String($sha.ComputeHash($blob)).TrimEnd('=')
        } finally {
            $sha.Dispose()
        }
        Assert-Equal -Expected $expected -Actual (Get-WinBridgePublicKeyFingerprint -Line $line) -Name 'key fingerprint'
        Assert-True -Condition (Test-WinBridgePublicKeyLine -Line $line) -Name 'public key line accepted'
        Assert-True -Condition (-not (Test-WinBridgePublicKeyLine -Line 'ssh-ed25519 not-base64 key')) -Name 'garbage rejected'
    }

    # ---- Config rendering ----------------------------------------------
    Test-Case 'config rendering writes only Tailscale hosts and nine tools' {
        $state = Get-WinBridgeDefaultState
        Set-WinBridgeProperty -Object $state -Name 'tailscale_ip' -Value '100.64.1.2'
        Set-WinBridgeProperty -Object $state -Name 'port' -Value 8090
        Add-WinBridgeClient -State $state -Client '100.64.1.3' | Out-Null
        $configPath = Join-Path $tempRoot 'render\winbridge.toml'
        $templatePath = Join-Path $bootstrapRoot 'config\winbridge.toml.template'
        Write-WinBridgeConfig -State $state -TemplatePath $templatePath -ConfigPath $configPath | Out-Null

        $rendered = Get-Content -LiteralPath $configPath -Raw
        Assert-True -Condition ($rendered -match '(?m)^host = "100\.64\.1\.2"\r?$') -Name 'host rendered'
        Assert-True -Condition ($rendered -match '(?m)^port = 8090\r?$') -Name 'port rendered'
        Assert-True -Condition ($rendered.Contains('"100.64.1.2"')) -Name 'self IP in allowlist'
        Assert-True -Condition ($rendered.Contains('"100.64.1.3/32"')) -Name 'client IP in allowlist'
        Assert-True -Condition ($rendered -notmatch '\{\{') -Name 'no placeholders left'
        Assert-True -Condition ($rendered -notmatch '(?im)^\s*auth[_-]?key\s*=') -Name 'no auth key assignment in config'
        Assert-True -Condition ($rendered -notmatch 'WINREMOTE_AUTH_KEY\s*=') -Name 'no auth key environment assignment in config'
        Assert-True -Condition ($rendered -notmatch '(?m)^\s*enable_tier3\s*=\s*true') -Name 'tier 3 must stay disabled'
        $enableBlock = [regex]::Match($rendered, '(?s)enable\s*=\s*\[(.*?)\]').Groups[1].Value
        $toolNames = @([regex]::Matches($enableBlock, '"([^"]+)"') | ForEach-Object { $_.Groups[1].Value })
        Assert-Equal -Expected 9 -Actual $toolNames.Count -Name 'rendered tool count'
    }

    Test-Case 'local.toml guard accepts the files table and refuses any other table' {
        Assert-WinBridgeLocalConfig -Text ("[files]`r`nread_roots = ['C:\\Users\\alice\\Shared']`r`n")
        Assert-WinBridgeLocalConfig -Text ("[jobs]`r`nroot = 'C:\\ProgramData\\WinBridgeJobs'`r`n[files]`r`nread_roots = ['D:\\data']`r`n")
        foreach ($bad in @('[security]', '[server]', '[tools]', '[files.extra]', '[filesystem]')) {
            Assert-Throws -Body { Assert-WinBridgeLocalConfig -Text ($bad + "`r`nx = 1`r`n") } -Name ('guard must refuse ' + $bad)
        }
    }

    Test-Case 'local.toml guard accepts write_roots in the files table' {
        Assert-WinBridgeLocalConfig -Text ("[files]`r`nread_roots = ['C:\\in']`r`nwrite_roots = ['C:\\out']`r`n")
    }

    Test-Case 'doctor files_write check is silent without write folders, warns while they exist and fails when one is missing' {
        $none = New-WinBridgeFilesWriteCheck -WriteRoots @()
        Assert-True -Condition ($null -eq $none) -Name 'no write folders, no check'
        $present = Join-Path ([IO.Path]::GetTempPath()) ('wb-write-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $present | Out-Null
        try {
            $warn = New-WinBridgeFilesWriteCheck -WriteRoots @($present)
            Assert-Equal -Expected 'files_write' -Actual $warn.name -Name 'check name'
            Assert-Equal -Expected 'WARN' -Actual $warn.status -Name 'an active write folder is a warning, not a failure'
            Assert-True -Condition ($warn.detail -eq ('write access is enabled for: ' + $present)) -Name 'detail names the folder'
            $missing = Join-Path $present 'gone'
            $fail = New-WinBridgeFilesWriteCheck -WriteRoots @($present, $missing)
            Assert-Equal -Expected 'FAIL' -Actual $fail.status -Name 'a missing write folder fails'
            Assert-True -Condition ($fail.detail.Contains($missing)) -Name 'the missing folder is named'
        } finally {
            Remove-Item -LiteralPath $present -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    Test-Case 'a native command that writes to stderr is captured, not thrown, under ErrorActionPreference Stop' {
        $self = (Get-Process -Id $PID).Path
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Stop'
        try {
            $r = Invoke-WinBridgeNative -FilePath $self -Arguments @('-NoProfile', '-Command', '[Console]::Error.WriteLine(''agent-says-no''); exit 3')
        } finally {
            $ErrorActionPreference = $previous
        }
        Assert-Equal -Expected 3 -Actual $r.ExitCode -Name 'exit code survives'
        Assert-True -Condition ($r.Output -match 'agent-says-no') -Name 'stderr text is returned'
    }

    Test-Case 'config rendering refuses a non-Tailscale host' {
        $state = Get-WinBridgeDefaultState
        Set-WinBridgeProperty -Object $state -Name 'tailscale_ip' -Value '192.168.1.5'
        $configPath = Join-Path $tempRoot 'render-bad\winbridge.toml'
        Assert-Throws -Body {
            Write-WinBridgeConfig -State $state -ConfigPath $configPath
        } -Name 'LAN bind must be refused'
        Assert-True -Condition (-not (Test-Path -LiteralPath $configPath)) -Name 'bad config must not be written'
    }

    Test-Case 'allowlist does not accept public or loopback clients' {
        $state = Get-WinBridgeDefaultState
        foreach ($value in @('0.0.0.0', '127.0.0.1', '192.168.1.10', '8.8.8.8')) {
            Assert-Throws -Body {
                Add-WinBridgeClient -State $state -Client $value | Out-Null
            } -Name ('client ' + $value + ' must be refused')
        }
    }

    # ---- Manifest -------------------------------------------------------
    Test-Case 'bundle manifest detects modification' {
        $root = Join-Path $tempRoot 'manifest'
        New-Item -ItemType Directory -Path $root -Force | Out-Null
        $file = Join-Path $root 'file.txt'
        [IO.File]::WriteAllText($file, 'hello', (New-Object System.Text.UTF8Encoding($false)))
        $hash = Get-WinBridgeFileSha256 -Path $file
        $manifest = [pscustomobject]@{
            schema = 1
            version = '0.1.0'
            files = @([pscustomobject]@{ path = 'file.txt'; size = 5; sha256 = $hash })
        }
        $manifestJson = $manifest | ConvertTo-Json -Depth 5
        [IO.File]::WriteAllText((Join-Path $root 'manifest.json'), $manifestJson, (New-Object System.Text.UTF8Encoding($false)))
        $loaded = Test-WinBridgeBundleManifest -Root $root
        Assert-Equal -Expected '0.1.0' -Actual ([string]$loaded.version) -Name 'manifest version'
        [IO.File]::WriteAllText($file, 'tampered', (New-Object System.Text.UTF8Encoding($false)))
        Assert-Throws -Body { Test-WinBridgeBundleManifest -Root $root } -Name 'modified file must fail verification'
    }

    # ---- Repository contracts ------------------------------------------
    Test-Case 'scripts dot-source the shared library and keep JSON stdout quiet' {
        $scripts = @(Get-ChildItem -LiteralPath (Join-Path $bootstrapRoot 'scripts') -Filter '*.ps1' -File)
        Assert-True -Condition ($scripts.Count -ge 8) -Name 'expected management scripts'
        foreach ($script in $scripts) {
            $text = Get-Content -LiteralPath $script.FullName -Raw
            Assert-True -Condition ($text -match "lib\\Common\.ps1") -Name ($script.Name + ' must dot-source Common.ps1')
            if ($text -match '\[switch\]\$Json') {
                Assert-True -Condition ($text -match 'Set-WinBridgeConsoleQuiet') -Name ($script.Name + ' must quiet console output in JSON mode')
            }
        }
    }

    Test-Case 'supervisor keeps the key out of argv and enforces strict mode' {
        $text = Get-Content -LiteralPath (Join-Path $bootstrapRoot 'scripts\supervisor.ps1') -Raw
        Assert-True -Condition ($text -match "'--transport',\s*'streamable-http'") -Name 'streamable-http transport'
        Assert-True -Condition ($text -match "'--host'") -Name 'explicit host'
        Assert-True -Condition ($text -match "'--config'") -Name 'explicit config'
        Assert-True -Condition ($text -notmatch "--auth-key") -Name 'auth key must not be in argv'
        Assert-True -Condition ($text -match 'WINREMOTE_AUTH_KEY') -Name 'key comes from the environment'
        Assert-True -Condition ($text -match "WINBRIDGE_STRICT\s*=\s*'1'") -Name 'strict mode is set'
    }

    Test-Case 'bootstrap and config enforce the reviewed invariants' {
        $bootstrap = Get-Content -LiteralPath (Join-Path $bootstrapRoot 'bootstrap.ps1') -Raw
        foreach ($forbidden in @('--enable-all', '--enable-tier3', '0.0.0.0')) {
            Assert-True -Condition ($bootstrap -notmatch [regex]::Escape($forbidden)) -Name ('bootstrap must not contain ' + $forbidden)
        }
        Assert-True -Condition ($bootstrap -match 'Assert-WinBridgeNoBroadPortRules') -Name 'broad firewall rules must be audited'
        Assert-True -Condition ($bootstrap -match 'Install-WinBridgeAuthorizedKey') -Name 'SSH key installation'
        Assert-True -Condition ($bootstrap -match 'Install-WinBridgePythonPackages') -Name 'hash-locked install'
        Assert-True -Condition ($bootstrap -match 'manifest\.json') -Name 'manifest required'
        Assert-True -Condition ($bootstrap -notmatch 'AutoAdminLogon|DefaultPassword|AutoLogonCount') -Name 'no autologon registry settings'
        $common = Get-Content -LiteralPath (Join-Path $bootstrapRoot 'lib\Common.ps1') -Raw
        Assert-True -Condition ($common.Contains('100.64.0.0/10')) -Name 'IPv4 Tailscale range'
        Assert-True -Condition ($common.Contains('fd7a:115c:a1e0::/48')) -Name 'IPv6 Tailscale range'
    }

    Test-Case 'lock and upstream metadata are pinned' {
        $lockPath = Join-Path $bootstrapRoot 'requirements\windows-py312.lock.txt'
        $lines = @(Get-Content -LiteralPath $lockPath)
        $pins = @($lines | Where-Object { $_ -match '^[A-Za-z0-9_.-]+==' })
        Assert-Equal -Expected 87 -Actual $pins.Count -Name 'locked package count'
        Assert-True -Condition (@($pins | Where-Object { $_ -match '^fastmcp==3\.' }).Count -eq 1) -Name 'fastmcp 3.x pin'
        $text = $lines -join "`n"
        Assert-True -Condition ($text -notmatch '(?i)winbridge|winremote-mcp') -Name 'lock must not contain the project'
        Assert-True -Condition ($text -match '--hash=sha256:') -Name 'hashes present'
        $upstream = Get-Content -LiteralPath (Join-Path $repoRoot 'upstream.json') -Raw | ConvertFrom-Json
        Assert-Equal -Expected '0.4.24' -Actual ([string]$upstream.base_version) -Name 'upstream base version'
        Assert-True -Condition ([string]$upstream.base_commit -match '^[0-9a-f]{40}$') -Name 'upstream commit shape'
        foreach ($artifact in @($upstream.artifacts.PSObject.Properties)) {
            Assert-True -Condition ([string]$artifact.Value.sha256 -match '^[0-9a-f]{64}$') -Name ('artifact hash ' + $artifact.Name)
        }
    }

    # The jobs installer copies the runner that then runs elevated: it must match the hash from the verified bundle.
    $jobsInstaller = Join-Path $bootstrapRoot 'jobs\Install-WinBridgeJobs.ps1'
    function Invoke-JobsInstallerWithRunner {
        param([string]$Name, [string]$RunnerText, [string]$ExpectedSha256)
        $dir = Join-Path $tempRoot ('jobs-install-' + $Name)
        New-Item -ItemType Directory -Path (Join-Path $dir 'packages') -Force | Out-Null
        Copy-Item -LiteralPath $jobsInstaller -Destination $dir
        Set-Content -LiteralPath (Join-Path $dir 'runner.ps1') -Value $RunnerText -Encoding ASCII
        $root = Join-Path $dir 'root'
        $message = ''
        try {
            & (Join-Path $dir 'Install-WinBridgeJobs.ps1') -Packages (Join-Path $dir 'packages') -Root $root -RunnerSha256 $ExpectedSha256 | Out-Null
        } catch { $message = $_.Exception.Message }
        return [pscustomobject]@{ Message = $message; RootCreated = (Test-Path -LiteralPath $root) }
    }

    Test-Case 'jobs install refuses a runner whose hash differs from the bundle' {
        $expected = (Get-FileHash -LiteralPath (Join-Path $bootstrapRoot 'jobs\runner.ps1') -Algorithm SHA256).Hash
        $r = Invoke-JobsInstallerWithRunner -Name 'bad' -RunnerText 'Write-Output tampered' -ExpectedSha256 $expected
        Assert-True ($r.Message -match 'runner hash mismatch') -Name 'error names the hash mismatch' -Detail $r.Message
        Assert-True (-not $r.RootCreated) -Name 'nothing is installed after a mismatch'
    }

    Test-Case 'jobs install compares the runner hash without regard to letter case' {
        $text = 'Write-Output genuine'
        $tmp = Join-Path $tempRoot 'genuine-runner-lc.ps1'
        Set-Content -LiteralPath $tmp -Value $text -Encoding ASCII
        $expected = (Get-FileHash -LiteralPath $tmp -Algorithm SHA256).Hash.ToLowerInvariant()
        $r = Invoke-JobsInstallerWithRunner -Name 'lower' -RunnerText $text -ExpectedSha256 $expected
        Assert-True ($r.Message -notmatch 'hash mismatch|parameter cannot be found|Cannot validate') -Name 'a lowercase hash is accepted' -Detail $r.Message
    }

    Test-Case 'jobs install accepts a runner whose hash matches' {
        $text = 'Write-Output genuine'
        $tmp = Join-Path $tempRoot 'genuine-runner.ps1'
        Set-Content -LiteralPath $tmp -Value $text -Encoding ASCII
        $expected = (Get-FileHash -LiteralPath $tmp -Algorithm SHA256).Hash
        $r = Invoke-JobsInstallerWithRunner -Name 'good' -RunnerText $text -ExpectedSha256 $expected
        Assert-True ($r.Message -notmatch 'hash mismatch|parameter cannot be found|Cannot validate') -Name 'a matching hash gets past the check' -Detail $r.Message
    }

    Test-Case 'no private key is committed with the bootstrap' {
        $forbidden = @(Get-ChildItem -LiteralPath $bootstrapRoot -Recurse -File | Where-Object {
            $_.Name -eq 'authorized_keys' -or
            $_.Extension -in @('.pem', '.key', '.pfx', '.p12')
        })
        Assert-Equal -Expected 0 -Actual $forbidden.Count -Name 'committed key files'
    }
} finally {
    $env:LOCALAPPDATA = $originalLocalAppData
    if (Test-Path -LiteralPath $tempRoot) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Write-Host ''
if ($failures.Count -gt 0) {
    Write-Host ('[test-bootstrap] ' + $passes + ' passed, ' + $failures.Count + ' failed.') -ForegroundColor Red
    foreach ($failure in $failures) {
        Write-Host ('  - ' + $failure) -ForegroundColor Red
    }
    exit 1
}

Write-Host ('[test-bootstrap] ' + $passes + ' checks passed.') -ForegroundColor Green
exit 0
