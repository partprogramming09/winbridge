# End-to-end install test for a Windows runner: builds the bundle, installs it, pairs with a test key, checks the
# server over HTTP and uninstalls. Tailscale is replaced by a stub that reports 100.64.0.1, which is added to the
# loopback adapter so the strict profile can bind a Tailscale-range address.
[CmdletBinding()]
param(
    [int]$Port = 8091,
    [ValidateSet('python', 'go', 'all')][string]$Scenario = 'all'
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$repo = Split-Path -Parent $PSScriptRoot
$work = Join-Path $(if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }) 'wb-e2e'
$serverIp = '100.64.0.1'
$clientIp = '100.64.0.2'
$account = 'ci@example.test'
$root = Join-Path $env:LOCALAPPDATA 'winbridge'

function Step {
    param([string]$Message)
    Write-Host ('[e2e] ' + $Message)
}

function Assert-That {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw ('E2E check failed: ' + $Message) }
    Write-Host ('[ OK ] ' + $Message)
}

function Show-Logs {
    foreach ($name in @('bootstrap.log', 'supervisor.log', 'server.err.log', 'server.out.log')) {
        $path = Join-Path $root ('logs\' + $name)
        if (Test-Path -LiteralPath $path) {
            Write-Host ('---- ' + $name + ' (tail)')
            Get-Content -LiteralPath $path -Tail 25 | ForEach-Object {
                if ($_.Length -gt 220) { $_.Substring(0, 220) } else { $_ }
            }
        }
    }
}

function New-TailscaleStub {
    $dir = Join-Path $env:ProgramFiles 'Tailscale'
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $source = @'
using System;
public static class Stub {
    public static int Main(string[] args) {
        string cmd = string.Join(" ", args);
        if (cmd.StartsWith("ip")) { Console.WriteLine("100.64.0.1"); return 0; }
        if (cmd.StartsWith("status")) {
            Console.WriteLine("{\"BackendState\":\"Running\",\"Self\":{\"Online\":true,\"UserID\":1,\"TailscaleIPs\":[\"100.64.0.1\"]},\"User\":{\"1\":{\"LoginName\":\"ci@example.test\"}}}");
            return 0;
        }
        return 1;
    }
}
'@
    Add-Type -TypeDefinition $source -OutputAssembly (Join-Path $dir 'tailscale.exe') -OutputType ConsoleApplication
}

function Invoke-Http {
    param([string]$Path, [string]$Key, [string]$Method = 'Get', [string]$Body)
    $params = @{
        Uri = ('http://' + $serverIp + ':' + $Port + $Path)
        Method = $Method
        UseBasicParsing = $true
        TimeoutSec = 10
    }
    $headers = @{}
    if ($Key) { $headers['Authorization'] = 'Bearer ' + $Key }
    if ($Body) {
        $headers['Accept'] = 'application/json, text/event-stream'
        $params['ContentType'] = 'application/json'
        $params['Body'] = $Body
    }
    if ($headers.Count -gt 0) { $params['Headers'] = $headers }
    try {
        return Invoke-WebRequest @params
    } catch {
        if ($_.Exception.Response) { return $_.Exception.Response }
        throw
    }
}

function Assert-Parity {
    param([string]$Label)
    if (-not $env:WB_PARITY_EXE) { return }
    $env:WINBRIDGE_KEY = $key
    try {
        $out = & $env:WB_PARITY_EXE --url ('http://' + $serverIp + ':' + $Port) 2>&1 | Out-String
        $code = $LASTEXITCODE
    } finally {
        Remove-Item Env:\WINBRIDGE_KEY -ErrorAction SilentlyContinue
    }
    Write-Host $out.Trim()
    Assert-That ($code -eq 0) ('the ' + $Label + ' conforms to the agent contract')
}

try {
    Step 'Preparing the runner (Tailscale stub, loopback address).'
    New-TailscaleStub
    & netsh.exe interface ipv4 add address 'Loopback Pseudo-Interface 1' $serverIp 255.255.255.255 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not add the Tailscale-range address to the loopback adapter.' }

    if ($Scenario -ne 'go') {
    Step 'Building the bundle.'
    New-Item -ItemType Directory -Path $work -Force | Out-Null
    & python (Join-Path $repo 'tools\build_bundle.py') --allow-no-ssh-key `
        --out-dir (Join-Path $work 'out') --build-dir (Join-Path $work 'build') --wheelhouse (Join-Path $work 'wheels')
    if ($LASTEXITCODE -ne 0) { throw 'build_bundle.py failed.' }
    $zip = Join-Path $work 'out\winbridge-0.1.0-bundle.zip'
    & python (Join-Path $repo 'tools\build_bundle.py') --verify $zip | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'The bundle did not verify.' }
    $bundle = Join-Path $work 'bundle'
    Expand-Archive -LiteralPath $zip -DestinationPath $bundle -Force

    Step 'Installing.'
    $pySw = [Diagnostics.Stopwatch]::StartNew()
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $bundle 'bootstrap.ps1') `
        -NoElevate -NonInteractive -SkipSsh -Port $Port -ClientIp $clientIp -TailscaleAccount $account
    if ($LASTEXITCODE -ne 0) { throw 'bootstrap.ps1 failed.' }
    Write-Host ('[e2e] Python bundle size: ' + (Get-Item -LiteralPath $zip).Length + ' bytes')
    Write-Host ('[e2e] Python install took ' + [int]$pySw.Elapsed.TotalSeconds + ' s')

    $state = Get-Content -LiteralPath (Join-Path $root 'state\state.json') -Raw | ConvertFrom-Json
    Assert-That ($state.port -eq $Port) 'the installed state keeps the requested port'
    Assert-That (@($state.allowed_clients) -contains ($clientIp + '/32')) 'the client address reached the allowlist'
    $config = Get-Content -LiteralPath (Join-Path $root 'config\winbridge.toml') -Raw
    Assert-That ($config -match ('(?m)^port = ' + $Port + '\r?$')) 'the rendered config uses the requested port'

    Step 'Pairing with a test key.'
    $bytes = New-Object byte[] 36
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $key = [Convert]::ToBase64String($bytes).Replace('+', '-').Replace('/', '_').TrimEnd('=')
    $task = Get-ScheduledTask -TaskName 'winbridge-mcp' -ErrorAction SilentlyContinue
    Assert-That ($null -ne $task) 'the supervisor task is registered'
    if ($task.State -ne 'Running') {
        Step 'The runner has no interactive logon for the task; starting the supervisor directly.'
        Start-Process -FilePath powershell.exe -WindowStyle Hidden -ArgumentList @(
            '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $root 'bin\supervisor.ps1'))
    }
    $key | & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Set-WinBridgeKey.ps1') `
        -Json -ClientIp $clientIp -TimeoutSeconds 120 | Out-String | ForEach-Object { Write-Host $_.Substring(0, [Math]::Min(300, $_.Length)) }
    if ($LASTEXITCODE -ne 0) { throw 'Set-WinBridgeKey.ps1 failed.' }

    Step 'Checking the server.'
    $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json
    Assert-That ($health.status -eq 'ok' -and $health.profile -eq 'winbridge') '/health reports the winbridge profile'
    $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
    Assert-That ($status.strict -eq $true -and [int]$status.tools_count -eq 9) '/status is strict with 9 profile tools'
    $anonymous = Invoke-Http -Path '/status'
    Assert-That ([int]$anonymous.StatusCode -eq 401) '/status without the key answers 401'
    $init = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}'
    $mcp = Invoke-Http -Path '/mcp' -Key $key -Method 'Post' -Body $init
    Assert-That ([int]$mcp.StatusCode -eq 200) 'MCP initialize answers 200 with the key'
    Assert-That (-not (Test-Path -LiteralPath (Join-Path $root 'state\pending-key.bin'))) 'no pending key file is left behind'
    Assert-Parity -Label 'Python agent'

    if ($env:WB_GO_AGENT_EXE) {
        $use = Join-Path $root 'bin\Use-WinBridgeAgent.ps1'
        $goSha = (Get-FileHash -LiteralPath $env:WB_GO_AGENT_EXE -Algorithm SHA256).Hash

        Step 'Switching to the Go agent.'
        & powershell -NoProfile -ExecutionPolicy Bypass -File $use -Agent go -ExePath $env:WB_GO_AGENT_EXE `
            -ExpectedSha256 $goSha -Json -TimeoutSeconds 120 | Out-String | ForEach-Object { Write-Host $_.Trim() }
        Assert-That ($LASTEXITCODE -eq 0) 'Use-WinBridgeAgent switched to go'
        $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json
        Assert-That ($health.upstream -eq 'native-go' -and $health.profile -eq 'winbridge') '/health comes from the Go agent'
        $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
        Assert-That ($status.strict -eq $true -and [int]$status.tools_count -eq 9) 'the Go agent is strict with 9 profile tools'
        $anonymous = Invoke-Http -Path '/status'
        Assert-That ([int]$anonymous.StatusCode -eq 401) 'the Go agent answers 401 without the key'
        $mcp = Invoke-Http -Path '/mcp' -Key $key -Method 'Post' -Body $init
        Assert-That ([int]$mcp.StatusCode -eq 200) 'MCP initialize answers 200 on the Go agent'
        $badKey = Invoke-Http -Path '/mcp' -Key 'wrong-key' -Method 'Post' -Body $init
        Assert-That ([int]$badKey.StatusCode -eq 401) 'MCP with a wrong key answers 401 on the Go agent'
        Assert-Parity -Label 'Go agent'

        Step 'Breaking the Go executable: the supervisor must go back to Python.'
        $goExe = Join-Path $root 'app\go\winbridge-agent.exe'
        New-Item -ItemType File -Path (Join-Path $root 'state\maintenance.flag') -Force | Out-Null
        Start-Sleep -Seconds 4
        Set-Content -LiteralPath $goExe -Value 'not an executable'
        Remove-Item -LiteralPath (Join-Path $root 'state\maintenance.flag') -Force
        $deadline = (Get-Date).AddSeconds(120)
        $agentNow = 'go'
        while ((Get-Date) -lt $deadline -and $agentNow -ne 'python') {
            Start-Sleep -Seconds 3
            $agentNow = (Get-Content -LiteralPath (Join-Path $root 'state\agent.txt') -Raw).Trim()
        }
        Assert-That ($agentNow -eq 'python') 'a broken Go executable makes the supervisor select python again'
        $deadline = (Get-Date).AddSeconds(120)
        $health = $null
        while ((Get-Date) -lt $deadline -and $null -eq $health) {
            Start-Sleep -Seconds 3
            try { $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json } catch { $health = $null }
            if ($null -ne $health -and $health.upstream -ne '0.4.24') { $health = $null }
        }
        Assert-That ($null -ne $health) 'the Python agent answers /health after the fallback'
    }

    Step 'Uninstalling.'
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Remove-WinBridge.ps1') -Purge -RemoveFirewall
    Assert-That (-not (Test-Path -LiteralPath $root)) 'the install folder is gone after the uninstall'
    }
    if ($env:WB_GO_AGENT_EXE -and $Scenario -ne 'python') {
        New-Item -ItemType Directory -Path $work -Force | Out-Null
        Step 'Building a Go-only bundle (no Python, no wheels).'
        $goOut = Join-Path $work 'go-out'
        & python (Join-Path $repo 'tools\build_bundle.py') --go-agent $env:WB_GO_AGENT_EXE --allow-no-ssh-key `
            --out-dir $goOut --build-dir (Join-Path $work 'go-build')
        if ($LASTEXITCODE -ne 0) { throw 'build_bundle.py --go-agent failed.' }
        $goZip = Join-Path $goOut 'winbridge-0.1.0-bundle.zip'
        & python (Join-Path $repo 'tools\build_bundle.py') --verify $goZip | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'The Go bundle did not verify.' }
        Write-Host ('[e2e] Go bundle size: ' + (Get-Item -LiteralPath $goZip).Length + ' bytes')
        $goBundle = Join-Path $work 'go-bundle'
        Expand-Archive -LiteralPath $goZip -DestinationPath $goBundle -Force

        Step 'Installing the Go-only bundle.'
        $sw = [Diagnostics.Stopwatch]::StartNew()
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $goBundle 'bootstrap.ps1') `
            -NoElevate -NonInteractive -SkipSsh -Port $Port -ClientIp $clientIp -TailscaleAccount $account
        if ($LASTEXITCODE -ne 0) { throw 'bootstrap.ps1 failed on the Go bundle.' }
        Write-Host ('[e2e] Go install took ' + [int]$sw.Elapsed.TotalSeconds + ' s')
        Assert-That (-not (Test-Path -LiteralPath (Join-Path $root 'app\venv'))) 'the Go install has no Python environment'
        Assert-That (Test-Path -LiteralPath (Join-Path $root 'app\go\winbridge-agent.exe')) 'the Go agent is installed'
        Assert-That ((Get-Content -LiteralPath (Join-Path $root 'state\agent.txt') -Raw).Trim() -eq 'go') 'the install selects the Go agent'

        Step 'Pairing and checking the Go-only install.'
        $bytes = New-Object byte[] 36
        [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
        $key = [Convert]::ToBase64String($bytes).Replace('+', '-').Replace('/', '_').TrimEnd('=')
        $task = Get-ScheduledTask -TaskName 'winbridge-mcp' -ErrorAction SilentlyContinue
        if ($task.State -ne 'Running') {
            Start-Process -FilePath powershell.exe -WindowStyle Hidden -ArgumentList @(
                '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $root 'bin\supervisor.ps1'))
        }
        $key | & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Set-WinBridgeKey.ps1') `
            -Json -ClientIp $clientIp -TimeoutSeconds 120 | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Set-WinBridgeKey.ps1 failed on the Go install.' }
        $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json
        Assert-That ($health.upstream -eq 'native-go' -and $health.profile -eq 'winbridge') '/health comes from the Go agent'
        $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
        Assert-That ($status.strict -eq $true -and [int]$status.tools_count -eq 9) 'the Go-only install is strict with 9 profile tools'
        $anonymous = Invoke-Http -Path '/status'
        Assert-That ([int]$anonymous.StatusCode -eq 401) 'the Go-only install answers 401 without the key'
        Assert-Parity -Label 'Go-only install'
        Assert-That ((Get-Acl -LiteralPath $root).AreAccessRulesProtected) 'the install root does not inherit permissions from its parent'
        Assert-That (@($status.builtin_tools) -contains 'Diagnose') '/status lists Diagnose as a built-in tool'
        $record = Get-Content -LiteralPath (Join-Path $root 'state\last-install.json') -Raw | ConvertFrom-Json
        $bundleExeSha = (Get-FileHash -LiteralPath (Join-Path $goBundle 'agent\winbridge-agent.exe') -Algorithm SHA256).Hash.ToLower()
        Assert-That ($record.ok -eq $true -and $record.exe_sha256 -eq $bundleExeSha) 'last-install.json records a successful install of this exe'
        if ($env:WB_CALL_EXE) {
            $env:WINBRIDGE_KEY = $key
            try {
                $diagOut = & $env:WB_CALL_EXE --url ('http://' + $serverIp + ':' + $Port + '/mcp') --tool Diagnose 2>&1 | Out-String
            } finally {
                Remove-Item Env:\WINBRIDGE_KEY -ErrorAction SilentlyContinue
            }
            Write-Host ($diagOut.Substring(0, [Math]::Min(1800, $diagOut.Length)) -replace [regex]::Escape($key), '[key]')
            Assert-That ($diagOut -match $bundleExeSha) 'Diagnose reports the SHA-256 of the exe in the bundle'
            Assert-That ($diagOut -match '"last_install"' -and $diagOut -match '"locked": false') 'Diagnose reports the last install and an unlocked session'
            Assert-That ($diagOut -notmatch $key) 'Diagnose never returns the auth key'
        }

        if ($env:WB_CALL_EXE -and $env:WB_GW_STUB_EXE) {
            Step 'Adding an MCP server through the gateway.'
            $stubDir = Join-Path $root 'servers\gw-stub'
            New-Item -ItemType Directory -Path $stubDir -Force | Out-Null
            Copy-Item -LiteralPath $env:WB_GW_STUB_EXE -Destination (Join-Path $stubDir 'gw-stub.exe') -Force
            $setOut = & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Set-WinBridgeServer.ps1') `
                -Name gw-stub -Command (Join-Path $stubDir 'gw-stub.exe') -AllowTools sleep -DenyArgs 'sleep=forbidden' -Json | Out-String
            Assert-That ($LASTEXITCODE -eq 0) ('Set-WinBridgeServer.ps1 succeeds: ' + $setOut.Trim())
            $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
            Assert-That ([int]$status.tools_count -eq 9 -and @($status.builtin_tools) -contains 'CallServerTool') 'the gateway tools are built-in and the strict count stays 9'
            $env:WINBRIDGE_KEY = $key
            try {
                $mcp = 'http://' + $serverIp + ':' + $Port + '/mcp'
                $listOut = & $env:WB_CALL_EXE --url $mcp --tool ListServers 2>&1 | Out-String
                Assert-That ($listOut -match 'gw-stub: sleep') 'ListServers shows the server and its allowed tool'
                $argsFile = Join-Path $work 'gw-args.json'
                Set-Content -LiteralPath $argsFile -Value '{"server":"gw-stub","tool":"sleep","arguments":{"seconds":2,"every":1}}' -Encoding ascii
                $callOut = & $env:WB_CALL_EXE --url $mcp --tool CallServerTool --args-file $argsFile --progress 2>&1 | Out-String
                Assert-That ($callOut -match 'slept 2s, progress token true' -and $callOut -match 'progress \+') 'CallServerTool returns the answer and forwards progress'
                Set-Content -LiteralPath $argsFile -Value '{"server":"gw-stub","tool":"other"}' -Encoding ascii
                $badOut = & $env:WB_CALL_EXE --url $mcp --tool CallServerTool --args-file $argsFile 2>&1 | Out-String
                Assert-That ($badOut -match 'isError: true' -and $badOut -match 'not allowed') 'a tool outside allow_tools is refused'
                Set-Content -LiteralPath $argsFile -Value '{"server":"gw-stub","tool":"sleep","arguments":{"seconds":1,"forbidden":"x"}}' -Encoding ascii
                $denyOut = & $env:WB_CALL_EXE --url $mcp --tool CallServerTool --args-file $argsFile 2>&1 | Out-String
                Assert-That ($denyOut -match 'isError: true' -and $denyOut -match 'forbidden') 'an argument listed in deny_args is refused'
            } finally {
                Remove-Item Env:\WINBRIDGE_KEY -ErrorAction SilentlyContinue
            }
            Assert-That (Test-Path -LiteralPath (Join-Path $root 'config\local.toml')) 'the server entry lives in local.toml'
        }

        if ($env:WB_CALL_EXE -and $env:WB_GW_STUB_EXE) {
            Step 'A server whose folder any user can write must not start.'
            $openDir = Join-Path $root 'servers\gw-open'
            New-Item -ItemType Directory -Path $openDir -Force | Out-Null
            Copy-Item -LiteralPath $env:WB_GW_STUB_EXE -Destination (Join-Path $openDir 'gw-open.exe') -Force
            & icacls $openDir /grant '*S-1-5-32-545:(OI)(CI)(W)' | Out-Null
            & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Set-WinBridgeServer.ps1') `
                -Name gw-open -Command (Join-Path $openDir 'gw-open.exe') -AllowTools sleep -Json | Out-Null
            Assert-That ($LASTEXITCODE -eq 0) 'Set-WinBridgeServer.ps1 adds the open server'
            $env:WINBRIDGE_KEY = $key
            try {
                $argsFile = Join-Path $work 'gw-open.json'
                Set-Content -LiteralPath $argsFile -Value '{"server":"gw-open","tool":"sleep","arguments":{"seconds":1}}' -Encoding ascii
                $openOut = & $env:WB_CALL_EXE --url ('http://' + $serverIp + ':' + $Port + '/mcp') --tool CallServerTool --args-file $argsFile 2>&1 | Out-String
                Assert-That ($openOut -match 'isError: true' -and $openOut -match 'can be changed by Users') 'a server in a folder any user can write is refused'
            } finally {
                Remove-Item Env:\WINBRIDGE_KEY -ErrorAction SilentlyContinue
            }
            & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Set-WinBridgeServer.ps1') -Name gw-open -Remove -Json | Out-Null
            Assert-That ($LASTEXITCODE -eq 0) 'Set-WinBridgeServer.ps1 removes the open server'
        }

        if ($env:WB_CALL_EXE) {
            Step 'Opt-in file tools: reading inside one folder, refusing everything that escapes it.'
            $filesRoot = 'C:\wb-files-ci'
            Remove-Item -LiteralPath $filesRoot -Recurse -Force -ErrorAction SilentlyContinue
            New-Item -ItemType Directory -Path (Join-Path $filesRoot 'ok') -Force | Out-Null
            Set-Content -LiteralPath (Join-Path $filesRoot 'ok\a.txt') -Value 'hello-from-ci' -Encoding ascii
            Set-Content -LiteralPath (Join-Path $filesRoot 'secret.txt') -Value 'top-secret-ci' -Encoding ascii
            & cmd /c mklink /J (Join-Path $filesRoot 'ok\jn') $filesRoot | Out-Null
            $setter = Join-Path $root 'bin\Set-WinBridgeFiles.ps1'
            & powershell -NoProfile -ExecutionPolicy Bypass -File $setter -ReadRoot 'C:\' -Json | Out-Null
            Assert-That ($LASTEXITCODE -ne 0) 'a drive root is refused as a file folder'
            Assert-That ((Get-Content -LiteralPath (Join-Path $root 'config\local.toml') -Raw) -notmatch '\[files\]') 'the refused [files] table was rolled back out of local.toml'
            $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
            Assert-That (@($status.builtin_tools) -notcontains 'ReadFile') 'without [files] the file tools do not exist'
            $setOut = & powershell -NoProfile -ExecutionPolicy Bypass -File $setter -ReadRoot (Join-Path $filesRoot 'ok') -Json | Out-String
            Assert-That ($LASTEXITCODE -eq 0) ('Set-WinBridgeFiles.ps1 succeeds: ' + $setOut.Trim())
            $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
            Assert-That ([int]$status.tools_count -eq 9 -and @($status.builtin_tools) -contains 'ReadFile' -and @($status.builtin_tools) -contains 'ListFiles') 'the file tools are built-in and the strict count stays 9'
            $env:WINBRIDGE_KEY = $key
            try {
                $mcp = 'http://' + $serverIp + ':' + $Port + '/mcp'
                $argsFile = Join-Path $work 'files-args.json'
                function Invoke-FileTool([string]$Tool, [string]$Path, [hashtable]$Extra = @{}) {
                    $toolArgs = @{ path = $Path }
                    foreach ($k in $Extra.Keys) { $toolArgs[$k] = $Extra[$k] }
                    Set-Content -LiteralPath $argsFile -Value ($toolArgs | ConvertTo-Json -Compress) -Encoding ascii
                    return (& $env:WB_CALL_EXE --url $mcp --tool $Tool --args-file $argsFile 2>&1 | Out-String)
                }
                $okOut = Invoke-FileTool 'ReadFile' (Join-Path $filesRoot 'ok\a.txt')
                Assert-That ($okOut -match 'hello-from-ci' -and $okOut -notmatch 'isError: true') 'ReadFile returns a file inside the folder'
                $listOut = Invoke-FileTool 'ListFiles' (Join-Path $filesRoot 'ok')
                Assert-That ($listOut -match 'a\.txt') 'ListFiles shows the file'
                $escapes = [ordered]@{
                    'a .. escape'                 = (Join-Path $filesRoot 'ok\..\secret.txt')
                    'an alternate data stream'    = (Join-Path $filesRoot 'ok\a.txt:s')
                    'a device name'               = (Join-Path $filesRoot 'ok\NUL')
                    'a junction pointing outside' = (Join-Path $filesRoot 'ok\jn\secret.txt')
                    'a sibling folder'            = (Join-Path $filesRoot 'secret.txt')
                }
                foreach ($name in $escapes.Keys) {
                    $out = Invoke-FileTool 'ReadFile' $escapes[$name]
                    Assert-That ($out -match 'isError: true' -and $out -notmatch 'top-secret-ci') ('ReadFile refuses ' + $name)
                }

                # Write folder: a data file goes in, nothing is replaced unless asked, no script goes in, and the doctor says it is open.
                $outRoot = Join-Path $filesRoot 'out'
                New-Item -ItemType Directory -Path $outRoot -Force | Out-Null
                $setOut = & powershell -NoProfile -ExecutionPolicy Bypass -File $setter -ReadRoot (Join-Path $filesRoot 'ok') -WriteRoot $outRoot -Json | Out-String
                Assert-That ($LASTEXITCODE -eq 0) ('Set-WinBridgeFiles.ps1 -WriteRoot succeeds: ' + $setOut.Trim())
                $w1 = Invoke-FileTool 'WriteFile' (Join-Path $outRoot 'note.txt') @{ content = 'written-by-ci' }
                Assert-That ($w1 -match 'wrote 13 bytes' -and $w1 -notmatch 'isError: true') ('WriteFile creates a file in the write folder: ' + $w1.Trim())
                Assert-That ((Get-Content -LiteralPath (Join-Path $outRoot 'note.txt') -Raw).Trim() -eq 'written-by-ci') 'the file on disk has the written content'
                $w2 = Invoke-FileTool 'WriteFile' (Join-Path $outRoot 'note.txt') @{ content = 'second' }
                Assert-That ($w2 -match 'isError: true' -and $w2 -match 'already exists') 'WriteFile does not replace a file unless overwrite=true'
                Assert-That ((Get-Content -LiteralPath (Join-Path $outRoot 'note.txt') -Raw).Trim() -eq 'written-by-ci') 'the refused write left the file alone'
                $w3 = Invoke-FileTool 'WriteFile' (Join-Path $outRoot 'note.txt') @{ content = 'second'; overwrite = $true }
                Assert-That ($w3 -notmatch 'isError: true' -and (Get-Content -LiteralPath (Join-Path $outRoot 'note.txt') -Raw).Trim() -eq 'second') 'WriteFile with overwrite=true replaces it'
                foreach ($bad in @('run.ps1', 'run.bat', 'noext', 'note.txt.exe')) {
                    $w = Invoke-FileTool 'WriteFile' (Join-Path $outRoot $bad) @{ content = 'x' }
                    Assert-That ($w -match 'isError: true' -and -not (Test-Path -LiteralPath (Join-Path $outRoot $bad))) ('WriteFile refuses ' + $bad)
                }
                $w4 = Invoke-FileTool 'WriteFile' (Join-Path $filesRoot 'ok\b.txt') @{ content = 'x' }
                Assert-That ($w4 -match 'isError: true' -and $w4 -match 'read-only' -and -not (Test-Path -LiteralPath (Join-Path $filesRoot 'ok\b.txt'))) 'WriteFile refuses a read-only folder'
                $w5 = Invoke-FileTool 'WriteFile' (Join-Path $outRoot 'a.txt:s') @{ content = 'x' }
                Assert-That ($w5 -match 'isError: true') 'WriteFile refuses an alternate data stream'
                & cmd /c mklink /J (Join-Path $outRoot 'jn') $filesRoot | Out-Null
                $w6 = Invoke-FileTool 'WriteFile' (Join-Path $outRoot 'jn\escaped.txt') @{ content = 'x' }
                Assert-That ($w6 -match 'isError: true' -and -not (Test-Path -LiteralPath (Join-Path $filesRoot 'escaped.txt'))) 'WriteFile refuses to write through a junction that points outside the write folder'
                & cmd /c rmdir (Join-Path $outRoot 'jn') | Out-Null
                $doctorJson = (& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Test-WinBridgeInstall.ps1') -Json | Out-String)
                $doctor = $doctorJson | ConvertFrom-Json
                $fw = @($doctor.checks | Where-Object { $_.name -eq 'files_write' })
                Assert-That ($fw.Count -eq 1 -and $fw[0].status -eq 'WARN' -and $fw[0].detail -match 'write access is enabled for') 'doctor warns while a write folder exists'
            } finally {
                Remove-Item Env:\WINBRIDGE_KEY -ErrorAction SilentlyContinue
            }
            & powershell -NoProfile -ExecutionPolicy Bypass -File $setter -Remove -Json | Out-Null
            Assert-That ($LASTEXITCODE -eq 0) 'Set-WinBridgeFiles.ps1 removes the table'
            $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
            Assert-That (@($status.builtin_tools) -notcontains 'ReadFile') 'removing [files] removes the file tools'
            & cmd /c rmdir (Join-Path $filesRoot 'ok\jn') | Out-Null
            Remove-Item -LiteralPath $filesRoot -Recurse -Force -ErrorAction SilentlyContinue
        }

        Step 'Restarting the Go agent through the supervisor.'
        $pidBefore = (Get-Content -LiteralPath (Join-Path $root 'state\server.pid') -Raw).Trim()
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Restart-WinBridge.ps1') -Json | Out-Null
        Assert-That ($LASTEXITCODE -eq 0) 'Restart-WinBridge.ps1 succeeds'
        $pidAfter = (Get-Content -LiteralPath (Join-Path $root 'state\server.pid') -Raw).Trim()
        Assert-That ($pidAfter -ne $pidBefore) 'the restart replaced the server process'

        Step 'Reinstalling over the running Go agent.'
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $goBundle 'bootstrap.ps1') `
            -NoElevate -NonInteractive -SkipSsh -Port $Port -ClientIp $clientIp -TailscaleAccount $account
        Assert-That ($LASTEXITCODE -eq 0) 'reinstalling over a running Go agent succeeds'
        $bundleExeSha = (Get-FileHash -LiteralPath (Join-Path $goBundle 'agent\winbridge-agent.exe') -Algorithm SHA256).Hash
        $installedSha = (Get-FileHash -LiteralPath (Join-Path $root 'app\go\winbridge-agent.exe') -Algorithm SHA256).Hash
        Assert-That ($installedSha -eq $bundleExeSha) 'the installed Go agent is the one in the bundle'
        $deadline = (Get-Date).AddSeconds(90)
        $health = $null
        while ((Get-Date) -lt $deadline -and $null -eq $health) {
            Start-Sleep -Seconds 3
            try { $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json } catch { $health = $null }
        }
        Assert-That ($null -ne $health -and $health.upstream -eq 'native-go') 'the Go agent answers /health after the reinstall'

        Step 'Updating in place from a new bundle, without reinstalling.'
        $recordBefore = Get-Content -LiteralPath (Join-Path $root 'state\last-install.json') -Raw | ConvertFrom-Json
        $updOut = Join-Path $work 'upd-out'
        & python (Join-Path $repo 'tools\build_bundle.py') --go-agent $env:WB_GO_AGENT_EXE --allow-no-ssh-key `
            --out-dir $updOut --build-dir (Join-Path $work 'upd-build')
        if ($LASTEXITCODE -ne 0) { throw 'build_bundle.py failed for the update bundle.' }
        $updZip = Join-Path $updOut 'winbridge-0.1.0-bundle.zip'
        $updSha = (Get-FileHash -LiteralPath $updZip -Algorithm SHA256).Hash.ToLower()
        $updText = & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Update-WinBridgeGo.ps1') `
            -BundlePath $updZip -Sha256 $updSha -Json -TimeoutSeconds 90 | Out-String
        $upd = $updText | ConvertFrom-Json
        Write-Host ('[e2e] update result: ' + $updText.Trim())
        Assert-That ($upd.ok -eq $true -and -not $upd.rolled_back) 'the in-place update succeeds'
        Assert-That ($upd.build_id -ne $recordBefore.build_id) 'the update installed the new build'
        $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json
        Assert-That ($health.upstream -eq 'native-go') 'the agent answers /health after the update'

        if ($env:WB_GW_STUB_EXE) {
            $status = (Invoke-Http -Path '/status' -Key $key).Content | ConvertFrom-Json
            Assert-That (@($status.builtin_tools) -contains 'CallServerTool') 'the server entry survived the in-place update'
        }

        Step 'An update whose agent does not start must put the previous agent back.'
        $badExe = Join-Path $work 'bad-agent.exe'
        Copy-Item -LiteralPath (Join-Path $env:SystemRoot 'System32\whoami.exe') -Destination $badExe
        $badOut = Join-Path $work 'bad-out'
        & python (Join-Path $repo 'tools\build_bundle.py') --go-agent $badExe --allow-no-ssh-key `
            --out-dir $badOut --build-dir (Join-Path $work 'bad-build')
        if ($LASTEXITCODE -ne 0) { throw 'build_bundle.py failed for the bad bundle.' }
        $badZip = Join-Path $badOut 'winbridge-0.1.0-bundle.zip'
        $badSha = (Get-FileHash -LiteralPath $badZip -Algorithm SHA256).Hash.ToLower()
        $installedSha = (Get-FileHash -LiteralPath (Join-Path $root 'app\go\winbridge-agent.exe') -Algorithm SHA256).Hash.ToLower()
        $badText = & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Update-WinBridgeGo.ps1') `
            -BundlePath $badZip -Sha256 $badSha -Json -TimeoutSeconds 25 | Out-String
        $bad = $badText | ConvertFrom-Json
        Write-Host ('[e2e] bad update result: ' + $badText.Trim())
        Assert-That ($bad.ok -eq $false -and $bad.rolled_back -eq $true) 'a failed update reports the rollback'
        $afterSha = (Get-FileHash -LiteralPath (Join-Path $root 'app\go\winbridge-agent.exe') -Algorithm SHA256).Hash.ToLower()
        Assert-That ($afterSha -eq $installedSha) 'the previous agent is back after the failed update'
        $deadline = (Get-Date).AddSeconds(90)
        $health = $null
        while ((Get-Date) -lt $deadline -and $null -eq $health) {
            Start-Sleep -Seconds 3
            try { $health = (Invoke-Http -Path '/health').Content | ConvertFrom-Json } catch { $health = $null }
        }
        Assert-That ($null -ne $health -and $health.upstream -eq 'native-go') 'the agent answers /health after the rollback'

        Step 'Uninstalling the Go-only install.'
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root 'bin\Remove-WinBridge.ps1') -Purge -RemoveFirewall
        Assert-That (-not (Test-Path -LiteralPath $root)) 'the Go-only install folder is gone after the uninstall'
    }
    Write-Host '[e2e] PASSED'
} catch {
    Write-Host ('[e2e] FAILED: ' + $_.Exception.Message)
    Show-Logs
    exit 1
}
