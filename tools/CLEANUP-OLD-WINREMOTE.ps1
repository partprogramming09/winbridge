# Removes what is left of the first winremote-mcp install. Run it yourself in an elevated PowerShell.
# It is not run by any WinBridge script and nothing depends on it; check the two printouts before answering Y.

$ruleName = 'winremote-mcp (Tailscale Mac only)'
$folder = Join-Path $env:LOCALAPPDATA 'winremote-mcp'

Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue | Format-List DisplayName, Enabled, Direction, Action
Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue | Get-NetFirewallPortFilter | Format-List LocalPort
Get-ChildItem -LiteralPath $folder -ErrorAction SilentlyContinue | Select-Object Name, Length | Format-Table

if ((Read-Host 'Delete this rule (port 8090) and this folder? [y/N]') -ne 'y') { return }

Remove-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $folder -Recurse -Force -ErrorAction SilentlyContinue
'Done.'
