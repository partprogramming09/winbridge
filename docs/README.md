# Índice de documentación: winbridge

**Lector:** personas

Antes de escribir un doc nuevo, buscar aquí si el tema ya existe; nunca explicar lo mismo dos veces.

## Para personas

WinBridge:

- [architecture.md](architecture.md): ¿Qué piezas tiene WinBridge, cómo se conectan y qué garantiza el modo estricto?
- [guides/operar-winbridge.md](guides/operar-winbridge.md): ¿Cómo se instala, se empareja, se opera día a día y se actualiza (y qué partes están por reconciliar con el agente Go)?
- [AGENT-CONTRACT.md](AGENT-CONTRACT.md): ¿Qué contrato (HTTP, modo estricto, configuración, herramientas) debe cumplir el agente de WinBridge?
- [GATEWAY.md](GATEWAY.md): ¿Cómo llama el agente a herramientas de otros servidores MCP locales y qué protege y qué no?
- [JOBS.md](JOBS.md): ¿Cómo se lanzan, vigilan y cancelan desde la Mac trabajos elevados en el escritorio del laptop?
- [adr/0001-perfil-estricto-solo-tailscale.md](adr/0001-perfil-estricto-solo-tailscale.md): ¿Por qué solo nueve herramientas y solo por Tailscale?
- [adr/0002-repositorio-publico-de-un-commit.md](adr/0002-repositorio-publico-de-un-commit.md): ¿Por qué el repositorio público tiene un solo commit y qué protege que no se filtren datos personales?

Heredados de upstream (`winremote-mcp 0.4.24`; describen el servidor Python original y no están verificados para el perfil estricto):

- [index.md](index.md): ¿Qué es WinRemote MCP, qué ofrece y cómo se empieza en un minuto?
- [installation.md](installation.md): ¿Cómo se instala (requisitos, PyPI, fuente, dependencias opcionales) y cómo se resuelven los fallos de instalación?
- [usage.md](usage.md): ¿Cómo se arranca el servidor, se conecta a clientes MCP y se usa en el día a día?
- [api.md](api.md): ¿Qué herramientas expone el servidor, con qué parámetros y qué devuelven?
- [faq.md](faq.md): ¿Cómo se resuelven las dudas y problemas más frecuentes de instalación, conexión y uso?
- [ocr-setup.md](ocr-setup.md): ¿Cómo se configura el OCR (pytesseract u OCR de Windows) y cómo se usa?
- [vision-guide.md](vision-guide.md): ¿Cómo se automatizan interfaces que Win32 no detecta (Flutter, Electron) usando visión?
- [openclaw-integration.md](openclaw-integration.md): ¿Cómo se integra con OpenClaw y cómo se publica como skill?
- [contributing.md](contributing.md): ¿Cómo se prepara el entorno de desarrollo, se añaden herramientas y tests, y se publica una versión?

## Diagramas

- [diagrams/arquitectura.png](diagrams/arquitectura.png) (original editable: [arquitectura.html](diagrams/arquitectura.html)): arquitectura de WinBridge (dos caminos, un perfil estricto).
- [diagrams/pairing.png](diagrams/pairing.png) ([pairing.html](diagrams/pairing.html)): cómo viaja la clave en el pairing.
- [diagrams/modo-estricto.png](diagrams/modo-estricto.png) ([modo-estricto.html](diagrams/modo-estricto.html)): las cuatro reglas del arranque estricto.
- [diagrams/trabajos.png](diagrams/trabajos.png) ([trabajos.html](diagrams/trabajos.html)): flujo del ejecutor de trabajos.
- [diagrams/pasarela.png](diagrams/pasarela.png) ([pasarela.html](diagrams/pasarela.html)): la pasarela MCP y sus verificaciones.
