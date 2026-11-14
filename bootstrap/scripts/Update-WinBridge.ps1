# Install a reviewed WinBridge wheel with SHA-256 verification and rollback.
# Runs in the owner's session context over SSH; no elevation is required.

param(
    [Parameter(Mandatory = $true)][string]$WheelPath,
    [Parameter(Mandatory = $true)][string]$Sha256,
    [string]$ExpectedVersion,
    [int]$TimeoutSeconds = 120,
    [switch]$Json
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Tailscale.ps1')
. (Join-Path $binRoot 'lib\WinBridgeConfig.ps1')
. (Join-Path $binRoot 'lib\PythonRuntime.ps1')
. (Join-Path $binRoot 'lib\ScheduledTask.ps1')

if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

function Get-WinBridgeInstalledWheel {
    $packages = Get-WinBridgePath 'packages'
    if (-not (Test-Path -LiteralPath $packages)) { return $null }
    $wheel = @(Get-ChildItem -LiteralPath $packages -Filter 'winbridge_mcp-*.whl' -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
    if ($wheel.Count -eq 0) { return $null }
    return $wheel[0].FullName
}

function Wait-WinBridgeServerStopped {
    param([int]$TimeoutSeconds = 60)
    $pidPath = Get-WinBridgePath 'state\server.pid'
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $process = Get-WinBridgeProcessFromPidFile -Path $pidPath
        if ($null -eq $process) { return $true }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    return $false
}

function Restart-WinBridgeForUpdate {
    $flag = Get-WinBridgePath 'state\restart.flag'
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $flag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($flag, (Get-Date).ToString('o'), $encoding)
    Start-WinBridgeTask | Out-Null
}

$maintenanceFlag = Get-WinBridgePath 'state\maintenance.flag'
$previousWheel = $null
$result = [pscustomobject]@{
    ok = $false
    version = $null
    previous_version = $null
    sha256 = $Sha256.ToLowerInvariant()
    rolled_back = $false
    error = $null
}

try {
    $wheel = (Resolve-Path -LiteralPath $WheelPath -ErrorAction Stop).Path
    Test-WinBridgeWheelHash -Path $wheel -ExpectedSha256 $Sha256
    $previousVersion = Get-WinBridgeInstalledVersion
    $previousWheel = Get-WinBridgeInstalledWheel
    $result.previous_version = $previousVersion

    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $maintenanceFlag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($maintenanceFlag, (Get-Date).ToString('o'), $encoding)
    if (-not (Wait-WinBridgeServerStopped -TimeoutSeconds 60)) {
        throw 'The server did not stop for maintenance.'
    }

    $venvPython = Get-WinBridgePath 'app\venv\Scripts\python.exe'
    if (-not (Test-Path -LiteralPath $venvPython)) {
        throw 'The WinBridge Python environment is missing.'
    }
    Install-WinBridgeWheel -VenvPython $venvPython -WheelPath $wheel
    $newVersion = Get-WinBridgePackageVersion -VenvPython $venvPython
    if (-not $newVersion) {
        throw 'The updated package version could not be read.'
    }
    if ($ExpectedVersion -and $newVersion -ne $ExpectedVersion) {
        throw ('Expected version ' + $ExpectedVersion + ' but installed ' + $newVersion + '.')
    }
    $result.version = $newVersion

    if (Test-Path -LiteralPath $maintenanceFlag) {
        Remove-Item -LiteralPath $maintenanceFlag -Force
    }
    Start-WinBridgeTask | Out-Null

    $tailscaleIp = Get-WinBridgeTailscaleIPv4
    if (-not $tailscaleIp) {
        throw 'Tailscale is not connected; cannot verify the updated server.'
    }
    $port = Get-WinBridgePort
    $health = Wait-WinBridgeHttp -Uri ('http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $port + '/health') -TimeoutSeconds $TimeoutSeconds
    if ([string]$health.version -ne [string]$newVersion) {
        throw ('Health reports ' + $health.version + ' but installed ' + $newVersion + '.')
    }
    if ([string]$health.profile -ne 'winbridge') {
        throw ('Health reports profile ' + $health.profile + ' instead of winbridge.')
    }
    $credential = Get-WinBridgeCredential
    if ($null -ne $credential) {
        $status = Get-WinBridgeStatus -Address $tailscaleIp -Key $credential.Secret -Port $port
        if ($status.profile -ne 'winbridge' -or -not $status.strict -or [int]$status.tools_count -ne 9) {
            throw 'The updated server is not running the strict 9-tool profile.'
        }
    }

    Ensure-WinBridgeDirectory -Path (Get-WinBridgePath 'packages')
    Copy-Item -LiteralPath $wheel -Destination (Get-WinBridgePath 'packages') -Force
    $state = Get-WinBridgeState
    if ($null -eq $state) { $state = Get-WinBridgeDefaultState }
    Set-WinBridgeProperty -Object $state -Name 'installed_version' -Value $newVersion
    Save-WinBridgeState -State $state

    $result.ok = $true
    if ($Json) {
        Write-Output ($result | ConvertTo-Json -Compress)
    } else {
        Write-Output $result
    }
} catch {
    $message = $_.Exception.Message
    $result.error = $message
    Write-WinBridgeError $message
    if ($previousWheel -and (Test-Path -LiteralPath $previousWheel)) {
        try {
            $venvPython = Get-WinBridgePath 'app\venv\Scripts\python.exe'
            Install-WinBridgeWheel -VenvPython $venvPython -WheelPath $previousWheel
            $result.rolled_back = $true
            Write-WinBridgeWarn 'Rolled back to the previous wheel.'
        } catch {
            Write-WinBridgeError ('Rollback failed: ' + $_.Exception.Message)
        }
    }
    if (Test-Path -LiteralPath $maintenanceFlag) {
        Remove-Item -LiteralPath $maintenanceFlag -Force -ErrorAction SilentlyContinue
    }
    try {
        Restart-WinBridgeForUpdate
    } catch {
        Write-WinBridgeWarn ('Could not restart after the failed update: ' + $_.Exception.Message)
    }
    if ($Json) {
        Write-Output ($result | ConvertTo-Json -Compress)
    }
    exit 1
}
