# WinBridge supervisor.
# Runs in the owner's interactive session at logon. It waits for pairing,
# resolves the Tailscale address, starts the MCP server, and restarts it on
# crash, IP change, update or explicit restart. It never writes the auth key.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Tailscale.ps1')
. (Join-Path $binRoot 'lib\WinBridgeConfig.ps1')

$mutex = $null
$hasMutex = $false
try {
    $mutex = New-Object System.Threading.Mutex($false, 'Local\WinBridgeSupervisor')
    $hasMutex = $mutex.WaitOne(0)
} catch {
    $hasMutex = $false
}
if (-not $hasMutex) {
    Write-WinBridgeLog -Message 'Another WinBridge supervisor is already running.' -Name 'supervisor' -Level 'WARN'
    if ($null -ne $mutex) { $mutex.Dispose() }
    exit 0
}

$root = Get-WinBridgeRoot
$stateDir = Join-Path $root 'state'
$logDir = Join-Path $root 'logs'
$configPath = Join-Path $root 'config\winbridge.toml'
$venvPython = Join-Path $root 'app\venv\Scripts\python.exe'
$goAgent = Join-Path $root 'app\go\winbridge-agent.exe'
$agentFlag = Join-Path $stateDir 'agent.txt'
$supervisorPidPath = Join-Path $stateDir 'supervisor.pid'
$serverPidPath = Join-Path $stateDir 'server.pid'
$stopFlag = Join-Path $stateDir 'stop.flag'
$restartFlag = Join-Path $stateDir 'restart.flag'
$maintenanceFlag = Join-Path $stateDir 'maintenance.flag'
$stdoutLog = Join-Path $logDir 'server.out.log'
$stderrLog = Join-Path $logDir 'server.err.log'

Ensure-WinBridgeDirectory -Path $stateDir
Ensure-WinBridgeDirectory -Path $logDir

# Leaves a line in the log when the process ends, so an unexplained stop (0xC000013A once) can be dated.
Register-EngineEvent -SourceIdentifier PowerShell.Exiting -MessageData (Join-Path $logDir 'supervisor.log') -Action {
    Add-Content -LiteralPath $Event.MessageData -Value ((Get-Date).ToString('o') + ' [WARN] The supervisor process is exiting.')
} | Out-Null

function Set-WinBridgePidFile {
    param([string]$Path, [int]$ProcessId)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($Path, [string]$ProcessId, $encoding)
}

function Remove-WinBridgePidFile {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    }
}

# Returns $true when state\agent.txt selects the Go agent and its executable is installed.
function Test-WinBridgeUseGo {
    if (-not (Test-Path -LiteralPath $agentFlag)) { return $false }
    if (-not (Test-Path -LiteralPath $goAgent)) { return $false }
    return ((Get-Content -LiteralPath $agentFlag -Raw).Trim() -eq 'go')
}

# Selects the Python agent again by rewriting state\agent.txt; does nothing in a Go-only install, which has no venv.
function Set-WinBridgeAgentPython {
    if (-not (Test-Path -LiteralPath $venvPython)) { return }
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($agentFlag, 'python', $encoding)
}

$script:goHealthFailures = 0
$maxGoHealthFailures = 3

# Polls /health until it answers ok, the child exits, or the timeout passes.
function Wait-WinBridgeHealth {
    param($Process, [string]$Address, [int]$Port, [int]$TimeoutSeconds = 20)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($Process.HasExited) { return $false }
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri ('http://' + $Address + ':' + $Port + '/health') -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return $true }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    return $false
}

function Stop-WinBridgeChild {
    param($Process)
    if ($null -eq $Process) { return }
    try {
        if (-not $Process.HasExited) {
            Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
            $Process.WaitForExit(5000) | Out-Null
        }
    } catch {
        # The process may already have exited.
    }
}

