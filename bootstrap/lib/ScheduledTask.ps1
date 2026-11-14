# Logon task for the WinBridge supervisor.
# No autologon is configured. The task starts when the owner logs in and
# unlocks the session; it waits for pairing before starting the server.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgeCurrentUser {
    return [Security.Principal.WindowsIdentity]::GetCurrent().Name
}

function Register-WinBridgeTask {
    param([string]$SupervisorPath = (Get-WinBridgePath 'bin\supervisor.ps1'))
    Assert-WinBridgeAdmin
    if (-not (Test-Path -LiteralPath $SupervisorPath)) {
        throw ('Supervisor script not found: ' + $SupervisorPath)
    }
    $user = Get-WinBridgeCurrentUser
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $arguments = '-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $SupervisorPath + '"'
    $action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments -WorkingDirectory (Get-WinBridgeRoot)
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
    # A repeating trigger relaunches the supervisor if it was closed; MultipleInstances IgnoreNew prevents duplicates.
    $watchdog = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(2) `
        -RepetitionInterval (New-TimeSpan -Minutes 5) -RepetitionDuration (New-TimeSpan -Days 3650)
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -StartWhenAvailable `
        -MultipleInstances IgnoreNew `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit (New-TimeSpan -Days 0)
    Register-ScheduledTask `
        -TaskName $script:WinBridgeTaskName `
        -TaskPath $script:WinBridgeTaskPath `
        -Action $action `
        -Trigger @($trigger, $watchdog) `
        -Principal $principal `
        -Settings $settings `
        -Description 'WinBridge MCP supervisor (Tailscale-only support session).' `
        -Force | Out-Null
    Write-WinBridgeStep ('Scheduled task registered: ' + $script:WinBridgeTaskFullName)
}

function Start-WinBridgeTask {
    try {
        Start-ScheduledTask -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -ErrorAction Stop
        return $true
    } catch {
        Write-WinBridgeWarn ('Could not start the scheduled task now: ' + $_.Exception.Message)
        return $false
    }
}

function Stop-WinBridgeTask {
    try {
        Stop-ScheduledTask -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -ErrorAction Stop
        return $true
    } catch {
        return $false
    }
}

function Get-WinBridgeTaskSummary {
    $task = Get-ScheduledTask -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -ErrorAction SilentlyContinue
    if ($null -eq $task) {
        return [pscustomobject]@{
            Present = $false
            State = $null
            LastRunTime = $null
            LastTaskResult = $null
            NumberOfMissedRuns = $null
        }
    }
    $info = Get-ScheduledTaskInfo -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -ErrorAction SilentlyContinue
    return [pscustomobject]@{
        Present = $true
        State = [string]$task.State
        LastRunTime = $info.LastRunTime
        LastTaskResult = $info.LastTaskResult
        NumberOfMissedRuns = $info.NumberOfMissedRuns
    }
}

function Unregister-WinBridgeTask {
    Assert-WinBridgeAdmin
    $task = Get-ScheduledTask -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        Unregister-ScheduledTask -TaskName $script:WinBridgeTaskName -TaskPath $script:WinBridgeTaskPath -Confirm:$false
        Write-WinBridgeStep 'WinBridge scheduled task removed.'
    }
}
