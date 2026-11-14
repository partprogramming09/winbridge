# Python runtime and offline wheel installation for WinBridge.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Get-WinBridgePythonVersion {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $value = & $Path -c "import sys; print('.'.join(map(str, sys.version_info[:3])))" 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return ([string]($value -join '')).Trim()
    } catch {
        return $null
    }
}

function Test-WinBridgePythonVersion {
    param([string]$Version)
    if (-not $Version) { return $false }
    $parts = $Version.Split('.')
    if ($parts.Count -lt 2) { return $false }
    $major = [int]$parts[0]
    $minor = [int]$parts[1]
    # The wheelhouse is built for CPython 3.12 and the install lock names the
    # exact built wheels (windows-py312-bundle.lock.txt). Do not accept another
    # minor version.
    return ($major -eq 3 -and $minor -eq 12)
}

function Resolve-WinBridgeLinkedPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    # Follows a junction or symlink on the interpreter's folder; Windows may refuse to traverse one it does not trust.
    try {
        $item = Get-Item -LiteralPath (Split-Path -Parent $Path) -Force -ErrorAction Stop
        if ($item.LinkType -and $item.Target) {
            $target = [string](@($item.Target)[0])
            $resolved = Join-Path $target (Split-Path -Leaf $Path)
            if (Test-Path -LiteralPath $resolved -PathType Leaf) { return $resolved }
        }
    } catch {
        # Keep the original path.
    }
    return $Path
}

function Resolve-WinBridgeBasePython {
    param([Parameter(Mandatory = $true)][string]$Path)
    # A shim such as uv's python.exe would make the venv point at a launcher link; use the real interpreter.
    try {
        $value = & $Path -c "import sys; print(sys._base_executable)" 2>$null
        if ($LASTEXITCODE -eq 0 -and $value) {
            $real = ([string]($value -join '')).Trim()
            if ($real -and (Test-Path -LiteralPath $real -PathType Leaf)) {
                $real = Resolve-WinBridgeLinkedPath -Path $real
                if (Get-WinBridgePythonVersion -Path $real) { return $real }
            }
        }
    } catch {
        # Keep the original path.
    }
    return $Path
}

function Find-WinBridgePython {
    $candidates = @()
    $command = Get-Command python.exe -ErrorAction SilentlyContinue
    if ($command -and $command.Source -notmatch '\\WindowsApps\\') {
        $candidates += $command.Source
    }
    $candidates += @(
        (Join-Path $env:LOCALAPPDATA 'Programs\Python\Python312\python.exe'),
        (Join-Path $env:ProgramFiles 'Python312\python.exe')
    )
    foreach ($candidate in $candidates) {
        if (-not $candidate) { continue }
        if (-not (Test-Path -LiteralPath $candidate)) { continue }
        $version = Get-WinBridgePythonVersion -Path $candidate
        if (Test-WinBridgePythonVersion -Version $version) {
            return [pscustomobject]@{ Path = (Resolve-WinBridgeBasePython -Path $candidate); Version = $version }
        }
    }
    $py = Get-Command py.exe -ErrorAction SilentlyContinue
    if ($py) {
        try {
            $value = & $py.Source -3.12 -c "import sys; print(sys.executable)" 2>$null
            if ($LASTEXITCODE -eq 0 -and $value) {
                $path = ([string]($value -join '')).Trim()
                if (Test-Path -LiteralPath $path) {
                    $version = Get-WinBridgePythonVersion -Path $path
                    if (Test-WinBridgePythonVersion -Version $version) {
                        return [pscustomobject]@{ Path = $path; Version = $version }
                    }
                }
            }
        } catch {
            # Fall through to the install path.
        }
    }
    return $null
}

function Install-WinBridgePython {
    $existing = Find-WinBridgePython
    if ($existing) { return $existing }

    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if ($winget) {
        Write-WinBridgeStep 'Installing Python 3.12 with winget.'
        & $winget.Source install --id Python.Python.3.12 -e --silent --accept-package-agreements --accept-source-agreements --disable-interactivity 2>&1 |
            ForEach-Object { Write-WinBridgeLog -Message ([string]$_) -Name 'bootstrap' -Level 'INFO' }
        Start-Sleep -Seconds 3
        $existing = Find-WinBridgePython
        if ($existing) { return $existing }
    }

    Write-WinBridgeStep 'Installing Python 3.12 from the official signed installer.'
    $installer = Join-Path $env:TEMP 'python-3.12.10-amd64.exe'
    Invoke-WebRequest -Uri 'https://www.python.org/ftp/python/3.12.10/python-3.12.10-amd64.exe' -OutFile $installer -UseBasicParsing
    try {
        $signature = Get-AuthenticodeSignature -LiteralPath $installer
        if ($signature.Status -ne 'Valid') {
            throw ('Python installer signature is not valid: ' + $signature.Status)
        }
        if ($signature.SignerCertificate.Subject -notmatch 'Python Software Foundation') {
            throw 'Python installer signer is not the Python Software Foundation.'
        }
        $process = Start-Process -FilePath $installer `
            -ArgumentList '/quiet InstallAllUsers=0 PrependPath=0 Include_launcher=1 Include_pip=1 Include_test=0' `
            -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw ('Python installer exited with code ' + $process.ExitCode)
        }
    } finally {
        if (Test-Path -LiteralPath $installer) {
            Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue
        }
    }
    $existing = Find-WinBridgePython
    if (-not $existing) {
        throw 'Python 3.12 was installed but python.exe was not found.'
    }
    return $existing
}

