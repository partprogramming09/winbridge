# 0001. Perfil estricto de nueve herramientas, solo por Tailscale

**Lector:** personas
**Estado:** Aceptada
**Fecha de registro:** 2026-11-14 (la decisión es anterior; se recoge aquí desde `docs/guides/operar-winbridge.md` y `docs/AGENT-CONTRACT.md`)

## Contexto

`winremote-mcp` (upstream) es un servidor MCP de administración remota de Windows con muchas herramientas: shell,
escritura de archivos, registro, servicios. WinBridge lo usa para dar soporte a un PC desde una Mac de confianza. Quien
alcance ese servidor controla el escritorio, así que la superficie debe ser la mínima que haga falta.

## Decisión

1. El perfil expone exactamente nueve herramientas de escritorio: `Snapshot`, `Click`, `Type`, `Move`, `Scroll`,
   `Shortcut`, `FocusWindow`, `Wait`, `GetSystemInfo`.
2. Con `WINBRIDGE_STRICT=1` el servidor se niega a arrancar si no se cumple todo: HTTP en streaming, puerto sin
   privilegios, enlace solo a una dirección Tailscale, clave presente, sin Tier 3 y con ese conjunto exacto de
   herramientas.
3. El acceso es solo por Tailscale: sin reenvío de puertos, sin DNS público, sin `0.0.0.0`, sin Funnel ni Serve. El
   firewall deja pasar solo los rangos de Tailscale a los puertos de SSH y de MCP.
4. La administración va por SSH con clave pública y sin contraseña. La clave del MCP se envía por SSH por entrada
   estándar y se guarda en el Credential Manager de Windows; nunca en archivos, argumentos ni registros.

## Consecuencias

- Menos funciones: sin shell ni escritura de archivos, y hay que desbloquear Windows a mano cuando se necesite soporte.
- Un cambio de configuración o un arranque manual no puede ensanchar el perfil: el servidor lo rechaza.
- El agente Go añade herramientas **integradas** (`Diagnose`, trabajos, pasarela) que no cuentan en `tools_count`; así
  el perfil de nueve se sigue pudiendo comprobar. Qué límites tienen esas herramientas está en
  [JOBS.md](../JOBS.md) y [GATEWAY.md](../GATEWAY.md).

## Alternativas

No se documentaron las alternativas que se descartaron. Pendiente de completar por quien tomó la decisión.
