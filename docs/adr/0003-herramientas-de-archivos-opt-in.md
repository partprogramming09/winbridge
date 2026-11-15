# 0003. Herramientas de archivos opcionales y acotadas por carpetas

**Lector:** personas
**Estado:** Aceptada (enmienda el ADR 0001 solo en lo que dice aquí)
**Fecha:** 2026-11-14

## Contexto

El [ADR 0001](0001-perfil-estricto-solo-tailscale.md) fija nueve herramientas de escritorio y ninguna lee ni escribe archivos.
Con solo esas nueve, un agente de IA que necesita un archivo del PC tiene que abrir ventanas y teclear mientras lee
capturas: cada paso es una imagen, falla si algo se mueve y no sirve con la sesión bloqueada. Una herramienta de archivos
devuelve el texto exacto en un paso.

El riesgo es real: quien tenga la clave podría leer cualquier archivo de la cuenta del usuario (claves SSH, documentos) o
dejar un archivo que algo ejecute. Ejemplo: sin límites, `ReadFile` sobre la carpeta `.ssh` del usuario entrega su clave
privada.

## Decisión

1. Se añaden tres herramientas: `ListFiles`, `ReadFile` y `WriteFile`. **Solo existen si el dueño del PC configura una tabla
   `[files]`** en `local.toml`; sin ella el agente expone exactamente lo de antes.
2. Están fuera del perfil de nueve: no cuentan en `tools_count`, aparecen en `builtin_tools` de `/status` (como `Processes` y
   las de trabajos) y `winbridge status` sigue diciendo `9 tools`.
3. Cada carpeta la nombra el dueño. Las de lectura y las de escritura son listas aparte; una carpeta de escritura también
   se lee. Una ruta fuera de ellas se rechaza.
4. El agente **se niega a arrancar** con una carpeta peligrosa: una raíz de unidad, el perfil de usuario entero, la carpeta
   de WinBridge, la de trabajos, la de tareas, `.ssh`, `AppData`, las carpetas de Inicio, `C:\Windows`, `Program Files` o un
   directorio del `PATH`.
5. `WriteFile` solo escribe extensiones de datos (lista cerrada, sin forma de ampliarla) y no pisa un archivo existente salvo
   que se pida.
6. Los comandos siguen por `RunTask` y los trabajos. Poder pasar parámetros a `RunTask` queda como una fase aparte, a decidir.

## Alternativas

- **Modo shell libre** (ejecutar cualquier comando): descartada; es lo que el ADR 0001 quería evitar y no se puede acotar.
- **Usar `winremote-mcp` tal cual:** descartada; tiene shell, registro y servicios sin límites.
- **Solo `RunTask`:** descartada como única vía; cada archivo exigiría un script y su hash.

## Consecuencias

- Quien tenga la clave, desde una dirección permitida, alcanza esas carpetas. Las carpetas limitan a un agente engañado, no
  a un atacante que ya controle la cuenta de Windows: esa cuenta ya puede leer los mismos archivos.
- Una carpeta de escritura elegida con descuido (una que algo interprete) sigue siendo un riesgo; `SECURITY.md` lo dice y
  `winbridge doctor` avisa mientras exista una.
- El respaldo Python (`src/winremote`) no tiene estas herramientas y no se rompe por la tabla `[files]`.
- Se mantiene el resto del ADR 0001: solo Tailscale, la misma clave y las mismas reglas de firewall.
