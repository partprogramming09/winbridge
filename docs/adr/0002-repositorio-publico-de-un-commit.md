# 0002. Repositorio público de un solo commit

**Lector:** personas
**Estado:** Aceptada
**Fecha:** 2026-11-14

## Contexto

Antes de abrir WinBridge al público había que decidir qué se publicaba: el código solo o también el historial. El
repositorio de trabajo tenía un historial largo y, dentro de él, tres tipos de datos que no se pueden retirar una vez que
el repositorio es público (cualquiera puede clonarlo y copiarlo antes de que se borre nada):

- correos personales en los autores y mensajes de los commits;
- direcciones de una red privada real (la de la tailnet de quien lo desarrolló);
- hallazgos de un escáner de secretos guardados en el historial.

Ejemplo concreto: si un commit de hace meses contenía una dirección de la tailnet real (en los docs se usa el ejemplo `100.64.1.x`), borrarla en el último
commit no sirve; sigue visible en `git log -p`.

## Decisión

1. **No se publica el historial.** Se creó `partprogramming09/winbridge` como repositorio nuevo con un solo commit, hecho
   con `git archive` del commit final. El autor de ese commit usa una dirección `noreply` de GitHub.
2. **El repositorio anterior se conserva como archivo privado:** pasó a `partprogramming09/winbridge-archive`.
3. **El material de trabajo no entra en el commit público.** `.gitattributes` marca con `export-ignore` las instrucciones
   de trabajo del proyecto, el relevo `docs/estado.md`, `docs/plans` y `docs/research`, y `git archive` los deja fuera.
   Un test (`tests/test_docs_public.py`) comprueba que esas marcas siguen ahí.
4. **Los datos personales se vigilan en dos capas.** `tests/test_no_personal_data.py` escanea el árbol con patrones
   genéricos (por ejemplo, formas de correo o de dirección de red privada). Los identificadores reales concretos no
   pueden estar en el repositorio, ni siquiera en el test; se barren fuera de él antes de cada publicación.
5. **`tools/build_bundle.py` tolera la falta de historial.** Si el commit base de upstream no está en el repositorio,
   avisa y omite esa comprobación. Un clon superficial (`--depth`) sigue fallando, porque ahí la falta de commits es un
   error de quien clonó y no una consecuencia de esta decisión.
6. **`main` está protegida:** sin force-push ni borrado, historial lineal y CI obligatoria.
7. **El release lo publica una automatización, no una persona.** Al etiquetar `v0.1.0`, `.github/workflows/release.yml`
   compila los binarios y publica `SHA256SUMS` y una atestación de procedencia. Cómo se verifica está en
   [operar-winbridge.md](../guides/operar-winbridge.md).

## Alternativas

- **Publicar el historial limpiándolo con reescritura** (`git filter-repo` u otra herramienta). Descartada: hay que
  acertar con cada patrón en cada commit, y un resto olvidado (una rama, una etiqueta, un mensaje) queda público para
  siempre. El coste de equivocarse es irreversible; el de empezar de cero es solo perder historia.
- **Hacer público el repositorio existente.** Descartada: expone tal cual los tres tipos de datos de arriba.

## Consecuencias

- Se pierde la redirección del nombre antiguo: los enlaces al repositorio anterior no llevan al nuevo.
- Las fusiones de upstream (`winremote-mcp`) se hacen en el repositorio archivado, que conserva el commit base, y se
  llevan al público como commits nuevos. La guía [operar-winbridge.md](../guides/operar-winbridge.md) lo explica en
  «Upstream merges».
- Quien clona no ve la historia previa a la publicación; `CHANGELOG.md` es el registro de los cambios anteriores.
- Hay que mantener los tests de `tests/test_docs_public.py` y `tests/test_no_personal_data.py` en la CI: son lo que
  impide que el material de trabajo o un dato personal vuelva a entrar.
