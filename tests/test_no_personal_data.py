"""Fails when a tracked file holds something that looks like real personal data.

Only generic patterns live here, never a real value: a test that named the real user, host or e-mail would publish it.
A sweep for the real identifiers is run outside the repository before publishing. Findings print path, line and kind only.
"""

from __future__ import annotations

import ipaddress
import re
import subprocess
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]

EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})\b")
IPV4 = re.compile(r"(?<![\d.])(\d{1,3}(?:\.\d{1,3}){3})(?!\.?\d)(/\d+)?")
UNIX_HOME = re.compile(r"/Users/([A-Za-z0-9._-]+)")
WIN_HOME = re.compile(r"[A-Za-z]:\\Users\\([A-Za-z0-9._-]+)", re.IGNORECASE)

ALLOWED_EMAIL_DOMAINS = {"example.com", "example.org", "users.noreply.github.com", "noreply.github.com", "noreply.anthropic.com", "openssh.com"}
# RFC 2606 names that can never be a real mailbox.
RESERVED_TLDS = (".test", ".invalid", ".example", ".localhost")
ALLOWED_HOME_NAMES = {"alice", "bob", "user", "username", "name", "you", "me", "public", "default", "shared", "all users"}
TAILSCALE = ipaddress.ip_network("100.64.0.0/10")
# Example addresses used in docs and tests; a real tailnet address in these two /24s is a one-in-ten-thousand coincidence.
EXAMPLE_PREFIXES = ("100.64.0.", "100.64.1.", "100.127.255.")
SKIP_SUFFIXES = {".png", ".jpg", ".jpeg", ".gif", ".ico", ".zip", ".whl", ".pyc", ".lock"}
# path -> reason. Keep this list short; every entry is a promise that the match is not real data.
EXCEPTIONS: dict[str, str] = {}


def scan_text(text: str) -> list[tuple[int, str]]:
    """Return (line number, kind) for every suspicious match in text."""
    found: list[tuple[int, str]] = []
    for number, line in enumerate(text.splitlines(), start=1):
        for match in EMAIL.finditer(line):
            if match.group(1).lower() not in ALLOWED_EMAIL_DOMAINS and not match.group(1).lower().endswith((".noreply.github.com", *RESERVED_TLDS)):
                found.append((number, "email"))
        for match in IPV4.finditer(line):
            try:
                address = ipaddress.ip_address(match.group(1))
            except ValueError:
                continue
            if address in TAILSCALE and not (match.group(2) == "/10" and str(address) == "100.64.0.0") and not match.group(1).startswith(EXAMPLE_PREFIXES):
                found.append((number, "tailscale-address"))
        for pattern, kind in ((UNIX_HOME, "unix-home"), (WIN_HOME, "windows-home")):
            for match in pattern.finditer(line):
                name = match.group(1).lower()
                if name not in ALLOWED_HOME_NAMES and set(name) != {"."}:
                    found.append((number, kind))
    return found


def _tracked_files() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], cwd=REPO_ROOT, check=True, capture_output=True, text=True
    ).stdout
    return [REPO_ROOT / name for name in out.split("\0") if name]


def test_tracked_files_have_no_personal_data():
    problems = []
    for path in _tracked_files():
        relative = path.relative_to(REPO_ROOT).as_posix()
        if path.suffix.lower() in SKIP_SUFFIXES or relative in EXCEPTIONS or not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        problems += [f"{relative}:{number}: {kind}" for number, kind in scan_text(text)]
    assert not problems, "personal-looking data in tracked files:\n" + "\n".join(problems)


def test_scanner_flags_a_real_looking_address():
    built = ".".join(["100", "101", "2", "3"])
    assert scan_text(f"host = {built}") == [(1, "tailscale-address")]
    assert scan_text("host = 100.64.1.7") == []
    assert scan_text("host = 100.64.0.2") == []
    assert scan_text("range 100.64.0.0/10") == []
    assert scan_text(built + "/10") == [(1, "tailscale-address")]
    assert scan_text("allow " + built + "/32") == [(1, "tailscale-address")]
    assert scan_text("connect to " + built + ".") == [(1, "tailscale-address")]
    assert scan_text("host = 192.168.1.10") == []


def test_scanner_flags_email_and_home_paths():
    at = "@"
    assert scan_text(f"owner{at}mailbox.net") == [(1, "email")]
    assert scan_text(f"owner{at}example.com") == []
    assert scan_text("path = /Users/" + "jdoe" + "/project") == [(1, "unix-home")]
    assert scan_text("path = C:\\Users\\" + "jdoe" + "\\AppData") == [(1, "windows-home")]
    assert scan_text("path = C:\\Users\\alice\\AppData") == []
