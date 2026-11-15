"""Checks on the documents a stranger reads first: security policy, README, community files, upstream leftovers."""

from __future__ import annotations

import re
import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
LINK = re.compile(r"\]\(([^)\s]+(?:\s+\"[^\"]*\")?)\)")


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def test_security_md_points_to_this_repo_and_names_private_reporting():
    text = _read(REPO_ROOT / "SECURITY.md")
    assert "Report a vulnerability" in text
    for forbidden in ("dddabtc", "PyPI", "--auth-key", "Tier 3"):
        assert forbidden not in text, forbidden
    for known_limit in ("0.0.0.0:22", "Processes", "Diagnose", "elevated"):
        assert known_limit in text, known_limit


def test_readme_has_both_languages_and_no_upstream_install():
    text = _read(REPO_ROOT / "README.md")
    assert "## English" in text and "## Español" in text
    for forbidden in ("pip install winremote-mcp", "private fork", "README.zh-CN"):
        assert forbidden not in text, forbidden
    for line in text.splitlines():
        assert not (line.lstrip().startswith("[![") and "dddabtc" in line), line
    assert "SECURITY.md" in text and "docs/architecture.md" in text
    usage = _read(REPO_ROOT / "cli" / "main.go")
    for command in ("winbridge adopt", "winbridge status", "winbridge doctor", "winbridge update"):
        assert command in text, command
        assert command in usage, f"{command} is taught but the CLI does not list it"


def test_community_files_exist_and_issue_form_blocks_security_reports():
    for name in (
        "CODE_OF_CONDUCT.md",
        "CONTRIBUTING.md",
        ".github/PULL_REQUEST_TEMPLATE.md",
        ".github/ISSUE_TEMPLATE/bug_report.yml",
        ".github/ISSUE_TEMPLATE/config.yml",
    ):
        assert (REPO_ROOT / name).is_file(), name
    config = _read(REPO_ROOT / ".github" / "ISSUE_TEMPLATE" / "config.yml")
    assert "blank_issues_enabled: false" in config and "Report a vulnerability" in config
    form = _read(REPO_ROOT / ".github" / "ISSUE_TEMPLATE" / "bug_report.yml")
    for redact in ("Windows user name", "computer name", "Tailscale address", "e-mail"):
        assert redact in form, redact
    contributing = _read(REPO_ROOT / "CONTRIBUTING.md")
    assert "dddabtc" not in contributing
    for command in ("uv run --with pytest pytest -q", "go vet ./...", "tools/test-bootstrap.ps1"):
        assert command in contributing, command
    assert "[INSERT CONTACT METHOD]" not in _read(REPO_ROOT / "CODE_OF_CONDUCT.md")


def test_no_upstream_leftovers_and_changelog_has_a_winbridge_entry():
    for name in ("Dockerfile", "glama.json", "mkdocs.yml", "README.zh-CN.md"):
        assert not (REPO_ROOT / name).exists(), name
    changelog = _read(REPO_ROOT / "CHANGELOG.md")
    first_entry = next(line for line in changelog.splitlines() if line.startswith("## "))
    assert first_entry.startswith("## [0.1.0]"), first_entry
    assert "## Upstream history (winremote-mcp, up to 0.4.24)" in changelog


def _tracked_markdown() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z", "*.md"], cwd=REPO_ROOT, check=True, capture_output=True, text=True
    ).stdout
    return [REPO_ROOT / n for n in out.split("\0") if n and not n.startswith("docs/plans/")]


def test_markdown_relative_links_point_to_existing_files():
    broken = []
    for path in _tracked_markdown():
        if not path.is_file():
            continue
        for target in LINK.findall(_read(path)):
            target = target.split("#")[0].split(" ")[0]
            if not target or "://" in target or target.startswith(("mailto:", "#")):
                continue
            if not (path.parent / target).resolve().exists():
                broken.append(f"{path.relative_to(REPO_ROOT).as_posix()} -> {target}")
    assert not broken, "\n".join(broken)


def test_operating_guide_matches_the_code():
    guide = _read(REPO_ROOT / "docs" / "guides" / "operar-winbridge.md")
    assert "8091" in guide
    for line in guide.splitlines():
        if "8090" in line:
            assert "Python" in line or "inherited" in line, line
    for command in ("winbridge update", "winbridge doctor", "winbridge adopt"):
        assert command in guide, command
    assert "Reconciliation pending" not in guide
    assert "claude/mac" not in guide and "(private)" not in guide and "private fork" not in guide


WORKING_MATERIAL = (
    "CLAUDE.md",
    "docs/estado.md",
    "docs/plans",
    "docs/research",
    "docs/diagrams/plan-publicar.html",
    "docs/diagrams/plan-publicar.png",
)


def test_gitattributes_excludes_working_material():
    for path in WORKING_MATERIAL:
        out = subprocess.run(
            ["git", "check-attr", "export-ignore", "--", path],
            cwd=REPO_ROOT,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
        assert out.strip().endswith("export-ignore: set"), f"{path} is not export-ignore: {out.strip()}"


def test_public_docs_do_not_link_to_working_material():
    index = _read(REPO_ROOT / "docs" / "README.md")
    for name in ("estado.md", "CLAUDE.md", "plans/", "plan-publicar"):
        assert name not in index, name


def test_inherited_skill_pages_carry_the_upstream_notice():
    pages = sorted((REPO_ROOT / "skill").rglob("*.md"))
    assert pages
    for page in pages:
        head = "\n".join(_read(page).splitlines()[:8])
        assert "Inherited from upstream" in head, page.relative_to(REPO_ROOT).as_posix()
