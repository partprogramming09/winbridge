# Tailscale helpers for the WinBridge bootstrap.
# Tailscale is the only network path used by SSH and the MCP listener.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgeTailscaleExe {
    $candidates = @()
    if ($env:ProgramFiles) {
        $candidates += (Join-Path $env:ProgramFiles 'Tailscale\tailscale.exe')
    }
    if (${env:ProgramFiles(x86)}) {
        $candidates += (Join-Path ${env:ProgramFiles(x86)} 'Tailscale\tailscale.exe')
    }
    if ($env:LOCALAPPDATA) {
        $candidates += (Join-Path $env:LOCALAPPDATA 'Tailscale\tailscale.exe')
    }
    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { return $candidate }
    }
    $command = Get-Command tailscale.exe -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    return $null
}

function Get-WinBridgeTailscaleGuiExe {
    $candidates = @()
    if ($env:ProgramFiles) {
        $candidates += (Join-Path $env:ProgramFiles 'Tailscale\tailscale-ipn.exe')
    }
    if (${env:ProgramFiles(x86)}) {
        $candidates += (Join-Path ${env:ProgramFiles(x86)} 'Tailscale\tailscale-ipn.exe')
    }
    foreach ($candidate in $candidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate)) { return $candidate }
    }
    return $null
}

function Get-WinBridgeTailscaleStatus {
    $exe = Get-WinBridgeTailscaleExe
    if (-not $exe) { return $null }
    try {
        $raw = & $exe status --json 2>$null
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($raw -join ''))) { return $null }
        return (($raw -join "`n") | ConvertFrom-Json)
    } catch {
        return $null
    }
}

function Get-WinBridgeTailscaleIPv4 {
    # This runs on the supervisor's slow poll and while waiting for pairing, so
    # prefer the single-process `tailscale ip -4` call over `status --json`.
    $exe = Get-WinBridgeTailscaleExe
    if ($exe) {
        try {
            $value = & $exe ip -4 2>$null
            if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace(($value -join ''))) {
                $address = ([string]($value -join '')).Trim()
                if ($address -match '^\d{1,3}(\.\d{1,3}){3}$') {
                    return $address
                }
            }
        } catch {
            # Fall through to the status JSON.
        }
    }
    $status = Get-WinBridgeTailscaleStatus
    if ($null -ne $status -and $null -ne $status.Self -and $null -ne $status.Self.TailscaleIPs) {
        foreach ($address in @($status.Self.TailscaleIPs)) {
            if ([string]$address -match '^\d{1,3}(\.\d{1,3}){3}$') {
                return [string]$address
            }
        }
    }
    return $null
}

function Get-WinBridgeTailscaleAccount {
    $status = Get-WinBridgeTailscaleStatus
    if ($null -eq $status -or $null -eq $status.Self) { return $null }
    $userId = [string]$status.Self.UserID
    if ($null -ne $status.User -and $userId) {
        $user = $status.User.$userId
        if ($null -ne $user -and $null -ne $user.LoginName) {
            return [string]$user.LoginName
        }
    }
    # Older Tailscale versions expose the login name directly.
    if ($null -ne $status.Self.UserLoginName) {
        return [string]$status.Self.UserLoginName
    }
    return $null
}

function Start-WinBridgeTailscaleGui {
    $gui = Get-WinBridgeTailscaleGuiExe
    if (-not $gui) { return $false }
    $running = Get-Process -Name 'tailscale-ipn' -ErrorAction SilentlyContinue
    if ($running) { return $true }
    try {
        Start-Process -FilePath $gui | Out-Null
        return $true
    } catch {
        return $false
    }
}

