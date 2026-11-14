param(
    [Parameter(Mandatory = $true)][string]$App,
    [string]$AppArgs = ''
)

# Starts one application from the "apps" registry of manifest.json, visible in this session, and prints its pid and window.
$ErrorActionPreference = 'Stop'
# The runner redirects the output to a file; without this it is written in the OEM code page and accents are lost.
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$root = Split-Path -Parent $PSScriptRoot
$manifest = Get-Content (Join-Path $root 'manifest.json') -Raw | ConvertFrom-Json

# Exact, case-sensitive lookup: dotted member access would also answer to intrinsic members such as Count.
$prop = if ($manifest.apps) { $manifest.apps.PSObject.Properties[$App] } else { $null }
if (-not $prop -or $prop.Name -cne $App) { throw "app not allowed: $App" }
$entry = $prop.Value

if (-not (Test-Path -LiteralPath $entry.path -PathType Leaf)) { throw "the executable of $App is missing" }
if ($entry.sha256 -and (Get-FileHash $entry.path -Algorithm SHA256).Hash -ne $entry.sha256) { throw "the hash of the executable of $App does not match the manifest" }

if ($AppArgs) {
    if (-not $entry.args) { throw "$App takes no arguments" }
    if ($AppArgs -cnotmatch ('\A(?:' + [string]$entry.args + ')\z')) { throw "invalid arguments for $App" }
    if ($AppArgs -match '[\x00-\x1f]') { throw 'control characters in the arguments' }
}

$stateDir = Join-Path $root 'state\apps'
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
$record = Join-Path $stateDir "$App.json"
if (Test-Path -LiteralPath $record) {
    $old = Get-Content $record -Raw | ConvertFrom-Json
    $alive = Get-Process -Id $old.pid -ErrorAction SilentlyContinue
    if ($alive -and $alive.StartTime.ToUniversalTime().ToString('o') -eq $old.started) { throw "$App is already running (pid $($old.pid)); stop it with app-stop" }
}

$runAs = if ($entry.run_as) { [string]$entry.run_as } else { 'user' }
try {
    if ($runAs -eq 'admin') {
        $startArgs = @{ FilePath = $entry.path; PassThru = $true; WorkingDirectory = (Split-Path -Parent $entry.path) }
        if ($AppArgs) { $startArgs.ArgumentList = $AppArgs }
        $p = Start-Process @startArgs
    } else {
        # Not elevated: the application gets the token of the user's own desktop shell (explorer), so it runs at the same
        # integrity level as the agent. An elevated window would block the agent's Click, Type and Move (measured).
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class ShellLauncher {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    struct STARTUPINFO { public int cb; public string lpReserved, lpDesktop, lpTitle; public int dwX, dwY, dwXSize, dwYSize, dwXCountChars, dwYCountChars, dwFillAttribute, dwFlags; public short wShowWindow, cbReserved2; public IntPtr lpReserved2, hStdInput, hStdOutput, hStdError; }
    [StructLayout(LayoutKind.Sequential)]
    struct PROCESS_INFORMATION { public IntPtr hProcess, hThread; public int dwProcessId, dwThreadId; }
    [DllImport("kernel32.dll", SetLastError = true)] static extern IntPtr OpenProcess(uint access, bool inherit, int pid);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr h);
    [DllImport("advapi32.dll", SetLastError = true)] static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError = true)] static extern bool DuplicateTokenEx(IntPtr token, uint access, IntPtr attrs, int level, int type, out IntPtr dup);
    [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    static extern bool CreateProcessWithTokenW(IntPtr token, uint logon, string app, string cmd, uint flags, IntPtr env, string dir, ref STARTUPINFO si, out PROCESS_INFORMATION pi);
    // Starts cmd with the token of process shellPid and returns the new process id; throws on failure.
    [DllImport("userenv.dll", SetLastError = true)] static extern bool CreateEnvironmentBlock(out IntPtr env, IntPtr token, bool inherit);
    [DllImport("userenv.dll")] static extern bool DestroyEnvironmentBlock(IntPtr env);
    public static int Run(int shellPid, string cmd, string dir) {
        IntPtr hp = IntPtr.Zero, tok = IntPtr.Zero, dup = IntPtr.Zero, env = IntPtr.Zero;
        try {
            hp = OpenProcess(0x400, false, shellPid);
            if (hp == IntPtr.Zero) throw new Exception("open shell");
            if (!OpenProcessToken(hp, 0x2, out tok)) throw new Exception("shell token");
            if (!DuplicateTokenEx(tok, 0x2000000, IntPtr.Zero, 2, 1, out dup)) throw new Exception("duplicate token");
            // The user's own environment, built from the token: the app must not inherit the elevated runner's variables.
            if (!CreateEnvironmentBlock(out env, dup, false)) throw new Exception("environment block");
            STARTUPINFO si = new STARTUPINFO(); si.cb = Marshal.SizeOf(si); si.lpDesktop = "winsta0\\default";
            PROCESS_INFORMATION pi;
            if (!CreateProcessWithTokenW(dup, 0, null, cmd, 0x400, env, dir, ref si, out pi)) throw new Exception("create process " + Marshal.GetLastWin32Error());
            CloseHandle(pi.hThread); CloseHandle(pi.hProcess);
            return pi.dwProcessId;
        } finally {
            if (env != IntPtr.Zero) DestroyEnvironmentBlock(env);
            if (dup != IntPtr.Zero) CloseHandle(dup);
            if (tok != IntPtr.Zero) CloseHandle(tok);
            if (hp != IntPtr.Zero) CloseHandle(hp);
        }
    }
}
'@
        $session = (Get-Process -Id $PID).SessionId
        # Only the real shell: a process the agent named explorer.exe must never lend its token.
        $realShell = Join-Path $env:SystemRoot 'explorer.exe'
        $shell = Get-Process explorer -ErrorAction SilentlyContinue | Where-Object { $_.SessionId -eq $session -and $_.Path -ieq $realShell } | Select-Object -First 1
        if (-not $shell) { throw 'no desktop shell' }
        $cmd = '"' + $entry.path + '"' + $(if ($AppArgs) { ' ' + $AppArgs } else { '' })
        $newPid = [ShellLauncher]::Run($shell.Id, $cmd, (Split-Path -Parent $entry.path))
        $p = Get-Process -Id $newPid
    }
} catch { throw "could not start $App" }  # the original error would show the executable path
$started = $p.StartTime.ToUniversalTime().ToString('o')
[IO.File]::WriteAllText($record, (@{ pid = $p.Id; started = $started; path = $entry.path } | ConvertTo-Json), (New-Object Text.UTF8Encoding $false))

# Wait for the window so the next Snapshot finds it; an app without a window_title just gets a short pause.
$title = ''
$deadline = (Get-Date).AddSeconds(20)
if ($entry.window_title) {
    while ((Get-Date) -lt $deadline -and -not $p.HasExited) {
        $p.Refresh()
        $title = $p.MainWindowTitle
        if ($title -and $title -cmatch [string]$entry.window_title) { break }
        Start-Sleep -Milliseconds 300
    }
    if (-not ($title -cmatch [string]$entry.window_title)) { Write-Output "warning: no window matching '$($entry.window_title)' appeared in 20 s" }
} else {
    Start-Sleep -Seconds 1
}
Write-Output ('pid={0}' -f $p.Id)
Write-Output ('window={0}' -f $title)
Write-Output ('exited={0}' -f $p.HasExited)
