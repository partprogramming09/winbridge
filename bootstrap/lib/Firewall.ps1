# Windows Firewall rules for WinBridge.
# Only Tailscale source ranges are allowed. Windows Firewall and Defender are
# never disabled, and unrelated third-party rules are never modified.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgeTailscaleRemoteAddresses {
    return @($script:WinBridgeTailscaleV4, $script:WinBridgeTailscaleV6)
}

function ConvertTo-WinBridgeCidr {
    param([Parameter(Mandatory = $true)][string]$Entry)
    # Windows Firewall reports 100.64.0.0/10 as 100.64.0.0/255.192.0.0.
    if ($Entry -match '^(\d{1,3}(?:\.\d{1,3}){3})/(\d{1,3}(?:\.\d{1,3}){3})$') {
        $bits = 0
        foreach ($octet in ($Matches[2] -split '\.')) {
            $value = [int]$octet
            while ($value -gt 0) { $bits += ($value -band 1); $value = $value -shr 1 }
        }
        return ($Matches[1] + '/' + $bits)
    }
    return $Entry
}

function Ensure-WinBridgeFirewallRule {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][int]$Port
    )
    Assert-WinBridgeAdmin
    $existing = @(Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue)
    foreach ($rule in $existing) {
        Remove-NetFirewallRule -Name $rule.Name -ErrorAction SilentlyContinue
    }
    $remote = Get-WinBridgeTailscaleRemoteAddresses
    New-NetFirewallRule `
        -DisplayName $Name `
        -Group $script:WinBridgeFirewallGroup `
        -Description 'WinBridge: inbound from Tailscale ranges only.' `
        -Direction Inbound `
        -Action Allow `
        -Protocol TCP `
        -LocalPort $Port `
        -RemoteAddress $remote `
        -Profile Any `
        -Enabled True | Out-Null
    Write-WinBridgeStep ('Firewall rule ready: ' + $Name + ' TCP ' + $Port + ' from Tailscale ranges.')
}

function Disable-WinBridgeBuiltinOpenSshRule {
    # Windows creates OpenSSH-Server-In-TCP with RemoteAddress=Any when the
    # OpenSSH Server capability is installed. WinBridge replaces it with a
    # Tailscale-only rule. Only this known built-in rule is touched.
    Assert-WinBridgeAdmin
    $rules = @(Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue)
    $changed = @()
    foreach ($rule in $rules) {
        if ([string]$rule.Enabled -eq 'True') {
            Disable-NetFirewallRule -Name $rule.Name -ErrorAction Stop
            $changed += [string]$rule.Name
        }
    }
    if ($changed.Count -gt 0) {
        Write-WinBridgeWarn 'Disabled the built-in broad OpenSSH firewall rule; WinBridge-SSH-Tailscale replaces it.'
    }
    return $changed
}

function Test-WinBridgeBroadRemoteEntry {
    param([AllowNull()][string]$Entry)
    if ([string]::IsNullOrWhiteSpace($Entry)) { return $false }
    $value = ConvertTo-WinBridgeCidr -Entry $Entry.Trim()
    return ($value -in @(
            'Any',
            '*',
            '0.0.0.0/0',
            '::/0',
            'LocalSubnet',
            'Internet',
            'Intranet',
            'DNS',
            'DHCP',
            'WINS',
            'DefaultGateway',
            'PlayToDevice',
            'System'
        ))
}

# Reads every firewall filter once, indexed by rule id. Asking each rule for its four filters one by one takes about a
# minute on a PC with a few hundred rules; the bulk calls take well under a second and return the same objects.
function Get-WinBridgeFirewallFilterIndex {
    $index = @{ Port = @{}; Application = @{}; Service = @{}; Address = @{} }
    foreach ($filter in @(Get-NetFirewallPortFilter -All)) { $index.Port[[string]$filter.InstanceID] = $filter }
    foreach ($filter in @(Get-NetFirewallApplicationFilter -All)) { $index.Application[[string]$filter.InstanceID] = $filter }
    foreach ($filter in @(Get-NetFirewallServiceFilter -All)) { $index.Service[[string]$filter.InstanceID] = $filter }
    foreach ($filter in @(Get-NetFirewallAddressFilter -All)) { $index.Address[[string]$filter.InstanceID] = $filter }
    return $index
}

