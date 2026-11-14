param(
    [Parameter(Mandatory = $true)][string]$App,
    [Parameter(Mandatory = $true)][string]$Action,
    [string]$Name = '',
    [string]$Filter = ''
)

# Reads or presses UI elements by name with UI Automation, only inside an application that app-run started.
#   Action=list   prints the named elements of the app (optionally those whose name contains Filter), with their centers
#   Action=click  presses the one enabled element called Name (Invoke, Toggle or Select pattern); fails when 0 or 2+ match
$ErrorActionPreference = 'Stop'
# The runner redirects the output to a file; without this it is written in the OEM code page and accents are lost.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$root = Split-Path -Parent $PSScriptRoot
if ($Action -cnotmatch '^(list|click)$') { throw 'Action must be list or click' }
if ($App -cnotmatch '^[A-Za-z0-9_-]{1,40}$') { throw 'invalid app name' }
if ($Action -eq 'click' -and -not $Name) { throw 'click needs Name' }

$record = Join-Path $root "state\apps\$App.json"
if (-not (Test-Path -LiteralPath $record)) { throw "$App was not started by app-run" }
$old = Get-Content $record -Raw | ConvertFrom-Json
$proc = Get-Process -Id $old.pid -ErrorAction SilentlyContinue
if (-not $proc -or $proc.StartTime.ToUniversalTime().ToString('o') -ne $old.started) { throw "$App is not running" }

Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$auto = [Windows.Automation.AutomationElement]
$pidCondition = New-Object Windows.Automation.PropertyCondition($auto::ProcessIdProperty, [int]$old.pid)
$windows = $auto::RootElement.FindAll([Windows.Automation.TreeScope]::Children, $pidCondition)
if ($windows.Count -eq 0) { throw "$App has no window yet" }

# Every named element of the app's windows, capped so a huge tree cannot flood the log.
$items = New-Object System.Collections.Generic.List[object]
foreach ($w in $windows) {
    foreach ($e in $w.FindAll([Windows.Automation.TreeScope]::Subtree, [Windows.Automation.Condition]::TrueCondition)) {
        $c = $e.Current
        if (-not $c.Name -or $c.IsOffscreen) { continue }
        $r = $c.BoundingRectangle
        $items.Add([pscustomobject]@{
            name = $c.Name; type = ($c.ControlType.ProgrammaticName -replace '^ControlType\.', '')
            automation_id = $c.AutomationId; enabled = $c.IsEnabled
            x = [int]($r.X + $r.Width / 2); y = [int]($r.Y + $r.Height / 2); element = $e
        })
        if ($items.Count -ge 300) { break }
    }
}

if ($Action -eq 'list') {
    $shown = @($items | Where-Object { -not $Filter -or $_.name.IndexOf($Filter, [StringComparison]::OrdinalIgnoreCase) -ge 0 })
    # -InputObject keeps a single element an array in Windows PowerShell 5.1.
    ConvertTo-Json -InputObject @($shown | Select-Object name, type, automation_id, enabled, x, y) -Compress | Write-Output
    exit 0
}

$hits = @($items | Where-Object { $_.enabled -and $_.name -ieq $Name })
if ($hits.Count -eq 0) { throw "no enabled element named '$Name' in $App" }
if ($hits.Count -gt 1) { throw "$($hits.Count) enabled elements are named '$Name' in $App; use ui list to see them" }
$target = $hits[0]
$el = $target.element
# Invoke can wait for a modal dialog the press opens, so it runs on another thread and is given 3 s.
$pattern = $null
foreach ($p in @([Windows.Automation.InvokePattern]::Pattern, [Windows.Automation.TogglePattern]::Pattern, [Windows.Automation.SelectionItemPattern]::Pattern)) {
    if ($el.TryGetCurrentPattern($p, [ref]$pattern)) { break }
    $pattern = $null
}
if (-not $pattern) { throw "'$Name' has no press pattern; use Click at ($($target.x), $($target.y))" }
$ps = [powershell]::Create()
$null = $ps.AddScript({ param($pat) if ($pat -is [Windows.Automation.InvokePattern]) { $pat.Invoke() } elseif ($pat -is [Windows.Automation.TogglePattern]) { $pat.Toggle() } else { $pat.Select() } }).AddArgument($pattern)
$handle = $ps.BeginInvoke()
if ($handle.AsyncWaitHandle.WaitOne(3000)) { $ps.EndInvoke($handle); $ps.Dispose(); Write-Output "pressed '$Name' ($($target.type))" }
else { Write-Output "pressed '$Name' ($($target.type)); it is still working, probably a dialog is open" }
