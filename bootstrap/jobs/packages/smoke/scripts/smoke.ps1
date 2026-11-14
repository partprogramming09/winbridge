param([string]$Word = 'none')

# Prints what a job sees: who runs it, in which Windows session, and whether it is elevated.
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$admin = ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
Write-Output ('user={0}' -f $identity.Name)
Write-Output ('session_id={0}' -f [Diagnostics.Process]::GetCurrentProcess().SessionId)
Write-Output ('elevated={0}' -f $admin)
Write-Output ('word={0}' -f $Word)
# The environment the runner gives a job: system-only PATH and modules, and a TEMP only administrators can write.
Write-Output ('temp={0}' -f $env:TEMP)
Write-Output ('path={0}' -f $env:PATH)
Write-Output ('psmodulepath={0}' -f $env:PSModulePath)
