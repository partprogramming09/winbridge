# Adds, replaces or removes one MCP server the agent may call through its gateway tools. It pins the SHA-256 of the
# launcher and of any extra files, writes the [servers.<name>] table to config\local.toml (kept across updates),
# checks the result with the agent and restarts it. It runs as the owner and needs no elevation.

param(
    [Parameter(Mandatory = $true)][string]$Name,
    [string]$Command,
    [string[]]$Arguments = @(),
    [string[]]$AllowTools = @(),
    [string[]]$PinFiles = @(),
    [string[]]$DenyArgs = @(),
    [string[]]$EnvPairs = @(),
    [int]$TimeoutSeconds = 120,
    [int]$IdleSeconds = 300,
    [int]$MaxOutput = 40000,
    [switch]$Remove,
    [switch]$NoRestart,
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

$result = [pscustomobject]@{ ok = $false; name = $Name; action = $null; command_sha256 = $null; error = $null }

# Quotes a value as a TOML literal string; a single quote or a line break cannot be represented, so it is refused.
function ConvertTo-TomlLiteral {
    param([string]$Value)
    if ($Value -match "['\r\n]") { throw ('The value cannot hold a single quote or a line break: ' + $Value) }
    return "'" + $Value + "'"
}

# Returns the lines of local.toml without the tables of one server.
function Remove-ServerBlock {
    param([string[]]$Lines, [string]$ServerName)
    $out = New-Object System.Collections.Generic.List[string]
    $skip = $false
    foreach ($line in $Lines) {
        $t = $line.Trim()
        if ($t.StartsWith('[')) {
            $skip = ($t -eq ('[servers.' + $ServerName + ']')) -or $t.StartsWith('[servers.' + $ServerName + '.')
        }
        if (-not $skip) { $out.Add($line) }
    }
    return , $out.ToArray()
}

try {
    if ($Name -notmatch '^[a-z0-9][a-z0-9-]{0,31}$') { throw 'The name uses lowercase letters, digits and dashes, up to 32.' }
    $configDir = Split-Path -Parent (Get-WinBridgeConfigPath)
    $localPath = Join-Path $configDir 'local.toml'
    $goExe = Get-WinBridgePath 'app\go\winbridge-agent.exe'
    if (-not (Test-Path -LiteralPath $goExe)) { throw 'This is not a Go install; use the installer.' }
    Ensure-WinBridgeDirectory -Path $configDir

    $existing = @()
    if (Test-Path -LiteralPath $localPath) { $existing = @(Get-Content -LiteralPath $localPath -Encoding UTF8) }
    $previous = $existing
    $lines = Remove-ServerBlock -Lines $existing -ServerName $Name

    if ($Remove) {
        $result.action = 'removed'
    } else {
        if (-not $Command -or -not [IO.Path]::IsPathRooted($Command)) { throw '-Command must be an absolute path.' }
        if ($AllowTools.Count -eq 0) { throw '-AllowTools must name at least one tool.' }
        $commandSha = Get-WinBridgeFileSha256 -Path $Command
        $result.command_sha256 = $commandSha
        $block = New-Object System.Collections.Generic.List[string]
        $block.Add('[servers.' + $Name + ']')
        $block.Add('command = ' + (ConvertTo-TomlLiteral $Command))
        $block.Add('args = [' + ((@($Arguments) | ForEach-Object { ConvertTo-TomlLiteral $_ }) -join ', ') + ']')
        $block.Add('sha256 = ' + (ConvertTo-TomlLiteral $commandSha))
        $block.Add('allow_tools = [' + ((@($AllowTools) | ForEach-Object { ConvertTo-TomlLiteral $_ }) -join ', ') + ']')
        $block.Add('timeout = ' + $TimeoutSeconds)
        $block.Add('idle = ' + $IdleSeconds)
        $block.Add('max_output = ' + $MaxOutput)
        if ($DenyArgs.Count -gt 0) {
            $block.Add('[servers.' + $Name + '.deny_args]')
            foreach ($entry in $DenyArgs) {
                $index = $entry.IndexOf('=')
                if ($index -lt 1) { throw 'Every -DenyArgs entry looks like tool=arg1,arg2.' }
                $names = @($entry.Substring($index + 1).Split(',') | ForEach-Object { ConvertTo-TomlLiteral $_.Trim() })
                $block.Add($entry.Substring(0, $index).Trim() + ' = [' + ($names -join ', ') + ']')
            }
        }
        if ($PinFiles.Count -gt 0) {
            $block.Add('[servers.' + $Name + '.files]')
            foreach ($file in $PinFiles) {
                if (-not [IO.Path]::IsPathRooted($file)) { throw 'Every -PinFiles entry must be an absolute path.' }
                $block.Add((ConvertTo-TomlLiteral $file) + ' = ' + (ConvertTo-TomlLiteral (Get-WinBridgeFileSha256 -Path $file)))
            }
        }
        if ($EnvPairs.Count -gt 0) {
            $block.Add('[servers.' + $Name + '.env]')
            foreach ($pair in $EnvPairs) {
                $index = $pair.IndexOf('=')
                if ($index -lt 1) { throw 'Every -EnvPairs entry looks like NAME=value.' }
                $block.Add($pair.Substring(0, $index) + ' = ' + (ConvertTo-TomlLiteral $pair.Substring($index + 1)))
            }
        }
        $lines = @($lines) + @('') + $block.ToArray()
        $result.action = 'set'
    }

    $text = ((@($lines) -join "`r`n").Trim()) + "`r`n"
    Assert-WinBridgeLocalConfig -Text $text
    $encoding = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($localPath, $text, $encoding)

    $state = Get-WinBridgeState
    $configPath = Write-WinBridgeConfig -State $state
    $check = Invoke-WinBridgeNative -FilePath $goExe -Arguments @('--config', $configPath, '--print-config')
    if ($check.ExitCode -ne 0) {
        [IO.File]::WriteAllText($localPath, ((@($previous) -join "`r`n") + "`r`n"), $encoding)
        Write-WinBridgeConfig -State $state | Out-Null
        throw ('The agent rejected the configuration, so it was put back: ' + (Redact-WinBridgeText $check.Output))
    }
    if (-not $NoRestart) {
        & (Join-Path $binRoot 'Restart-WinBridge.ps1') -Json | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'The configuration is saved but the agent did not restart; run restart from the Mac.' }
    }
    $result.ok = $true
} catch {
    $result.error = Redact-WinBridgeText $_.Exception.Message
}

if ($Json) { Write-Output ($result | ConvertTo-Json -Compress) } else { Write-Output $result }
if (-not $result.ok) { exit 1 }
