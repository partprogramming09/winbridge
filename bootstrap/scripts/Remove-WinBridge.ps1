# Remove the WinBridge task, firewall rules and optionally local state.
# Tailscale, OpenSSH and the owner's SSH authorized key are not removed.

param(
    [switch]$Purge,
    [switch]$KeepCredential,
    [switch]$RemoveFirewall,
    [switch]$RemoveSshConfig,
    [switch]$Json
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Firewall.ps1')
. (Join-Path $binRoot 'lib\OpenSsh.ps1')
. (Join-Path $binRoot 'lib\ScheduledTask.ps1')

if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

try {
    Assert-WinBridgeAdmin
    $root = Get-WinBridgeRoot
    $stopFlag = Join-Path $root 'state\stop.flag'
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $stopFlag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($stopFlag, (Get-Date).ToString('o'), $encoding)

    Unregister-WinBridgeTask
    Stop-WinBridgeTask | Out-Null
    $supervisorPid = Join-Path $root 'state\supervisor.pid'
    $deadline = (Get-Date).AddSeconds(20)
    while ((Test-Path -LiteralPath $supervisorPid) -and ((Get-Date) -lt $deadline)) {
        Start-Sleep -Milliseconds 500
    }
    Stop-WinBridgeStaleServer | Out-Null
    $supervisor = Get-WinBridgeProcessFromPidFile -Path $supervisorPid
    if ($null -ne $supervisor) {
        try {
            Stop-Process -Id $supervisor.ProcessId -Force -ErrorAction Stop
            Write-WinBridgeWarn ('Stopped supervisor process ' + $supervisor.ProcessId + '.')
        } catch {
            Write-WinBridgeWarn ('Could not stop supervisor process: ' + $_.Exception.Message)
        }
    }

    if (-not $KeepCredential) {
        try {
            Remove-WinBridgeCredential | Out-Null
            Write-WinBridgeStep 'WinBridge credential removed from Credential Manager.'
        } catch {
            Write-WinBridgeWarn ('Could not remove the Credential Manager entry: ' + $_.Exception.Message)
        }
    }

    if ($RemoveFirewall) {
        Remove-WinBridgeFirewallRules
    }

    if ($RemoveSshConfig) {
        $configPath = Join-Path $env:ProgramData 'ssh\sshd_config'
        $includePath = Join-Path $env:ProgramData 'ssh\winbridge_sshd.conf'
        if (Test-Path -LiteralPath $configPath) {
            $lines = New-Object System.Collections.Generic.List[string]
            $inBlock = $false
            foreach ($line in @(Get-Content -LiteralPath $configPath)) {
                $trimmed = $line.Trim()
                if ($trimmed -eq '# BEGIN WINBRIDGE') { $inBlock = $true; continue }
                if ($trimmed -eq '# END WINBRIDGE') { $inBlock = $false; continue }
                if ($inBlock -or $trimmed -like 'Include*winbridge_sshd.conf') { continue }
                $lines.Add($line)
            }
            [IO.File]::WriteAllLines($configPath, @($lines), $encoding)
            $sshd = Get-WinBridgeSshdExe
            if ($sshd) {
                & $sshd -t 2>&1 | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw 'sshd_config validation failed after removing the WinBridge include.'
                }
                Restart-Service -Name sshd
            }
        }
        if (Test-Path -LiteralPath $includePath) {
            Remove-Item -LiteralPath $includePath -Force
        }
    }

    if ($Purge) {
        if (Test-Path -LiteralPath $root) {
            Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    $result = [pscustomobject]@{
        ok = $true
        purged = [bool]$Purge
        credential_removed = (-not $KeepCredential)
        firewall_removed = [bool]$RemoveFirewall
        ssh_config_removed = [bool]$RemoveSshConfig
    }
    if ($Json) {
        Write-Output ($result | ConvertTo-Json -Compress)
    } else {
        Write-Output $result
    }
} catch {
    $message = $_.Exception.Message
    Write-WinBridgeError $message
    if ($Json) {
        Write-Output ((@{ ok = $false; error = $message } | ConvertTo-Json -Compress))
    }
    exit 1
}
