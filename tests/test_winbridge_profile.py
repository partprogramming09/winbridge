"""Tests for the reviewed WinBridge profile and version contract."""

from __future__ import annotations

import pytest

from winremote import PROFILE_NAME, UPSTREAM_BASE, __version__
from winremote.tiers import WINBRIDGE_PROFILE, is_tailscale_address, validate_winbridge_profile

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


def _valid_kwargs(**overrides):
    kwargs = {
        "transport": "streamable-http",
        "host": "100.64.1.2",
        "port": 8090,
        "auth_key": "not-a-real-key",
        "allow_insecure_remote": False,
        "enable_all": False,
        "enable_tier3": False,
        "enabled_tools": set(EXPECTED_TOOLS),
    }
    kwargs.update(overrides)
    return kwargs


def test_version_contract():
    assert __version__ == "0.1.0"
    assert UPSTREAM_BASE == "0.4.24"
    assert PROFILE_NAME == "winbridge"


def test_profile_is_exactly_nine_tools():
    assert set(WINBRIDGE_PROFILE) == EXPECTED_TOOLS
    assert len(WINBRIDGE_PROFILE) == 9


@pytest.mark.parametrize(
    "address",
    ["100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1", "[fd7a:115c:a1e0::1]"],
)
def test_tailscale_addresses_accepted(address):
    assert is_tailscale_address(address)


@pytest.mark.parametrize(
    "address",
    ["0.0.0.0", "::", "127.0.0.1", "192.168.1.10", "8.8.8.8", "tailscale"],
)
def test_non_tailscale_addresses_rejected(address):
    assert not is_tailscale_address(address)


def test_valid_profile_passes():
    validate_winbridge_profile(**_valid_kwargs())


@pytest.mark.parametrize(
    "overrides",
    [
        {"host": "0.0.0.0"},
        {"host": "127.0.0.1"},
        {"host": "192.168.1.10"},
        {"auth_key": None},
        {"auth_key": ""},
        {"allow_insecure_remote": True},
        {"enable_all": True},
        {"enable_tier3": True},
        {"transport": "stdio"},
        {"port": 80},
        {"enabled_tools": set(EXPECTED_TOOLS) - {"Snapshot"}},
        {"enabled_tools": set(EXPECTED_TOOLS) | {"Shell"}},
    ],
)
def test_profile_refuses_unsafe_variants(overrides):
    with pytest.raises(ValueError):
        validate_winbridge_profile(**_valid_kwargs(**overrides))


def test_python_fallback_ignores_the_files_table(tmp_path):
    """The Python fallback has no file tools, but a local.toml with [files] must not break it."""
    from winremote.config import load_config

    path = tmp_path / "winbridge.toml"
    path.write_text(
        '[server]\nhost = "100.64.1.2"\nport = 8090\n[tools]\nenable = ["Snapshot"]\n'
        "[files]\nread_roots = ['C:\\Users\\alice\\Shared']\nmax_bytes = 1024\n",
        encoding="utf-8",
    )
    cfg = load_config(path)
    assert cfg.server.port == 8090
    assert cfg.tools.enable == ["Snapshot"]
