"""Checks on the GitHub Actions workflows and Dependabot: Go is pinned, formatting is enforced, releases are least-privilege.

Text checks on purpose: the CI installs only the test extras, so no YAML parser is assumed.
"""

from __future__ import annotations

import re
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = REPO_ROOT / ".github" / "workflows"


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _job(text: str, name: str) -> str:
    """Return the text of one top-level job of a workflow."""
    match = re.search(rf"^  {re.escape(name)}:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)", text, re.S | re.M)
    assert match, f"job {name} not found"
    return match.group(1)


def test_ci_pins_go_checks_format_and_builds_the_cli_for_darwin():
    ci = _read(WORKFLOWS / "ci.yml")
    assert re.search(r"push:\n\s+branches: \[main\]", ci)
    cli = _job(ci, "cli")
    assert "go-version-file: cli/go.mod" in cli
    assert 'test -z "$(gofmt -l .)"' in cli
    assert "GOOS: darwin" in cli and "arm64" in cli and "amd64" in cli
    agent = _job(ci, "agent-go")
    assert "go-version-file: agent/go.mod" in agent
    assert 'test -z "$(gofmt -l .)"' in agent


def test_dependabot_covers_go_modules():
    text = _read(REPO_ROOT / ".github" / "dependabot.yml")
    for directory in ("/agent", "/cli"):
        assert re.search(rf'package-ecosystem: "gomod"\n\s+directory: "{directory}"\n\s+schedule:\n\s+interval: weekly', text), directory


def test_release_workflow_is_least_privilege_and_pinned():
    release = _read(WORKFLOWS / "release.yml")
    assert re.search(r"on:\n\s+push:\n\s+tags: \[\"v\*\"\]\n", release)
    assert "workflow_dispatch" not in release and "pull_request" not in release
    assert re.search(r"^permissions: \{\}", release, re.M), "no permissions at workflow level"
    build = _job(release, "build")
    assert re.search(r"permissions:\n\s+contents: read\b", build)
    assert "write" not in build and "id-token" not in build
    publish = _job(release, "publish")
    for permission in ("contents: write", "id-token: write", "attestations: write"):
        assert permission in publish, permission
    for use in re.findall(r"uses: (\S+)", release):
        assert re.fullmatch(r"[\w./-]+@[0-9a-f]{40}", use), f"action not pinned by hash: {use}"
    assert "persist-credentials: false" in release
    assert "CGO_ENABLED=0" in build and "-trimpath" in build
    assert "-X main.version=${version}" in build and "${GITHUB_REF_NAME#v}" in build
    assert "SHA256SUMS" in publish and "gh release create" in publish and '-R "$GITHUB_REPOSITORY"' in publish
    for artifact in ("winbridge-agent-windows-amd64.exe", "winbridge-darwin-arm64", "winbridge-darwin-amd64"):
        assert artifact in build, artifact
