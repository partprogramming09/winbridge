# winbridge (CLI de la Mac)

Gestiona PCs con WinBridge desde la Mac. Un binario en Go, sin dependencias; usa `ssh` y el Llavero de macOS.

```sh
cd cli && go build -o ~/.local/bin/winbridge .
winbridge hosts add laptop --address 100.64.1.10 --user alice \
  --ssh-key ~/.ssh/id_ed25519 --client-ip 100.64.1.2
winbridge status            # /health, /status con la clave del Llavero, y 401 sin clave
winbridge doctor            # el doctor del PC, por SSH
winbridge logs -n 40        # logs (el PC los redacta)
winbridge restart           # reinicia el servidor mediante el supervisor
winbridge adopt             # clave nueva en el Llavero y emparejamiento (también rota la clave)
```

- La configuración está en `~/.config/winbridge/hosts.json` (variable `WINBRIDGE_CONFIG` para cambiarla) y nunca
  contiene la clave. La clave vive en el Llavero con el servicio `winbridge-<nombre>-<puerto>`.
- `adopt` envía la clave por la entrada estándar de `ssh`, nunca como argumento. Si el emparejamiento falla, restaura
  la clave anterior en el Llavero.
- Limitaciones: la clave pasa un instante como argumento de `security` al guardarla en el Llavero (visible en la
  lista de procesos de la Mac); `doctor` desde SSH no puede leer el Administrador de credenciales de Windows, así que
  esa comprobación sale como aviso y `status` es la que verifica la autenticación.
- Falta (fases siguientes): `install` (copiar y verificar el paquete) y registrar el MCP en Claude Code.

Pruebas: `go test ./...` (usan un servidor HTTP falso y un `Runner` falso; no tocan la red ni el Llavero).