function New-WinBridgeVenv {
    param(
        [Parameter(Mandatory = $true)][string]$PythonPath,
        [switch]$Recreate
    )
    $venv = Get-WinBridgePath 'app\venv'
    if ($Recreate -and (Test-Path -LiteralPath $venv)) {
        Write-WinBridgeStep 'Recreating the WinBridge virtual environment.'
        Remove-Item -LiteralPath $venv -Recurse -Force
    }
    $venvPython = Join-Path $venv 'Scripts\python.exe'
    if (-not (Test-Path -LiteralPath $venvPython)) {
        Ensure-WinBridgeDirectory -Path (Split-Path -Parent $venv)
        & $PythonPath -m venv $venv
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $venvPython)) {
            throw 'Could not create the WinBridge virtual environment.'
        }
    }
    & $venvPython -c "import sys" 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw ('The WinBridge virtual environment cannot run its Python (' + $venvPython + '). Run the bootstrap again with -RecreateVenv.')
    }
    return $venvPython
}

function Test-WinBridgeWheelHash {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedSha256
    )
    if (-not (Test-Path -LiteralPath $Path)) {
        throw ('Wheel not found: ' + $Path)
    }
    $name = Split-Path -Leaf $Path
    if ($name -notlike 'winbridge_mcp-*.whl') {
        throw ('Refusing to install a wheel that is not winbridge_mcp: ' + $name)
    }
    $actual = Get-WinBridgeFileSha256 -Path $Path
    if ($actual -ne $ExpectedSha256.ToLowerInvariant()) {
        throw ('Wheel SHA-256 mismatch. Expected ' + $ExpectedSha256 + ' but found ' + $actual)
    }
}

function Get-WinBridgeWheelPath {
    param([Parameter(Mandatory = $true)][string]$Root)
    $wheels = @(Get-ChildItem -LiteralPath $Root -Filter 'winbridge_mcp-*.whl' -File -ErrorAction SilentlyContinue)
    if ($wheels.Count -ne 1) {
        throw ('Expected exactly one winbridge_mcp wheel in ' + $Root + ', found ' + $wheels.Count + '.')
    }
    return $wheels[0].FullName
}

function Install-WinBridgePythonPackages {
    param(
        [Parameter(Mandatory = $true)][string]$VenvPython,
        [Parameter(Mandatory = $true)][string]$Wheelhouse,
        [Parameter(Mandatory = $true)][string]$LockFile
    )
    if (-not (Test-Path -LiteralPath $LockFile)) {
        throw ('Requirements lock not found: ' + $LockFile)
    }
    Write-WinBridgeStep 'Installing hash-locked Python packages (offline).'
    $output = & $VenvPython -m pip install `
        --no-index `
        --find-links $Wheelhouse `
        --require-hashes `
        --disable-pip-version-check `
        --no-warn-script-location `
        -r $LockFile 2>&1
    $exitCode = $LASTEXITCODE
    foreach ($line in @($output)) {
        Write-WinBridgeLog -Message ([string]$line) -Name 'bootstrap' -Level 'INFO'
    }
    if ($exitCode -ne 0) {
        throw ('pip install failed with exit code ' + $exitCode + '.')
    }
}

function Install-WinBridgeWheel {
    param(
        [Parameter(Mandatory = $true)][string]$VenvPython,
        [Parameter(Mandatory = $true)][string]$WheelPath
    )
    $output = & $VenvPython -m pip install --no-deps --force-reinstall --disable-pip-version-check $WheelPath 2>&1
    $exitCode = $LASTEXITCODE
    foreach ($line in @($output)) {
        Write-WinBridgeLog -Message ([string]$line) -Name 'bootstrap' -Level 'INFO'
    }
    if ($exitCode -ne 0) {
        throw ('pip install of the WinBridge wheel failed with exit code ' + $exitCode + '.')
    }
}

function Get-WinBridgePackageVersion {
    param([Parameter(Mandatory = $true)][string]$VenvPython)
    try {
        $value = & $VenvPython -c "import winremote; print(winremote.__version__)" 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return ([string]($value -join '')).Trim()
    } catch {
        return $null
    }
}