$child = $null
$backoffSeconds = 5
try {
    Rotate-WinBridgeLog -Path (Join-Path $logDir 'supervisor.log')
    Set-WinBridgePidFile -Path $supervisorPidPath -ProcessId $PID
    Write-WinBridgeLog -Message ('Supervisor started (pid ' + $PID + ').') -Name 'supervisor' -Level 'INFO'
    if (Stop-WinBridgeStaleServer) {
        Write-WinBridgeLog -Message 'Removed a stale server process from a previous supervisor.' -Name 'supervisor' -Level 'WARN'
    }

    while (-not (Test-Path -LiteralPath $stopFlag)) {
        if (Test-Path -LiteralPath $maintenanceFlag) {
            Stop-WinBridgeChild -Process $child
            $child = $null
            Remove-WinBridgePidFile -Path $serverPidPath
            Start-Sleep -Seconds 2
            continue
        }

        # A restart request that arrives while no server runs is satisfied by the start below.
        Remove-Item -LiteralPath $restartFlag -Force -ErrorAction SilentlyContinue
        $useGo = Test-WinBridgeUseGo
        if (-not $useGo -and -not (Test-Path -LiteralPath $venvPython)) {
            Write-WinBridgeLog -Message 'Python environment is missing; waiting.' -Name 'supervisor' -Level 'ERROR'
            Start-Sleep -Seconds 15
            continue
        }
        if (-not (Test-Path -LiteralPath $configPath)) {
            Write-WinBridgeLog -Message 'Config is missing; waiting.' -Name 'supervisor' -Level 'ERROR'
            Start-Sleep -Seconds 15
            continue
        }

        try {
            if (Import-WinBridgePendingKey) {
                Write-WinBridgeLog -Message 'Stored the pending auth key in Credential Manager.' -Name 'supervisor' -Level 'INFO'
            }
        } catch {
            Write-WinBridgeLog -Message ('Could not import the pending key: ' + $_.Exception.Message) -Name 'supervisor' -Level 'ERROR'
        }
        $credential = Get-WinBridgeCredential
        if ($null -eq $credential) {
            Write-WinBridgeLog -Message 'Not paired yet; waiting for the Mac to store the auth key.' -Name 'supervisor' -Level 'INFO'
            Start-Sleep -Seconds 10
            continue
        }

        try {
            $key = Assert-WinBridgeKey -Key $credential.Secret
        } catch {
            Write-WinBridgeLog -Message ('Stored credential is invalid: ' + $_.Exception.Message) -Name 'supervisor' -Level 'ERROR'
            Start-Sleep -Seconds 15
            continue
        }

        $tailscaleIp = Get-WinBridgeTailscaleIPv4
        if (-not $tailscaleIp) {
            Write-WinBridgeLog -Message 'Tailscale is not ready; waiting.' -Name 'supervisor' -Level 'WARN'
            Start-WinBridgeTailscaleGui | Out-Null
            Start-Sleep -Seconds 10
            continue
        }

        $port = Get-WinBridgePort
        Rotate-WinBridgeLog -Path $stdoutLog
        Rotate-WinBridgeLog -Path $stderrLog
        $fingerprint = Get-WinBridgeKeyFingerprint -Secret $key
        $agentName = if ($useGo) { 'go' } else { 'python' }
        Write-WinBridgeLog -Message ('Starting ' + $agentName + ' server on ' + $tailscaleIp + ':' + $port + ' (key ' + $fingerprint + ').') -Name 'supervisor' -Level 'INFO'

        if ($useGo) {
            $serverExe = $goAgent
            $arguments = @(
                '--host', (Quote-WinBridgeArg $tailscaleIp),
                '--port', [string]$port,
                '--config', (Quote-WinBridgeArg $configPath)
            ) -join ' '
        } else {
            $serverExe = $venvPython
            $arguments = @(
                '-m', 'winremote',
                '--transport', 'streamable-http',
                '--host', (Quote-WinBridgeArg $tailscaleIp),
                '--port', [string]$port,
                '--config', (Quote-WinBridgeArg $configPath)
            ) -join ' '
        }

        $env:WINREMOTE_AUTH_KEY = $key
        $env:WINBRIDGE_STRICT = '1'
        $env:PYTHONUTF8 = '1'
        try {
            $child = Start-Process `
                -FilePath $serverExe `
                -ArgumentList $arguments `
                -WorkingDirectory $root `
                -PassThru `
                -NoNewWindow `
                -RedirectStandardOutput $stdoutLog `
                -RedirectStandardError $stderrLog
            # Reading the handle keeps ExitCode available after the child exits; without it the log shows an empty code.
            $null = $child.Handle
        } catch {
            if (-not $useGo) { throw }
            Write-WinBridgeLog -Message ('The Go agent could not start (' + $_.Exception.Message + '); going back to the Python agent.') -Name 'supervisor' -Level 'ERROR'
            Set-WinBridgeAgentPython
            $child = $null
        } finally {
            Remove-Item Env:\WINREMOTE_AUTH_KEY -ErrorAction SilentlyContinue
            Remove-Item Env:\WINBRIDGE_STRICT -ErrorAction SilentlyContinue
            Remove-Item Env:\PYTHONUTF8 -ErrorAction SilentlyContinue
        }
        if ($null -eq $child) { Start-Sleep -Seconds 5; continue }
        Set-WinBridgePidFile -Path $serverPidPath -ProcessId $child.Id
        $startedAt = Get-Date
        $boundIp = $tailscaleIp
        $lastIpCheck = Get-Date
        Write-WinBridgeLog -Message ('Server started (pid ' + $child.Id + ').') -Name 'supervisor' -Level 'INFO'

        if ($useGo -and -not (Wait-WinBridgeHealth -Process $child -Address $tailscaleIp -Port $port)) {
            # A Go agent that exits right after a reboot (the Tailscale address is not bound yet) is retried before the
            # switch to Python, which is remembered across restarts.
            $script:goHealthFailures++
            if ($script:goHealthFailures -ge $maxGoHealthFailures) {
                Write-WinBridgeLog -Message ('The Go agent did not answer /health ' + $script:goHealthFailures + ' times; going back to the Python agent.') -Name 'supervisor' -Level 'ERROR'
                Set-WinBridgeAgentPython
            } else {
                Write-WinBridgeLog -Message ('The Go agent did not answer /health (attempt ' + $script:goHealthFailures + ' of ' + $maxGoHealthFailures + '); trying again.') -Name 'supervisor' -Level 'WARN'
            }
            Stop-WinBridgeChild -Process $child
            Remove-WinBridgePidFile -Path $serverPidPath
            $child = $null
            Start-Sleep -Seconds 10
            continue
        }
        $script:goHealthFailures = 0

        while (-not $child.HasExited) {
            if (Test-Path -LiteralPath $stopFlag) {
                Stop-WinBridgeChild -Process $child
                break
            }
            if (Test-Path -LiteralPath $maintenanceFlag) {
                Stop-WinBridgeChild -Process $child
                break
            }
            if (Test-Path -LiteralPath $restartFlag) {
                Remove-Item -LiteralPath $restartFlag -Force -ErrorAction SilentlyContinue
                Stop-WinBridgeChild -Process $child
                break
            }
            if (((Get-Date) - $lastIpCheck).TotalSeconds -ge 30) {
                $lastIpCheck = Get-Date
                $currentIp = Get-WinBridgeTailscaleIPv4
                if ($currentIp -and $currentIp -ne $boundIp) {
                    Write-WinBridgeLog -Message ('Tailscale address changed from ' + $boundIp + ' to ' + $currentIp + '; restarting server.') -Name 'supervisor' -Level 'WARN'
                    try {
                        $state = Get-WinBridgeState
                        if ($null -eq $state) { $state = Get-WinBridgeDefaultState }
                        Set-WinBridgeProperty -Object $state -Name 'tailscale_ip' -Value $currentIp
                        Save-WinBridgeState -State $state
                        Write-WinBridgeConfig -State $state | Out-Null
                    } catch {
                        Write-WinBridgeLog -Message ('Could not refresh config for the new Tailscale address: ' + $_.Exception.Message) -Name 'supervisor' -Level 'ERROR'
                    }
                    Stop-WinBridgeChild -Process $child
                    break
                }
            }
            Start-Sleep -Seconds 2
        }

        $exitCode = $null
        try {
            if ($child.HasExited) { $exitCode = $child.ExitCode }
        } catch {
            $exitCode = $null
        }
        Remove-WinBridgePidFile -Path $serverPidPath
        $child = $null

        if (Test-Path -LiteralPath $stopFlag) { break }
        if (Test-Path -LiteralPath $maintenanceFlag) { continue }

        if (((Get-Date) - $startedAt).TotalSeconds -gt 60) {
            $backoffSeconds = 5
        }
        Write-WinBridgeLog -Message ('Server exited (code ' + $exitCode + '); restarting in ' + $backoffSeconds + 's.') -Name 'supervisor' -Level 'WARN'
        Start-Sleep -Seconds $backoffSeconds
        $backoffSeconds = [Math]::Min($backoffSeconds * 2, 60)
    }
} catch {
    Write-WinBridgeLog -Message ('Supervisor fatal error: ' + $_.Exception.Message) -Name 'supervisor' -Level 'ERROR'
    throw
} finally {
    Stop-WinBridgeChild -Process $child
    Remove-WinBridgePidFile -Path $serverPidPath
    Remove-WinBridgePidFile -Path $supervisorPidPath
    Remove-Item Env:\WINREMOTE_AUTH_KEY -ErrorAction SilentlyContinue
    Remove-Item Env:\WINBRIDGE_STRICT -ErrorAction SilentlyContinue
    Remove-Item Env:\PYTHONUTF8 -ErrorAction SilentlyContinue
    try {
        $mutex.ReleaseMutex()
    } catch {
        # Nothing to release.
    }
    if ($null -ne $mutex) { $mutex.Dispose() }
    Write-WinBridgeLog -Message 'Supervisor stopped.' -Name 'supervisor' -Level 'INFO'
}