function Get-WinBridgeBroadPortRules {
    param(
        [Parameter(Mandatory = $true)][int]$Port,
        [scriptblock]$RuleReader = { Get-NetFirewallRule -Direction Inbound -Action Allow -Enabled True -ErrorAction SilentlyContinue },
        [scriptblock]$FilterReader = { Get-WinBridgeFirewallFilterIndex }
    )
    $result = @()
    $rules = @(& $RuleReader | Where-Object {
            # WinBridge's own rules, and the built-in OpenSSH one (Disable-WinBridgeBuiltinOpenSshRule handles it).
            $_.DisplayName -ne $script:WinBridgeSshRule -and $_.DisplayName -ne $script:WinBridgeMcpRule -and
            [string]$_.Name -ne 'OpenSSH-Server-In-TCP'
        })
    if ($rules.Count -eq 0) { return $result }
    # Read outside the per-rule try: if the filters cannot be read, the audit must fail, not pass with every rule skipped.
    $filters = & $FilterReader
    if ($null -eq $filters -or $filters.Port.Count -eq 0) {
        throw 'the firewall filters could not be read; refusing to report the rules as safe'
    }
    foreach ($rule in $rules) {
        try {
            $id = [string]$rule.InstanceID
            $portFilter = $filters.Port[$id]
            $applicationFilter = $filters.Application[$id]
            $serviceFilter = $filters.Service[$id]
            $addressFilter = $filters.Address[$id]
            if ($null -eq $portFilter -or $null -eq $applicationFilter -or $null -eq $serviceFilter -or $null -eq $addressFilter) {
                throw 'its filters are not in the firewall store'
            }
            $protocol = [string]$portFilter.Protocol
            if ($protocol -notin @('TCP', '6')) { continue }
            $localPorts = @($portFilter.LocalPort | ForEach-Object { [string]$_ })
            if (($localPorts -notcontains [string]$Port) -and ($localPorts -notcontains 'Any')) { continue }
            # A rule tied to another program or service only admits that program's sockets, not sshd's.
            $program = [string]$applicationFilter.Program
            $service = [string]$serviceFilter.Service
            $otherProgram = $program -and ($program -ne 'Any') -and ($program -notmatch 'sshd\.exe$')
            $otherService = $service -and ($service -ne 'Any') -and ($service -ne 'sshd')
            if ($otherProgram -or $otherService) { continue }
            $remote = @($addressFilter.RemoteAddress | ForEach-Object { [string]$_ })
            $broad = @($remote | Where-Object { Test-WinBridgeBroadRemoteEntry -Entry $_ })
            if ($broad.Count -gt 0) {
                $result += [pscustomobject]@{
                    Name = [string]$rule.Name
                    DisplayName = [string]$rule.DisplayName
                    RemoteAddress = $remote
                    LocalPort = $localPorts
                }
            }
        } catch {
            Write-WinBridgeWarn ('Could not inspect firewall rule ' + $rule.DisplayName + ': ' + $_.Exception.Message)
        }
    }
    return $result
}

function Assert-WinBridgeNoBroadPortRules {
    param([Parameter(Mandatory = $true)][int]$Port)
    $broad = @(Get-WinBridgeBroadPortRules -Port $Port)
    if ($broad.Count -gt 0) {
        $names = ($broad | ForEach-Object { $_.DisplayName }) -join '; '
        throw (
            'Refusing to continue: inbound TCP port ' + $Port +
            ' is open to non-Tailscale sources by existing firewall rule(s): ' + $names +
            '. Disable or narrow those rules, then run the bootstrap again.'
        )
    }
}

