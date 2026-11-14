# Windows Credential Manager access for the WinBridge auth key.
# The key is never written to disk, passed on a command line, or logged.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

if (-not ('WinBridge.CredentialManager' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;

namespace WinBridge {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    public struct CREDENTIAL {
        public uint Flags;
        public uint Type;
        public string TargetName;
        public string Comment;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
        public uint CredentialBlobSize;
        public IntPtr CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public IntPtr Attributes;
        public string TargetAlias;
        public string UserName;
    }

    public static class CredentialManager {
        private const uint CRED_TYPE_GENERIC = 1;
        private const uint CRED_PERSIST_LOCAL_MACHINE = 2;
        private const int ERROR_NOT_FOUND = 1168;

        [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        private static extern bool CredReadW(string target, uint type, uint flags, out IntPtr credential);

        [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        private static extern bool CredWriteW(ref CREDENTIAL credential, uint flags);

        [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        private static extern bool CredDeleteW(string target, uint type, uint flags);

        [DllImport("advapi32.dll")]
        private static extern void CredFree(IntPtr buffer);

        public static string Read(string target) {
            IntPtr pointer;
            if (!CredReadW(target, CRED_TYPE_GENERIC, 0, out pointer)) {
                int error = Marshal.GetLastWin32Error();
                if (error == ERROR_NOT_FOUND) { return null; }
                throw new Win32Exception(error);
            }
            try {
                CREDENTIAL credential = (CREDENTIAL)Marshal.PtrToStructure(pointer, typeof(CREDENTIAL));
                if (credential.CredentialBlobSize == 0) { return string.Empty; }
                return Marshal.PtrToStringUni(credential.CredentialBlob, (int)credential.CredentialBlobSize / 2);
            } finally {
                CredFree(pointer);
            }
        }

        public static string ReadUserName(string target) {
            IntPtr pointer;
            if (!CredReadW(target, CRED_TYPE_GENERIC, 0, out pointer)) {
                int error = Marshal.GetLastWin32Error();
                if (error == ERROR_NOT_FOUND) { return null; }
                throw new Win32Exception(error);
            }
            try {
                CREDENTIAL credential = (CREDENTIAL)Marshal.PtrToStructure(pointer, typeof(CREDENTIAL));
                return credential.UserName;
            } finally {
                CredFree(pointer);
            }
        }

        public static void Write(string target, string userName, string secret) {
            byte[] bytes = Encoding.Unicode.GetBytes(secret);
            IntPtr blob = Marshal.AllocHGlobal(bytes.Length);
            try {
                Marshal.Copy(bytes, 0, blob, bytes.Length);
                CREDENTIAL credential = new CREDENTIAL();
                credential.Type = CRED_TYPE_GENERIC;
                credential.TargetName = target;
                credential.UserName = userName;
                credential.CredentialBlob = blob;
                credential.CredentialBlobSize = (uint)bytes.Length;
                credential.Persist = CRED_PERSIST_LOCAL_MACHINE;
                if (!CredWriteW(ref credential, 0)) {
                    throw new Win32Exception(Marshal.GetLastWin32Error());
                }
            } finally {
                Marshal.FreeHGlobal(blob);
            }
        }

        public static bool Delete(string target) {
            if (CredDeleteW(target, CRED_TYPE_GENERIC, 0)) { return true; }
            int error = Marshal.GetLastWin32Error();
            if (error == ERROR_NOT_FOUND) { return false; }
            throw new Win32Exception(error);
        }
    }
}
'@
}

function Assert-WinBridgeKey {
    param([AllowNull()][string]$Key)
    if ([string]::IsNullOrWhiteSpace($Key)) {
        throw 'The auth key is empty.'
    }
    $value = $Key.Trim()
    if ($value.Length -lt 32 -or $value.Length -gt 512) {
        throw 'The auth key must be between 32 and 512 characters.'
    }
    if ($value -notmatch '^[A-Za-z0-9._~-]+$') {
        throw 'The auth key contains characters outside the reviewed URL-safe set.'
    }
    return $value
}

function Get-WinBridgeCredential {
    param([string]$Target = $script:WinBridgeCredentialTarget)
    $secret = [WinBridge.CredentialManager]::Read($Target)
    if ($null -eq $secret) { return $null }
    $userName = [WinBridge.CredentialManager]::ReadUserName($Target)
    return [pscustomobject]@{
        Target = $Target
        UserName = $userName
        Secret = $secret
    }
}

function Set-WinBridgeCredential {
    param(
        [Parameter(Mandatory = $true)][string]$Secret,
        [string]$Target = $script:WinBridgeCredentialTarget,
        [string]$UserName = $script:WinBridgeCredentialUser
    )
    $value = Assert-WinBridgeKey -Key $Secret
    [WinBridge.CredentialManager]::Write($Target, $UserName, $value)
}

function Remove-WinBridgeCredential {
    param([string]$Target = $script:WinBridgeCredentialTarget)
    return [WinBridge.CredentialManager]::Delete($Target)
}

function Get-WinBridgePendingKeyPath {
    return (Get-WinBridgePath 'state\pending-key.bin')
}

function Set-WinBridgePendingKey {
    param([Parameter(Mandatory = $true)][string]$Secret)
    # Credential Manager is unreachable from an SSH logon session, so the key waits here, protected with the
    # machine DPAPI scope and an owner-only ACL, until the supervisor (an interactive session) stores it.
    Add-Type -AssemblyName System.Security
    $value = Assert-WinBridgeKey -Key $Secret
    $path = Get-WinBridgePendingKeyPath
    Ensure-WinBridgeDirectory -Path (Split-Path -Parent $path)
    $bytes = [Text.Encoding]::UTF8.GetBytes($value)
    $protected = [Security.Cryptography.ProtectedData]::Protect($bytes, $null, [Security.Cryptography.DataProtectionScope]::LocalMachine)
    [IO.File]::WriteAllBytes($path, $protected)
    $userSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    & icacls.exe $path /inheritance:r /grant:r ('*' + $userSid + ':F') '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
        throw 'Could not restrict the pending key file to its owner.'
    }
}

function Import-WinBridgePendingKey {
    $path = Get-WinBridgePendingKeyPath
    if (-not (Test-Path -LiteralPath $path)) { return $false }
    Add-Type -AssemblyName System.Security
    $protected = [IO.File]::ReadAllBytes($path)
    $bytes = [Security.Cryptography.ProtectedData]::Unprotect($protected, $null, [Security.Cryptography.DataProtectionScope]::LocalMachine)
    $secret = [Text.Encoding]::UTF8.GetString($bytes)
    Set-WinBridgeCredential -Secret $secret
    [IO.File]::WriteAllBytes($path, (New-Object byte[] $protected.Length))
    Remove-Item -LiteralPath $path -Force
    return $true
}

function Get-WinBridgeKeyFingerprint {
    param([Parameter(Mandatory = $true)][string]$Secret)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes($Secret)
        $hash = $sha.ComputeHash($bytes)
        $parts = @()
        for ($index = 0; $index -lt 8; $index++) {
            $parts += $hash[$index].ToString('x2')
        }
        return ($parts -join '')
    } finally {
        $sha.Dispose()
    }
}

function Test-WinBridgeCredential {
    param([string]$Target = $script:WinBridgeCredentialTarget)
    $credential = Get-WinBridgeCredential -Target $Target
    if ($null -eq $credential) {
        return [pscustomobject]@{
            Present = $false
            Target = $Target
            UserName = $null
            Fingerprint = $null
        }
    }
    return [pscustomobject]@{
        Present = $true
        Target = $Target
        UserName = $credential.UserName
        Fingerprint = (Get-WinBridgeKeyFingerprint -Secret $credential.Secret)
    }
}