function Install-WinBridgeTailscale {
    if (Get-WinBridgeTailscaleExe) { return }

    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if ($winget) {
        Write-WinBridgeStep 'Installing Tailscale with winget.'
        & $winget.Source install --id Tailscale.Tailscale -e --silent --accept-package-agreements --accept-source-agreements --disable-interactivity 2>&1 |
            ForEach-Object { Write-WinBridgeLog -Message ([string]$_) -Name 'bootstrap' -Level 'INFO' }
        Start-Sleep -Seconds 3
        if (Get-WinBridgeTailscaleExe) { return }
    }

    Write-WinBridgeStep 'Installing Tailscale from the official signed installer.'
    $installer = Join-Path $env:TEMP 'tailscale-setup-latest.exe'
    Invoke-WebRequest -Uri 'https://pkgs.tailscale.com/stable/tailscale-setup-latest.exe' -OutFile $installer -UseBasicParsing
    try {
        $signature = Get-AuthenticodeSignature -LiteralPath $installer
        if ($signature.Status -ne 'Valid') {
            throw ('Tailscale installer signature is not valid: ' + $signature.Status)
        }
        if ($signature.SignerCertificate.Subject -notmatch 'Tailscale') {
            throw 'Tailscale installer signer is not Tailscale.'
        }
        $process = Start-Process -FilePath $installer -ArgumentList '/quiet' -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw ('Tailscale installer exited with code ' + $process.ExitCode)
        }
    } finally {
        if (Test-Path -LiteralPath $installer) {
            Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue
        }
    }

    if (-not (Get-WinBridgeTailscaleExe)) {
        throw 'Tailscale was installed but tailscale.exe was not found.'
    }
}

function Wait-WinBridgeTailscaleReady {
    param([int]$TimeoutSeconds = 600)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $announced = $false
    do {
        $status = Get-WinBridgeTailscaleStatus
        if ($null -ne $status -and $null -ne $status.Self) {
            $online = $true
            if ($null -ne $status.Self.PSObject.Properties['Online']) {
                $online = [bool]$status.Self.Online
            }
            $hasIp = $false
            if ($null -ne $status.Self.TailscaleIPs) {
                foreach ($address in @($status.Self.TailscaleIPs)) {
                    if ([string]$address -match '^\d{1,3}(\.\d{1,3}){3}$') { $hasIp = $true }
                }
            }
            if ($status.BackendState -eq 'Running' -and $online -and $hasIp) {
                return $status
            }
        }
        if (-not $announced) {
            Write-Host '[winbridge] Complete the Tailscale login in the browser if it is not connected yet.'
            $announced = $true
        }
        Start-WinBridgeTailscaleGui | Out-Null
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    throw 'Tailscale did not become ready. Log in with the PC owner account and run the bootstrap again.'
}

function Confirm-WinBridgeTailscaleAccount {
    param(
        [string]$ExpectedAccount,
        [switch]$NonInteractive
    )
    $account = Get-WinBridgeTailscaleAccount
    if (-not $account) {
        throw 'Could not read the Tailscale account. Make sure Tailscale is logged in.'
    }
    Write-Host ('[winbridge] Tailscale account: ' + $account)
    if ($ExpectedAccount) {
        if ($account -ne $ExpectedAccount) {
            throw ('Tailscale is logged in as ' + $account + ' but ' + $ExpectedAccount + ' was expected.')
        }
        return $account
    }
    if ($NonInteractive) {
        throw 'Non-interactive install requires -TailscaleAccount <owner-email> for confirmation.'
    }
    $answer = Read-Host 'Is this the PC owner Tailscale account? Type yes to continue'
    if ($answer -notmatch '^(?i)yes$') {
        throw 'Tailscale account was not confirmed. No changes were made to the Tailscale account.'
    }
    return $account
}

function Ensure-WinBridgeTailscaleStartup {
    $gui = Get-WinBridgeTailscaleGuiExe
    if (-not $gui) { return }
    $startup = [Environment]::GetFolderPath('Startup')
    if (-not $startup) { return }
    $shortcutPath = Join-Path $startup 'Tailscale.lnk'
    if (Test-Path -LiteralPath $shortcutPath) { return }
    try {
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($shortcutPath)
        $shortcut.TargetPath = $gui
        $shortcut.WorkingDirectory = (Split-Path -Parent $gui)
        $shortcut.Description = 'Tailscale'
        $shortcut.Save()
        Write-WinBridgeStep 'Created the Tailscale logon shortcut.'
    } catch {
        Write-WinBridgeWarn ('Could not create the Tailscale logon shortcut: ' + $_.Exception.Message)
    }
}
