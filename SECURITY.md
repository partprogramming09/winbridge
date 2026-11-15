# Security policy

**Reader:** anyone who runs WinBridge or wants to report a vulnerability.

WinBridge lets an AI agent on a Mac see and control the desktop of a Windows PC over Tailscale. That is remote control of a
real desktop: read this page before you install it.

## Report a vulnerability

Use GitHub's private vulnerability reporting: open this repository's **Security** tab and choose **Report a vulnerability**.
Do not open a public issue for a vulnerability and do not post keys, logs or `winbridge doctor` output that has not been
redacted.

Only the latest release is supported. Reports are read and answered on a best-effort basis; there is no response-time
promise.

**En español:** para reportar una vulnerabilidad usa la pestaña **Security** de este repositorio y *Report a vulnerability*
(aviso privado de GitHub). No abras un issue público ni pegues claves, registros o la salida del `doctor` sin tapar.

## What counts as a vulnerability

- Anything that lets a caller without the key, or from outside the Tailscale allowlist, reach a tool.
- A way to run something the strict profile does not allow (a tool outside the profile, a job outside the manifest, a shell).
- A way to read the key or other secrets from logs, `/status`, the bundle or the repository.
- A way for the agent account to change what the elevated job runner executes (see [Known limits](#known-limits)).

Out of scope: an attacker who already controls the Windows account that runs the agent (see the first known limit), and
problems in Tailscale, Windows or OpenSSH themselves.

## Threat model

WinBridge is designed for one trusted Mac and one Windows PC on the same tailnet.

What it does:

- The Go agent, as the supervisor launches it (`WINBRIDGE_STRICT=1`), listens only on the PC's Tailscale address
  (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`); it refuses to start on `0.0.0.0`, `::`, loopback or a LAN address. Started by
  hand without that variable, it does not enforce this.
- In the Go agent every path, including `/health`, obeys an IP allowlist, and an empty allowlist closes everything. The
  inherited Python server exempts `/health` from its allowlist and mounts no allowlist when the list is empty.
- Every tool call needs a Bearer key. The Go agent compares a SHA-256 of the key in constant time; the inherited Python server
  uses `hmac.compare_digest`.
- The key lives in Windows Credential Manager on the PC and in the macOS Keychain on the Mac. `winbridge adopt` sends it to
  the PC over SSH standard input, never as an argument there. While it waits for the supervisor it sits in a DPAPI-protected
  file, and the agent receives it through its environment. On the Mac it appears briefly in the arguments of
  `security add-generic-password` when it goes into the Keychain.
- The strict profile exposes exactly nine tools: `Snapshot`, `Click`, `Type`, `Move`, `Scroll`, `Shortcut`, `FocusWindow`,
  `Wait` and `GetSystemInfo`; none of the nine runs a command or reads a file (the opt-in file tools below are separate). Through `Type` and `Shortcut`, a key holder can
  still start any program on the unlocked desktop.
- Traffic is plain HTTP inside the Tailscale tunnel, which Tailscale encrypts. There is no TLS of its own.

What it does not do: it is not an Internet tunnel, it does not protect a PC that Tailscale already exposes to a hostile
device, and after the network layer the client allowlist and the Bearer key are the defences. Anyone holding the key, from an
allowed address, can control the desktop.

## Known limits

These are known and documented, not hidden. Some are fixed by design choices; the ones marked *not verified* come from our
own code review and were not shown to be exploitable.

- **The agent and the job runner share one Windows account.** The runner runs elevated through a scheduled task. UAC is not a
  security boundary against another process of the same account. The runner hardens what it can: jobs get a system-only
  `PATH` and module path, a `TEMP` that only administrators can write, and none of the `COR_*`, `CORECLR_*` and `DOTNET_STARTUP_HOOKS`
  variables (the `DOTNET_*` profiler variables are not cleared); the runner file is installed only if its bytes match the
  SHA-256 given to the installer, which does not cover the installer or the job scripts, so run the installer from the
  freshly extracted, verified bundle and never from `bin\jobs`; and its clean-up of `cancel\` never enters subfolders or
  links and skips names Win32 rewrites (a trailing dot or space). It cannot protect its own process from variables the account set before it
  started. See [docs/JOBS.md](docs/JOBS.md).
- **Built-in tools beyond the nine.** The agent also exposes `Processes` and `Diagnose` (read-only, always) and, when
  `local.toml` defines `[jobs]`, `[servers]` or `[tasks]`, the job tools (`SubmitJob` and others), the gateway tools
  (`CallServerTool` and others) and `RunTask`. `/status` lists the first two groups in `builtin_tools`, outside `tools_count`. `RunTask` appears in `tools` and is
  counted, so `winbridge adopt` reports a tool-count mismatch while `[tasks]` is defined. They exist only if the owner
  configures them.
- **Opt-in file tools (`ListFiles`, `ReadFile`).** They exist only with a `[files]` table in `local.toml`. A key holder reads
  every file inside the folders the owner named and nothing outside them; the agent refuses to start with a folder that is a
  drive root, the user profile, WinBridge's folders, `.ssh`, `AppData`, the Start menu, Windows, Program Files or a `PATH`
  folder. The folders limit a deceived agent, not an attacker who already controls the Windows account, which can read the
  same files. Choose folders that hold only what you are willing to share. They are built-in tools and do not count in
  `tools_count`. The Python fallback does not have them. See [ADR 0003](docs/adr/0003-herramientas-de-archivos-opt-in.md).
- **`sshd` listens on `0.0.0.0:22` and `:::22`.** Only the Windows Firewall rules, which allow the Tailscale ranges, limit who
  can reach it. A `ListenAddress` restriction was postponed because a mistake would cut your only way in. `winbridge doctor`
  checks the firewall rules and flags a rule that opens port 22 or the MCP port to `Any`, `LocalSubnet` and similar broad
  sources; it does not flag an explicit LAN range.
- **The inherited Python server** (`src/winremote`, from upstream `winremote-mcp 0.4.24`) is still in the repository and can
  run outside the strict profile. WinBridge's installer deploys the Go agent. Running the Python server without the strict
  profile is not a supported setup. The supervisor also falls back to it, still in strict mode, if the Go agent keeps
  failing; its guarantees are weaker (see the Threat model).
- **Installer signer check (*not verified*).** The installer accepts a downloaded Tailscale or Python installer whose signer
  contains a given name, not one whose signer matches exactly (`bootstrap/lib/Tailscale.ps1`, `PythonRuntime.ps1`).
- **Time of check, time of use in admin apps (*not verified*).** A job app configured with `run_as=admin` is hashed and then
  started by path (`bootstrap/jobs/packages/apps/scripts/app-run.ps1`); a file replaced in between would run. Keep such apps in folders only
  administrators can write.
- **No lock-screen unlock.** There is no autologon. If the session is locked, the input tools (`Click`, `Type`, `Move`, `Scroll`, `Shortcut`, `FocusWindow`) fail with an
  explicit error; `Snapshot` shows the lock screen and marks it as locked.

## Hardening you should do

- Keep Tailscale access controls tight: only your Mac should reach the PC's ports 22 and 8091.
- Pair with `winbridge adopt` (64-character key); rotate by running it again.
- Run `winbridge doctor` after every update and read the warnings.
- Never paste a key into an issue. Before you attach `doctor` output, remove the Windows user name and `C:\Users\…` paths,
  the computer name, the Tailscale address and the Tailscale account e-mail.