function Test-WinBridgeFirewallRule {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][int]$Port
    )
    $rule = @(Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue)
    if ($rule.Count -eq 0) {
        return [pscustomobject]@{ Ok = $false; Detail = 'rule is missing' }
    }
    if ($rule.Count -gt 1) {
        return [pscustomobject]@{ Ok = $false; Detail = 'duplicate rules exist' }
    }
    $item = $rule[0]
    if ([string]$item.Enabled -ne 'True') {
        return [pscustomobject]@{ Ok = $false; Detail = 'rule is disabled' }
    }
    if ([string]$item.Direction -ne 'Inbound' -or [string]$item.Action -ne 'Allow') {
        return [pscustomobject]@{ Ok = $false; Detail = 'rule is not inbound allow' }
    }
    $portFilter = $item | Get-NetFirewallPortFilter
    $localPorts = @($portFilter.LocalPort | ForEach-Object { [string]$_ })
    if ($localPorts -notcontains [string]$Port) {
        return [pscustomobject]@{ Ok = $false; Detail = ('local port is ' + ($localPorts -join ', ')) }
    }
    $addressFilter = $item | Get-NetFirewallAddressFilter
    $remote = @($addressFilter.RemoteAddress | ForEach-Object { [string]$_ })
    $allowed = Get-WinBridgeTailscaleRemoteAddresses
    foreach ($entry in $remote) {
        if ($allowed -notcontains (ConvertTo-WinBridgeCidr -Entry $entry)) {
            return [pscustomobject]@{ Ok = $false; Detail = ('remote address is not Tailscale-only: ' + $entry) }
        }
    }
    return [pscustomobject]@{ Ok = $true; Detail = ('TCP ' + $Port + ' from ' + ($remote -join ', ')) }
}

function Test-WinBridgeFirewall {
    param(
        [int]$SshPort = 22,
        [int]$McpPort = 8091
    )
    $checks = @()
    $ssh = Test-WinBridgeFirewallRule -Name $script:WinBridgeSshRule -Port $SshPort
    $checks += [pscustomobject]@{ Name = 'firewall_ssh'; Ok = $ssh.Ok; Warn = $false; Detail = $ssh.Detail }
    $mcp = Test-WinBridgeFirewallRule -Name $script:WinBridgeMcpRule -Port $McpPort
    $checks += [pscustomobject]@{ Name = 'firewall_mcp'; Ok = $mcp.Ok; Warn = $false; Detail = $mcp.Detail }

    $builtin = @(Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue | Where-Object { [string]$_.Enabled -eq 'True' })
    $checks += [pscustomobject]@{
        Name = 'firewall_openssh_builtin'
        Ok = ($builtin.Count -eq 0)
        Warn = $false
        Detail = if ($builtin.Count -eq 0) { 'disabled' } else { 'OpenSSH-Server-In-TCP is still enabled' }
    }

    foreach ($port in @($SshPort, $McpPort)) {
        try {
            $broad = @(Get-WinBridgeBroadPortRules -Port $port)
            $checks += [pscustomobject]@{
                Name = ('firewall_port_' + $port + '_broad')
                Ok = ($broad.Count -eq 0)
                Warn = $false
                Detail = if ($broad.Count -eq 0) { 'no broad inbound rules' } else { (($broad | ForEach-Object { $_.DisplayName }) -join '; ') }
            }
        } catch {
            $checks += [pscustomobject]@{
                Name = ('firewall_port_' + $port + '_broad')
                Ok = $false
                Warn = $true
                Detail = ('could not inspect rules: ' + $_.Exception.Message)
            }
        }
    }
    return $checks
}

function Remove-WinBridgeFirewallRules {
    Assert-WinBridgeAdmin
    foreach ($name in @($script:WinBridgeSshRule, $script:WinBridgeMcpRule)) {
        $rules = @(Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue)
        foreach ($rule in $rules) {
            Remove-NetFirewallRule -Name $rule.Name -ErrorAction SilentlyContinue
        }
    }
    Write-WinBridgeStep 'WinBridge firewall rules removed.'
}
