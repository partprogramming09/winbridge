WinBridge - instalacion en el PC con Windows
=============================================

WinBridge permite que la persona de soporte (Mac) vea la pantalla y controle
mouse/teclado SOLO por Tailscale y SOLO con la clave de acceso generada en el
Mac. No abre puertos a Internet, no desactiva Windows Firewall ni Defender y no
configura inicio de sesion automatico.

Que necesitas
-------------
- Windows 10 (1809 o posterior) o Windows 11, 64 bits.
- Una cuenta de administrador del PC y acceso a la ventana de UAC.
- La cuenta de Tailscale del dueno del PC (no la del Mac). Si el Mac no esta
  instalado, el programa lo instala y abre el navegador para iniciar sesion.
- Conexion a Internet durante esta instalacion.
- El archivo ZIP completo entregado por el Mac. No ejecutes solo bootstrap.ps1
  sacado del ZIP: se necesitan las carpetas bootstrap\ y wheels\.

Pasos
-----
1. Verifica el SHA-256 del ZIP contra el valor que te dio el Mac:
     Get-FileHash .\winbridge-0.1.0-bundle.zip -Algorithm SHA256
2. Descomprime el ZIP completo en una carpeta local (por ejemplo, Escritorio).
3. Abre PowerShell como administrador en esa carpeta:
     Click derecho > Abrir PowerShell aqui, o
     Inicio > PowerShell > Click derecho > Ejecutar como administrador.
4. Ejecuta:
     powershell -NoProfile -ExecutionPolicy Bypass -File .\bootstrap.ps1
5. Acepta el UAC. Si Windows pregunta por Tailscale, inicia sesion con la cuenta
   del dueno del PC y confirma en la consola que es la cuenta correcta.
6. Espera. Al final debe aparecer "waiting for pairing" y las direcciones
   Tailscale del PC. El Mac hara el emparejamiento por SSH.

El instalador:
- Instala OpenSSH Server y lo deja con autenticacion por clave, sin passwords.
- Crea reglas de Windows Firewall solo para rangos de Tailscale (TCP 22 y 8091).
- Instala Python 3.12 en el perfil del usuario si no esta.
- Instala el servidor WinBridge con dependencias fijadas por hash, sin Internet
  para los paquetes de Python.
- Crea una tarea al iniciar sesion y un supervisor que espera la clave.
- NO guarda ninguna clave en archivos. La clave de WinBridge se guarda despues
  en el Administrador de credenciales de Windows.

Despues de instalar
-------------------
- El servidor queda esperando el emparejamiento. No hay servidor MCP activo
  hasta que el Mac ejecute "adopt".
- Mientras haya soporte, deja la sesion de Windows desbloqueada. Bloqueada no
  se puede capturar la pantalla ni hacer clic.
- No compartas tu contrasena de Windows. El Mac usa una clave SSH y una clave
  de API que se pueden revocar.
- Para ver el estado (no requiere administrador):
     powershell -NoProfile -ExecutionPolicy Bypass -File "$env:LOCALAPPDATA\winbridge\bin\Test-WinBridgeInstall.ps1"
- Logs:
     %LOCALAPPDATA%\winbridge\logs

Desinstalar
-----------
En PowerShell como administrador:
     powershell -NoProfile -ExecutionPolicy Bypass -File "$env:LOCALAPPDATA\winbridge\bin\Remove-WinBridge.ps1" -Purge -RemoveFirewall

Eso quita la tarea, las reglas de firewall de WinBridge, la clave de API y los
archivos locales. No desinstala Tailscale ni OpenSSH, y no borra la clave SSH
publica del Mac. Para quitar tambien la configuracion de sshd de WinBridge,
agrega -RemoveSshConfig.

Si algo falla
-------------
El instalador se puede volver a ejecutar: es idempotente y conserva el estado.
Revisa %LOCALAPPDATA%\winbridge\logs\bootstrap.log. No envies ese log si contiene
datos personales; no contiene la clave de API.
