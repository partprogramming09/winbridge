"""Contract tests for the WinBridge bootstrap, bundle and security invariants.

These tests are intentionally read-only. They validate the committed template,
hash lock, upstream pin, PowerShell contracts and repository hygiene without
installing packages, touching services, or reading real credentials.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover - Python 3.10
    import tomli as tomllib

from winremote import PROFILE_NAME, UPSTREAM_BASE, __version__
from winremote.tiers import WINBRIDGE_PROFILE

REPO_ROOT = Path(__file__).resolve().parents[1]
BOOTSTRAP = REPO_ROOT / "bootstrap"
TEMPLATE = BOOTSTRAP / "config" / "winbridge.toml.template"
LOCK = BOOTSTRAP / "requirements" / "windows-py312.lock.txt"
CONSTRAINTS = BOOTSTRAP / "requirements" / "constraints.txt"
UPSTREAM = REPO_ROOT / "upstream.json"

EXPECTED_TOOLS = {
    "Snapshot",
    "Click",
    "Type",
    "Move",
    "Scroll",
    "Shortcut",
    "FocusWindow",
    "Wait",
    "GetSystemInfo",
}
EXPECTED_UPSTREAM_COMMIT = "f177b74dfb1973d432054603bcab608def826b82"
EXPECTED_ARTIFACT_HASHES = {
    "winremote_mcp-0.4.24-py3-none-any.whl": "a5d881503baaf01a56e64319709743f3aaa404ecb2b590a483c78dc2072be28d",
    "winremote_mcp-0.4.24.tar.gz": "1225c3529f914d1fecb9259d1598839b38e920f0ce51a35a4db016c8ccb916d6",
}
PIN_RE = re.compile(r"^([A-Za-z0-9_.-]+)==([^\s\\;]+)")
HASH_RE = re.compile(r"^--hash=sha256:([0-9a-f]{64})$")


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _render_template() -> str:
    return (
        _read(TEMPLATE)
        .replace("{{TAILSCALE_IP}}", "100.64.1.2")
        .replace("{{PORT}}", "8090")
        .replace("{{ALLOWED_CLIENTS}}", '"100.64.1.2", "100.64.1.3/32"')
    )


def _parse_lock() -> dict[str, dict[str, object]]:
    text = _read(LOCK)
    pins: dict[str, dict[str, object]] = {}
    current: str | None = None
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.endswith("\\"):
            line = line[:-1].strip()
        match = PIN_RE.match(line)
        if match:
            current = match.group(1)
            pins[current] = {"version": match.group(2), "hashes": []}
            continue
        hash_match = HASH_RE.match(line)
        if hash_match and current is not None:
            hashes = pins[current]["hashes"]
            assert isinstance(hashes, list)
            hashes.append(hash_match.group(1))
            continue
        raise AssertionError(f"unexpected lock line: {line!r}")
    return pins


def test_version_and_upstream_contract():
    assert __version__ == "0.1.0"
    assert UPSTREAM_BASE == "0.4.24"
    assert PROFILE_NAME == "winbridge"
    assert set(WINBRIDGE_PROFILE) == EXPECTED_TOOLS
    assert len(WINBRIDGE_PROFILE) == 9


def test_upstream_json_pins_the_reviewed_base():
    data = json.loads(_read(UPSTREAM))
    assert data["project"] == "winremote-mcp"
    assert data["base_version"] == "0.4.24"
    assert data["base_tag"] == "v0.4.24"
    assert data["base_commit"] == EXPECTED_UPSTREAM_COMMIT
    artifacts = {entry["filename"]: entry["sha256"] for entry in data["artifacts"].values()}
    assert artifacts == EXPECTED_ARTIFACT_HASHES


def test_template_renders_to_the_reviewed_profile():
    rendered = _render_template()
    assert "{{" not in rendered
    config = tomllib.loads(rendered)
    assert set(config["tools"]["enable"]) == EXPECTED_TOOLS
    assert len(config["tools"]["enable"]) == 9
    assert config["tools"]["exclude"] == []
    assert config["server"]["host"] == "100.64.1.2"
    assert config["server"]["port"] == 8090
    assert config["server"]["allow_insecure_remote"] is False
    assert config["security"]["enable_tier3"] is False
    assert config["security"]["disable_tier2"] is False
    assert config["security"]["ip_allowlist"] == ["100.64.1.2", "100.64.1.3/32"]
    assert not re.search(r"(?im)^\s*auth[_-]?key\s*=", rendered)
    assert not re.search(r"WINREMOTE_AUTH_KEY\s*=", rendered)


def test_template_has_one_of_each_placeholder():
    template = _read(TEMPLATE)
    for placeholder in ("{{TAILSCALE_IP}}", "{{PORT}}", "{{ALLOWED_CLIENTS}}"):
        assert template.count(placeholder) == 1


def test_lock_is_hash_pinned_and_excludes_the_project():
    text = _read(LOCK)
    assert "winbridge" not in text.lower()
    assert "winremote-mcp" not in text.lower()
    assert "--index-url" not in text
    assert "--extra-index-url" not in text
    pins = _parse_lock()
    assert len(pins) == 87
    assert pins["fastmcp"]["version"] == "3.4.11"
    for name, entry in pins.items():
        hashes = entry["hashes"]
        assert isinstance(hashes, list) and hashes, f"{name} has no SHA-256 hash"
        assert all(re.fullmatch(r"[0-9a-f]{64}", value) for value in hashes)


def test_fastmcp_stays_below_version_4():
    constraints = _read(CONSTRAINTS)
    assert "fastmcp>=3.2.0,<4" in constraints
    pyproject = tomllib.loads(_read(REPO_ROOT / "pyproject.toml"))
    assert pyproject["project"]["name"] == "winbridge-mcp"
    assert pyproject["project"]["version"] == "0.1.0"
    fastmcp = [dep for dep in pyproject["project"]["dependencies"] if dep.startswith("fastmcp")]
    assert fastmcp == ["fastmcp>=3.2.0,<4"]
    assert pyproject["project"]["scripts"] == {"winbridge-mcp": "winremote.__main__:cli"}


def test_bootstrap_never_widens_the_profile():
    powershell = sorted(BOOTSTRAP.rglob("*.ps1"))
    assert BOOTSTRAP.joinpath("bootstrap.ps1").exists()
    assert len(powershell) >= 18
    for path in powershell:
        text = _read(path)
        assert "--enable-all" not in text, path
        assert "--enable-tier3" not in text, path
        assert "--auth-key" not in text, path
        assert "--host 0.0.0.0" not in text, f"{path} must not bind the wildcard address"
        assert 'host = "0.0.0.0"' not in text, f"{path} must not write a wildcard bind"


def test_supervisor_keeps_the_key_out_of_argv_and_enables_strict_mode():
    text = _read(BOOTSTRAP / "scripts" / "supervisor.ps1")
    assert "'--transport', 'streamable-http'" in text
    assert "'--host'" in text
    assert "'--config'" in text
    assert "--auth-key" not in text
    assert "WINREMOTE_AUTH_KEY" in text
    assert "WINBRIDGE_STRICT = '1'" in text
    start = text.index("$arguments = @(")
    end = text.index("$env:WINREMOTE_AUTH_KEY", start)
    argv = text[start:end]
    assert "$key" not in argv
    assert "auth" not in argv.lower()


def test_bootstrap_enforces_firewall_ssh_python_and_manifest_contracts():
    text = _read(BOOTSTRAP / "bootstrap.ps1")
    assert "Assert-WinBridgeNoBroadPortRules" in text
    assert "Install-WinBridgeAuthorizedKey" in text
    assert "Install-WinBridgePythonPackages" in text
    assert "manifest.json" in text
    assert "Test-WinBridgeAdmin" in text
    assert "Register-WinBridgeTask" in text
    for forbidden in ("AutoAdminLogon", "DefaultPassword", "net user", "netsh advfirewall set"):
        assert forbidden not in text
    common = _read(BOOTSTRAP / "lib" / "Common.ps1")
    assert "100.64.0.0/10" in common
    assert "fd7a:115c:a1e0::/48" in common


def test_sshd_config_is_key_only():
    text = _read(BOOTSTRAP / "lib" / "OpenSsh.ps1")
    for setting in (
        "PubkeyAuthentication yes",
        "PasswordAuthentication no",
        "KbdInteractiveAuthentication no",
        "PermitRootLogin no",
        "PermitUserEnvironment no",
        "AllowAgentForwarding no",
        "AllowTcpForwarding no",
        "GatewayPorts no",
        "PermitTunnel no",
        "X11Forwarding no",
        "StrictModes yes",
    ):
        assert setting in text
    assert "administrators_authorized_keys" in text
    assert "icacls.exe" in text
    assert "PRIVATE KEY" in text


def test_key_reaches_credential_manager_through_the_supervisor():
    creds = _read(BOOTSTRAP / "lib" / "CredentialManager.ps1")
    assert "LocalMachine" in creds and "Import-WinBridgePendingKey" in creds
    assert "/inheritance:r" in creds
    setter = _read(BOOTSTRAP / "scripts" / "Set-WinBridgeKey.ps1")
    assert "Set-WinBridgePendingKey" in setter
    supervisor = _read(BOOTSTRAP / "scripts" / "supervisor.ps1")
    assert supervisor.index("Import-WinBridgePendingKey") < supervisor.index("$credential = Get-WinBridgeCredential")


def test_doctor_does_not_fail_what_an_ssh_session_cannot_see():
    doctor = _read(BOOTSTRAP / "lib" / "Doctor.ps1")
    assert "$credentialUnreadable = [bool]$env:SSH_CONNECTION" in doctor
    assert "elseif ($credentialUnreadable)" in doctor
    assert doctor.count("elseif ($credentialUnreadable)") == 2



def test_doctor_skips_what_an_ssh_session_cannot_see_instead_of_warning():
    doctor = _read(BOOTSTRAP / "lib" / "Doctor.ps1")
    assert "if ($env:SSH_CONNECTION) { 'SKIP' } else { 'FAIL' }" in doctor
    assert "-Name 'authenticated_status' -Status 'SKIP' -Detail 'skipped: no Credential Manager" in doctor
    # A real problem keeps its WARN: pairing not done yet.
    assert "-Name 'credential' -Status 'WARN' -Detail 'not paired yet" in doctor


def test_doctor_does_not_warn_about_the_session_lock_from_inside_ssh():
    doctor = _read(BOOTSTRAP / "lib" / "Doctor.ps1")
    start = doctor.index("# session_lock:")
    block = doctor[start : doctor.index("legacy_task")]
    # An SSH session never has the input desktop, so the check cannot say anything there.
    assert "if ($env:SSH_CONNECTION)" in block
    assert "-Name 'session_lock' -Status 'SKIP' -Detail 'skipped: an SSH session has no desktop" in block
    # Outside SSH a locked session is still a real warning.
    assert "-Name 'session_lock' -Status 'WARN' -Detail 'the Windows session is locked" in block
    assert block.index("if ($env:SSH_CONNECTION)") < block.index("Test-WinBridgeSessionUnlocked)")


def test_doctor_forces_utf8_output_before_any_output():
    common = _read(BOOTSTRAP / "lib" / "Common.ps1")
    helper = common[common.index("function Set-WinBridgeUtf8Output"):]
    assert "UTF8Encoding($false)" in helper.split("\nfunction ")[0]
    script = _read(BOOTSTRAP / "scripts" / "Test-WinBridgeInstall.ps1")
    assert script.index("Set-WinBridgeUtf8Output") < script.index("Invoke-WinBridgeDoctor")
    # The CLI decodes every script's output as UTF-8, so the logs script writes it that way too.
    logs = _read(BOOTSTRAP / "scripts" / "Get-WinBridgeLogs.ps1")
    assert logs.index("Set-WinBridgeUtf8Output") < logs.index("Get-WinBridgePath 'logs'")


def test_doctor_reviews_sshd_read_only():
    doctor = _read(BOOTSTRAP / "lib" / "Doctor.ps1")
    ssh = _read(BOOTSTRAP / "lib" / "OpenSsh.ps1")
    assert "New-WinBridgeSshdSettingsCheck" in doctor and "New-WinBridgeSshdFailuresCheck" in doctor
    # Both run only when SSH is not skipped, and the review never changes the service.
    assert doctor.index("New-WinBridgeSshdSettingsCheck") > doctor.index("if (-not $SkipSsh)")
    for name in ("Get-WinBridgeSshdFailureCounts", "Measure-WinBridgeSshdFailures", "New-WinBridgeSshdFailuresCheck"):
        body = ssh[ssh.index("function " + name):].split("\nfunction ")[0]
        assert not re.search(r"Restart-Service|Stop-Service|Set-Service|New-NetFirewallRule", body), name
    assert "-cmatch" in ssh  # 'Invalid user' differs from 'invalid user' only by case


def test_task_has_a_watchdog_trigger_without_duplicates():
    task = _read(BOOTSTRAP / "lib" / "ScheduledTask.ps1")
    assert "-RepetitionInterval (New-TimeSpan -Minutes 5)" in task
    assert "-Trigger @($trigger, $watchdog)" in task
    assert "-MultipleInstances IgnoreNew" in task


def test_sshd_hardening_is_a_block_in_sshd_config():
    ssh = _read(BOOTSTRAP / "lib" / "OpenSsh.ps1")
    assert "# BEGIN WINBRIDGE" in ssh and "# END WINBRIDGE" in ssh
    assert "AllowTcpForwarding no" in ssh
    assert "[IO.File]::WriteAllText($includePath" not in ssh


def test_firewall_normalizes_netmask_ranges():
    fw = _read(BOOTSTRAP / "lib" / "Firewall.ps1")
    assert "function ConvertTo-WinBridgeCidr" in fw
    assert "ConvertTo-WinBridgeCidr -Entry $entry" in fw


def test_supervisor_records_the_exit_code_and_its_own_exit():
    supervisor = _read(BOOTSTRAP / "scripts" / "supervisor.ps1")
    assert "$null = $child.Handle" in supervisor
    assert "PowerShell.Exiting" in supervisor
    assert supervisor.index("$null = $child.Handle") < supervisor.index("$child.ExitCode")


def test_elevation_forwards_the_script_parameters():
    text = _read(BOOTSTRAP / "bootstrap.ps1")
    assert "$script:BoundParameters = $PSBoundParameters" in text
    assert "Restart-WinBridgeElevated -Bound $script:BoundParameters" in text
    assert "Restart-WinBridgeElevated -Bound $PSBoundParameters" not in text


def test_venv_uses_the_real_interpreter_and_is_checked():
    text = _read(BOOTSTRAP / "lib" / "PythonRuntime.ps1")
    assert "sys._base_executable" in text
    assert "Resolve-WinBridgeLinkedPath" in text
    assert "Resolve-WinBridgeBasePython -Path $candidate" in text
    assert "cannot run its Python" in text


def test_template_is_copied_before_the_config_is_written():
    text = _read(BOOTSTRAP / "bootstrap.ps1")
    copy = text.index("winbridge.toml.template")
    write = text.index("Write-WinBridgeConfig -State $state")
    assert copy < write


def test_firewall_audit_skips_rules_of_other_programs():
    text = _read(BOOTSTRAP / "lib" / "Firewall.ps1")
    start = text.index("function Get-WinBridgeFirewallFilterIndex")
    audit = text[start : text.index("function Assert-WinBridgeNoBroadPortRules")]
    assert "function Get-WinBridgeBroadPortRules" in audit
    # The filters are read in bulk once; asking each rule for its filters took about a minute.
    assert "Get-NetFirewallPortFilter -All" in audit
    assert "| Get-NetFirewallPortFilter" not in audit
    assert "Get-NetFirewallApplicationFilter" in audit
    assert "Get-NetFirewallServiceFilter" in audit
    assert "sshd" in audit


def test_firewall_never_disables_protection():
    text = _read(BOOTSTRAP / "lib" / "Firewall.ps1")
    assert "New-NetFirewallRule" in text
    assert "RemoteAddress $remote" in text
    assert "Disable-NetFirewallRule" in text  # only the built-in broad OpenSSH rule
    assert "OpenSSH-Server-In-TCP" in text
    assert "Assert-WinBridgeNoBroadPortRules" in text
    lowered = text.lower()
    assert "set-netfirewallprofile" not in lowered
    assert "set-mppreference" not in lowered
    assert "disableantivirus" not in lowered


def test_no_secret_material_is_committed():
    forbidden_markers = (
        "-----BEGIN OPENSSH PRIVATE KEY-----",
        "-----BEGIN RSA PRIVATE KEY-----",
        "-----BEGIN PRIVATE KEY-----",
    )
    skip_dirs = {".git", "build", "dist", "site", ".venv", "venv", "__pycache__", ".pytest_cache"}
    marker_definitions = {
        REPO_ROOT / "tools" / "build_bundle.py",
        REPO_ROOT / "tests" / "test_bootstrap_contract.py",
    }
    for path in REPO_ROOT.rglob("*"):
        if not path.is_file() or any(part in skip_dirs for part in path.parts):
            continue
        if path in marker_definitions:
            continue
        if path.name == "authorized_keys.example":
            continue
        assert path.name != "authorized_keys", f"unexpected authorized_keys: {path}"
        assert path.suffix.lower() not in {".pem", ".key", ".pfx", ".p12"}, path
        if path.stat().st_size > 2 * 1024 * 1024:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for marker in forbidden_markers:
            assert marker not in text, f"private key material in {path}"


def test_management_scripts_are_json_safe_and_share_the_library():
    scripts = sorted((BOOTSTRAP / "scripts").glob("*.ps1"))
    assert len(scripts) >= 8
    for path in scripts:
        text = _read(path)
        assert "lib\\Common.ps1" in text, path
        if "[switch]$Json" in text:
            assert "Set-WinBridgeConsoleQuiet" in text, path


def test_bundle_builder_contract():
    source = _read(REPO_ROOT / "tools" / "build_bundle.py")
    compile(source, "tools/build_bundle.py", "exec")
    for required in (
        "--ssh-public-key-file",
        "--allow-no-ssh-key",
        "--verify",
        "scan_for_secrets",
        "verify_bundle",
        "manifest.json",
        "SHA256SUMS",
        "verify_git_base",
        "parse_lock",
        "verify_wheelhouse",
        "--require-hashes",
    ):
        assert required in source
    assert "PRIVATE_KEY_MARKERS" in source
    assert "date_time=(2020, 1, 1, 0, 0, 0)" in source


def test_ci_and_gitignore_cover_the_fork():
    ci = _read(REPO_ROOT / ".github" / "workflows" / "ci.yml")
    assert "main" in ci
    assert "windows-latest" in ci
    assert "check-ps1.ps1" in ci
    assert "test-bootstrap.ps1" in ci
    assert not (REPO_ROOT / ".github" / "workflows" / "publish.yml").exists()
    ignore = _read(REPO_ROOT / ".gitignore")
    for entry in ("dist/", "*.whl", ".env", "bootstrap/authorized_keys", "build/wheelhouse/"):
        assert entry in ignore


def test_jobs_installer_pins_the_runner_hash():
    installer = _read(BOOTSTRAP / "jobs" / "Install-WinBridgeJobs.ps1")
    assert "$RunnerSha256" in installer
    assert "runner hash mismatch" in installer
    assert "ReadAllBytes" in installer and "WriteAllBytes" in installer
    update = _read(BOOTSTRAP / "scripts" / "Update-WinBridgeGo.ps1")
    assert "runner_sha256" in update


def test_docs_name_the_builtin_tools():
    for name in ("architecture.md", "AGENT-CONTRACT.md"):
        text = _read(REPO_ROOT / "docs" / name)
        assert "Processes" in text and "Diagnose" in text, name
        assert "9 herramientas de perfil" in text, name


def test_no_script_or_cli_says_9_tools_without_profile():
    for folder in (BOOTSTRAP, REPO_ROOT / "cli"):
        for path in folder.rglob("*"):
            if path.suffix not in {".ps1", ".go", ".md", ".toml"} or not path.is_file():
                continue
            for line in path.read_text(encoding="utf-8").splitlines():
                assert not re.search(r"\b9 tools\b", line), f"{path}: {line.strip()}"


def test_default_port_is_8091_everywhere():
    cli = _read(REPO_ROOT / "cli" / "main.go")
    ports = {
        "bootstrap.ps1": re.search(r"\[int\]\$Port = (\d+)", _read(BOOTSTRAP / "bootstrap.ps1")),
        "Common.ps1": re.search(r"WinBridgeDefaultPort = (\d+)", _read(BOOTSTRAP / "lib" / "Common.ps1")),
        "Firewall.ps1": re.search(r"\[int\]\$McpPort = (\d+)", _read(BOOTSTRAP / "lib" / "Firewall.ps1")),
        "cli/main.go": re.search(r"Host\{Port: (\d+)", cli),
    }
    found = {name: (m.group(1) if m else None) for name, m in ports.items()}
    assert set(found.values()) == {"8091"}, found
