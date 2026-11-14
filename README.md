# WinBridge

> **Security notice / Aviso de seguridad.** WinBridge gives an AI agent control of a real Windows desktop (screenshots,
> mouse, keyboard). Behind the Tailscale network, the allowlist and the Bearer key are the defences: whoever holds the key
> controls the desktop. Read [SECURITY.md](SECURITY.md) first. / *WinBridge da a un agente de IA el control de un escritorio real de
> Windows. Tras la red Tailscale, la lista de IP y la clave Bearer son las defensas. Lee antes [SECURITY.md](SECURITY.md).*

[English](#english) · [Español](#español)

## English

**Reader:** someone who wants a Mac AI agent to see and operate a Windows PC over Tailscale.

### What it is, and what it is not

WinBridge is a fork of [winremote-mcp](https://github.com/dddabtc/winremote-mcp) 0.4.24 with a strict profile: one MCP server
on a Windows PC that exposes **nine desktop tools** and nothing else by default, reachable **only through Tailscale**.

- It is **not** a tunnel and it is not reachable from the Internet or a LAN. The agent binds to the PC's Tailscale address and
  refuses anything else.
- It has **no shell and no file access**. The nine profile tools are `Snapshot`, `Click`, `Type`, `Move`, `Scroll`, `Shortcut`,
  `FocusWindow`, `Wait` and `GetSystemInfo`. The agent adds built-in read-only tools (`Processes`, `Diagnose`) and, if you
  configure them, job and gateway tools; they are listed in `/status` under `builtin_tools`.
- The desktop tools need an unlocked interactive session. If the PC is locked they fail with a clear error; WinBridge never
  unlocks it.

How the pieces fit: [docs/architecture.md](docs/architecture.md). Decision record:
[ADR 0001](docs/adr/0001-perfil-estricto-solo-tailscale.md).

### Requirements

- A Windows 10 or 11 PC (x64) and a Mac. The `winbridge` command-line client runs **on macOS only** (it keeps the key in the
  Keychain with `security`).
- [Tailscale](https://tailscale.com/) on both machines, and SSH with a key from the Mac to the PC.
- Go 1.25 and Python 3.11+ with [uv](https://docs.astral.sh/uv/) to build; PowerShell 7 is recommended for the tests.

> A clean install on a fresh Windows machine has not been tested end to end yet. The update path on an existing install has.

### Install, step by step

1. **Build the agent and the bundle** (on the Mac, from a clone of this repository):

   ```sh
   (cd agent && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
     go build -ldflags "-X main.version=0.1.0" -o ../dist-go/winbridge-agent.exe ./cmd/winbridge-agent)
   uv run python tools/build_bundle.py --go-agent dist-go/winbridge-agent.exe \
     --ssh-public-key-file ~/.ssh/id_ed25519.pub
   ```

   Instead of compiling, you can download `winbridge-agent-windows-amd64.exe`, `winbridge-darwin-arm64` or
   `winbridge-darwin-amd64` and `SHA256SUMS` from the repository's Releases page, then check them:

   ```sh
   shasum -a 256 -c SHA256SUMS      # download all three binaries and SHA256SUMS first
   gh attestation verify winbridge-agent-windows-amd64.exe -R partprogramming09/winbridge
   ```

   Make a downloaded client runnable with `chmod +x winbridge-darwin-arm64` and
   `xattr -d com.apple.quarantine winbridge-darwin-arm64`, and move it to a folder on your `PATH` as `winbridge`. Pass the
   downloaded agent to `--go-agent`. No ready-made bundle is published, because the bundle carries *your* SSH public
   key.

   The `build_bundle.py` command prints the ZIP's SHA-256. Keep it: it is the trust anchor, so deliver it to the PC by a different channel
   than the ZIP.
2. **Install on the PC.** Copy the ZIP, check its SHA-256, extract the whole ZIP, open an elevated Windows PowerShell in that
   folder and run `powershell -NoProfile -ExecutionPolicy Bypass -File .\bootstrap.ps1`. Log in to Tailscale with the PC
   owner's account if asked. Details and options: [docs/guides/operar-winbridge.md](docs/guides/operar-winbridge.md).
3. **Build the client** (Mac): `(cd cli && go build -ldflags "-X main.version=0.1.0" -o ~/.local/bin/winbridge .)`.
4. **Register the PC and pair** (Mac): use the PC's Tailscale address and this Mac's Tailscale address.

   ```sh
   winbridge hosts add mypc --address 100.64.1.10 --user alice --client-ip 100.64.1.20
   winbridge adopt mypc
   winbridge status mypc
   winbridge doctor mypc
   ```

   `adopt` creates a 64-character key, stores it in the Keychain and sends it to the PC over SSH standard input.
   `status` should print `ok`, the version, `strict` and `9 tools`. Run `doctor` after every change and read its warnings.
5. **Update later** with a new bundle: `winbridge update mypc --bundle winbridge-0.1.0-bundle.zip`. If the new agent does not answer
   `/health`, the previous executable is put back (only the executable; see the operating guide).

Your MCP client connects to `http://<pc-tailscale-address>:8091/mcp` with the header `Authorization: Bearer <key>`.

### Learn more

- [SECURITY.md](SECURITY.md): reporting, threat model and known limits.
- [docs/guides/operar-winbridge.md](docs/guides/operar-winbridge.md): operating guide.
- [docs/AGENT-CONTRACT.md](docs/AGENT-CONTRACT.md), [docs/JOBS.md](docs/JOBS.md), [docs/GATEWAY.md](docs/GATEWAY.md).
- [CONTRIBUTING.md](CONTRIBUTING.md) and [CHANGELOG.md](CHANGELOG.md).

Licence: MIT ([LICENSE](LICENSE)). WinBridge builds on winremote-mcp; see [NOTICE](NOTICE).

## Español

**Lector:** quien quiere que un agente de IA en una Mac vea y maneje un PC con Windows por Tailscale.

### Qué es y qué no es

WinBridge es una bifurcación de [winremote-mcp](https://github.com/dddabtc/winremote-mcp) 0.4.24 con un perfil estricto: un
servidor MCP en un PC con Windows que expone **nueve herramientas de escritorio** y nada más por defecto, accesible **solo
por Tailscale**.

- **No** es un túnel y no se alcanza desde Internet ni desde la red local. El agente escucha en la dirección Tailscale del PC.
- **No** hay shell ni acceso a archivos. Las nueve herramientas de perfil son `Snapshot`, `Click`, `Type`, `Move`, `Scroll`,
  `Shortcut`, `FocusWindow`, `Wait` y `GetSystemInfo`. El agente añade herramientas integradas de solo lectura (`Processes`,
  `Diagnose`) y, si las configuras, las de trabajos y pasarela; `/status` las lista en `builtin_tools`.
- Las herramientas de escritorio necesitan una sesión interactiva desbloqueada. Con el PC bloqueado fallan con un error
  claro; WinBridge nunca lo desbloquea.

### Requisitos

- Un PC con Windows 10 u 11 (x64) y una Mac. El cliente `winbridge` **solo corre en macOS** (guarda la clave en el Llavero).
- [Tailscale](https://tailscale.com/) en ambos equipos y SSH con clave de la Mac al PC.
- Go 1.25 y Python 3.11+ con [uv](https://docs.astral.sh/uv/) para compilar; se recomienda PowerShell 7 para las pruebas.

> Una instalación limpia en un Windows nuevo aún no se ha probado de principio a fin. La actualización de una instalación
> existente sí.

### Instalación

Los pasos y las órdenes son los de la sección *English* (compilar el agente y el paquete, instalar en el PC con
`bootstrap.ps1`, compilar el cliente, `winbridge hosts add`, `winbridge adopt`, `winbridge status`, `winbridge doctor`). Para
actualizar: `winbridge update mypc --bundle winbridge-0.1.0-bundle.zip`. Tu cliente MCP se conecta a
`http://<dirección-tailscale-del-pc>:8091/mcp` con la cabecera `Authorization: Bearer <clave>`.

### Más información

- [SECURITY.md](SECURITY.md): cómo reportar, modelo de amenaza y límites conocidos.
- [docs/architecture.md](docs/architecture.md) y [docs/guides/operar-winbridge.md](docs/guides/operar-winbridge.md).
- [CONTRIBUTING.md](CONTRIBUTING.md) y [CHANGELOG.md](CHANGELOG.md).

Licencia: MIT ([LICENSE](LICENSE)). WinBridge se apoya en winremote-mcp; ver [NOTICE](NOTICE).
