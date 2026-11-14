# Measures idle memory, threads and CPU of the installed WinBridge server for the Python and the Go agent.
# Run on the PC in a normal PowerShell window while nobody uses WinBridge. It switches the agent with
# Use-WinBridgeAgent.ps1 for the duration and leaves the Go agent selected when done.
[CmdletBinding()]
param(
    [int]$Samples = 10,
    [int]$IntervalSeconds = 30,
    [int]$WarmupSeconds = 60,
    [string]$Out = (Join-Path $env:TEMP 'winbridge-idle.csv')
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$root = Join-Path $env:LOCALAPPDATA 'winbridge'
$use = Join-Path $root 'bin\Use-WinBridgeAgent.ps1'
$rows = @()

# Sums the process and its descendants: the Python server pid is a launcher whose child does the work.
function Get-WinBridgeTree {
    param([int]$RootPid)
    $all = Get-CimInstance Win32_Process | Select-Object ProcessId, ParentProcessId
    $ids = @($RootPid)
    do {
        $more = @($all | Where-Object { $ids -contains $_.ParentProcessId -and $ids -notcontains $_.ProcessId } | ForEach-Object { $_.ProcessId })
        $ids += $more
    } while ($more.Count -gt 0)
    $procs = @(Get-Process -Id $ids -ErrorAction SilentlyContinue)
    [pscustomobject]@{
        count = $procs.Count
        ws = ($procs | Measure-Object WorkingSet64 -Sum).Sum
        priv = ($procs | Measure-Object PrivateMemorySize64 -Sum).Sum
        threads = ($procs | ForEach-Object { $_.Threads.Count } | Measure-Object -Sum).Sum
        cpu = ($procs | ForEach-Object { $_.TotalProcessorTime.TotalSeconds } | Measure-Object -Sum).Sum
    }
}

foreach ($agent in @('python', 'go')) {
    Write-Host ('Switching to ' + $agent)
    & powershell -NoProfile -ExecutionPolicy Bypass -File $use -Agent $agent -Json | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ('Could not switch to ' + $agent) }
    Start-Sleep -Seconds $WarmupSeconds
    $serverPid = [int](Get-Content -LiteralPath (Join-Path $root 'state\server.pid') -Raw)
    $previous = Get-WinBridgeTree -RootPid $serverPid
    for ($i = 1; $i -le $Samples; $i++) {
        Start-Sleep -Seconds $IntervalSeconds
        $p = Get-WinBridgeTree -RootPid $serverPid
        $cpu = [math]::Round((($p.cpu - $previous.cpu) / $IntervalSeconds) * 100 / [Environment]::ProcessorCount, 3)
        $previous = $p
        $rows += [pscustomobject]@{
            agent = $agent; sample = $i; pid = $serverPid; processes = $p.count
            working_set_mb = [math]::Round($p.ws / 1MB, 1)
            private_mb = [math]::Round($p.priv / 1MB, 1)
            threads = $p.threads; cpu_percent = $cpu
        }
    }
}
& powershell -NoProfile -ExecutionPolicy Bypass -File $use -Agent go -Json | Out-Null
$rows | Export-Csv -LiteralPath $Out -NoTypeInformation
$rows | Group-Object agent | ForEach-Object {
    $g = $_.Group
    '{0}: working set {1}-{2} MB, private {3}-{4} MB, threads {5}, cpu avg {6} %' -f $_.Name,
        ($g.working_set_mb | Measure-Object -Minimum).Minimum, ($g.working_set_mb | Measure-Object -Maximum).Maximum,
        ($g.private_mb | Measure-Object -Minimum).Minimum, ($g.private_mb | Measure-Object -Maximum).Maximum,
        ($g.threads | Measure-Object -Maximum).Maximum, [math]::Round(($g.cpu_percent | Measure-Object -Average).Average, 3)
}
Write-Host ('CSV: ' + $Out)
