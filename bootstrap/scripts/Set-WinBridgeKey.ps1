# Store the WinBridge auth key in Windows Credential Manager and verify the
# running profile. The key is read from stdin only: never from argv or a file.

param(
    [string]$ClientIp,
    [int]$TimeoutSeconds = 90,
    [switch]$Json
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$binRoot = $PSScriptRoot
. (Join-Path $binRoot 'lib\Common.ps1')
. (Join-Path $binRoot 'lib\CredentialManager.ps1')
. (Join-Path $binRoot 'lib\Tailscale.ps1')
. (Join-Path $binRoot 'lib\WinBridgeConfig.ps1')
. (Join-Path $binRoot 'lib\ScheduledTask.ps1')

if ($Json) { Set-WinBridgeConsoleQuiet -Quiet $true }

function Write-WinBridgeResult {
    param($Value)
    if ($Json) {
        Write-Output ($Value | ConvertTo-Json -Depth 8 -Compress)
    } else {
        Write-Output $Value
    }
}

$previousCredential = $null
$keyStored = $false
try {
    $rawKey = [Console]::In.ReadLine()
    $key = Assert-WinBridgeKey -Key $rawKey

    $state = Get-WinBridgeState
    if ($null -eq $state) {
        $state = Get-WinBridgeDefaultState
    }
    if ($ClientIp) {
        $normalized = Add-WinBridgeClient -State $state -Client $ClientIp
        Save-WinBridgeState -State $state
        Write-WinBridgeStep ('Client allowlist includes ' + $normalized + '.')
    }
    $tailscaleIp = Get-WinBridgeBindAddress -State $state
    Set-WinBridgeProperty -Object $state -Name 'tailscale_ip' -Value $tailscaleIp
    Save-WinBridgeState -State $state
    Write-WinBridgeConfig -State $state | Out-Null

    $fingerprint = Get-WinBridgeKeyFingerprint -Secret $key
    try {
        $previousCredential = Get-WinBridgeCredential
        Set-WinBridgeCredential -Secret $key
        $keyStored = $true
        Write-WinBridgeStep ('Auth key stored in Credential Manager (fingerprint ' + $fingerprint + ').')
    } catch {
        # An SSH session has no Credential Manager; hand the key to the interactive supervisor instead.
        Set-WinBridgePendingKey -Secret $key
        Write-WinBridgeStep ('Auth key handed to the supervisor (fingerprint ' + $fingerprint + '); it stores it in Credential Manager.')
    }

    # The supervisor only reads the credential when it (re)starts the server,
    # so force one restart to make the new key effective immediately.
    $restartFlag = Get-WinBridgePath 'state\restart.flag'
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $restartFlag)
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($restartFlag, (Get-Date).ToString('o'), $encoding)
    Start-WinBridgeTask | Out-Null

    $port = Get-WinBridgePort
    $health = Wait-WinBridgeHttp -Uri ('http://' + (Format-WinBridgeHttpHost -Address $tailscaleIp) + ':' + $port + '/health') -TimeoutSeconds $TimeoutSeconds
    if ($health.status -ne 'ok') {
        throw 'The WinBridge health endpoint did not report ok.'
    }
    $status = Get-WinBridgeStatus -Address $tailscaleIp -Key $key -Port $port
    if ($status.profile -ne 'winbridge' -or -not $status.strict) {
        throw 'The server is not running the WinBridge strict profile.'
    }
    $expectedTools = @(
        'Snapshot', 'Click', 'Type', 'Move', 'Scroll',
        'Shortcut', 'FocusWindow', 'Wait', 'GetSystemInfo'
    )
    $reportedTools = @($status.tools)
    if ([int]$status.tools_count -ne 9 -or $reportedTools.Count -ne 9) {
        throw ('Expected 9 profile tools (built-in diagnostics tools are not counted), server reported ' + $status.tools_count + '.')
    }
    foreach ($expected in $expectedTools) {
        if ($reportedTools -notcontains $expected) {
            throw ('The running profile is missing tool ' + $expected + '.')
        }
    }
    if ([string]$status.version -ne [string]$health.version) {
        throw 'The server version is inconsistent between /health and /status.'
    }
    if ([string]$status.upstream -notin @('0.4.24', 'native-go')) {
        throw ('Unexpected upstream base: ' + $status.upstream + '.')
    }

    Write-WinBridgeResult ([pscustomobject]@{
        ok = $true
        version = [string]$status.version
        upstream = [string]$status.upstream
        profile = [string]$status.profile
        fingerprint = $fingerprint
        tailscale_ip = $tailscaleIp
        port = $port
        tools = @($reportedTools | Sort-Object)
        tools_count = [int]$status.tools_count
        client_ip = $ClientIp
    })
} catch {
    $message = $_.Exception.Message
    if ($keyStored) {
        try {
            if ($null -ne $previousCredential -and $previousCredential.Secret) {
                Set-WinBridgeCredential -Secret $previousCredential.Secret
                $restartFlag = Get-WinBridgePath 'state\restart.flag'
                Ensure-WinBridgeDirectory -Path (Split-Path -Parent $restartFlag)
                $encoding = New-Object System.Text.UTF8Encoding($false)
                [IO.File]::WriteAllText($restartFlag, (Get-Date).ToString('o'), $encoding)
                Start-WinBridgeTask | Out-Null
                Write-WinBridgeWarn 'Pairing failed; the previous auth key was restored.'
            } else {
                Remove-WinBridgeCredential | Out-Null
            }
        } catch {
            Write-WinBridgeWarn ('Could not restore the previous credential: ' + $_.Exception.Message)
        }
    }
    Write-WinBridgeError $message
    if ($Json) {
        Write-Output ((@{ ok = $false; error = $message } | ConvertTo-Json -Compress))
    }
    exit 1
}
