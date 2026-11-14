"""A bundle built with --go-agent carries the executable and its SHA-256 and no Python wheels or locks."""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from pathlib import Path

import pytest

pytestmark = pytest.mark.skipif(sys.version_info < (3, 11), reason="build_bundle.py needs Python 3.11+")

REPO_ROOT = Path(__file__).resolve().parents[1]
BUILD = REPO_ROOT / "tools" / "build_bundle.py"


def test_go_bundle_has_agent_and_no_python_payload(tmp_path: Path) -> None:
    base = json.loads((REPO_ROOT / "upstream.json").read_text(encoding="utf-8"))["base_commit"]
    if subprocess.run(["git", "merge-base", "--is-ancestor", base, "HEAD"], cwd=REPO_ROOT).returncode != 0:
        pytest.skip("shallow checkout: the upstream base commit is not in history")
    exe = tmp_path / "winbridge-agent.exe"
    exe.write_bytes(b"MZ not a real executable")
    out, build = tmp_path / "out", tmp_path / "build"
    cmd = [
        sys.executable,
        str(BUILD),
        "--go-agent",
        str(exe),
        "--allow-no-ssh-key",
        "--out-dir",
        str(out),
        "--build-dir",
        str(build),
        "--no-zip",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, check=False)
    assert result.returncode == 0, result.stderr
    bundle = next(build.iterdir())
    manifest = json.loads((bundle / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["agent_exe"] == {
        "path": "agent/winbridge-agent.exe",
        "sha256": hashlib.sha256(exe.read_bytes()).hexdigest(),
    }
    assert "app_wheel" not in manifest and "bundle_lock" not in manifest
    assert not (bundle / "wheels").exists()
    assert not (bundle / "bootstrap" / "requirements").exists()
    assert (bundle / "agent" / "winbridge-agent.exe").read_bytes() == exe.read_bytes()


def test_go_bundle_rejects_a_missing_executable(tmp_path: Path) -> None:
    cmd = [
        sys.executable,
        str(BUILD),
        "--go-agent",
        str(tmp_path / "nope.exe"),
        "--allow-no-ssh-key",
        "--out-dir",
        str(tmp_path),
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, check=False)
    assert result.returncode != 0
    assert "not found" in result.stderr
