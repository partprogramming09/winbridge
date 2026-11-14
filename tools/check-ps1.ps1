#requires -Version 5.1
# Parse every PowerShell script with the real PowerShell parser.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\check-ps1.ps1
#   pwsh -NoProfile -File tools/check-ps1.ps1
#
# This catches syntax errors only. Runtime contracts are covered by
# tools/test-bootstrap.ps1 and the Python contract tests.

[CmdletBinding()]
param(
    [string]$Root = ''
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

if (-not $Root) {
    $Root = Split-Path -Parent $PSScriptRoot
}
$rootPath = (Resolve-Path -LiteralPath $Root -ErrorAction Stop).Path
$files = @(
    Get-ChildItem -LiteralPath $rootPath -Recurse -Filter '*.ps1' -File -ErrorAction SilentlyContinue |
        Where-Object {
            $_.FullName -notmatch '[\\/](\.git|\.venv|venv|build|dist|node_modules)[\\/]'
        } |
        Sort-Object FullName
)

if ($files.Count -eq 0) {
    Write-Error ('No PowerShell files found under ' + $rootPath)
    exit 1
}

$failures = 0
$version = $PSVersionTable.PSVersion.ToString()
Write-Host ('[check-ps1] PowerShell ' + $version + ' - ' + $files.Count + ' file(s) under ' + $rootPath)

foreach ($file in $files) {
    $tokens = $null
    $errors = $null
    try {
        [System.Management.Automation.Language.Parser]::ParseFile(
            $file.FullName,
            [ref]$tokens,
            [ref]$errors
        ) | Out-Null
    } catch {
        $failures++
        $relative = $file.FullName.Substring($rootPath.Length).TrimStart('\', '/')
        Write-Host ('[FAIL] ' + $relative + ': parser could not read the file: ' + $_.Exception.Message) -ForegroundColor Red
        continue
    }

    $relative = $file.FullName.Substring($rootPath.Length).TrimStart('\', '/')
    if ($null -ne $errors -and $errors.Count -gt 0) {
        $failures++
        foreach ($error in $errors) {
            Write-Host (
                '[FAIL] ' + $relative + ':' + $error.Extent.StartLineNumber + ':' +
                $error.Extent.StartColumnNumber + ': ' + $error.Message
            ) -ForegroundColor Red
        }
    } else {
        Write-Host ('[ OK ] ' + $relative) -ForegroundColor Green
    }
}

if ($failures -gt 0) {
    Write-Host ('[check-ps1] ' + $failures + ' file(s) failed to parse.') -ForegroundColor Red
    exit 1
}

Write-Host '[check-ps1] All PowerShell files parsed cleanly.'
exit 0
