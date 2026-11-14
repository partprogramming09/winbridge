"""Tool tier definitions and access control."""

from __future__ import annotations

import ipaddress

# The only tool set the WinBridge bootstrap enables. Keep this in sync with
# bootstrap/config/winbridge.toml.template.
WINBRIDGE_PROFILE = frozenset(
    {
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
)

TAILSCALE_NETWORKS = (
    ipaddress.ip_network("100.64.0.0/10"),
    ipaddress.ip_network("fd7a:115c:a1e0::/48"),
)

TOOL_TIERS = {
    "tier1": {
        "Snapshot",
        "AnnotatedSnapshot",
        "GetClipboard",
        "GetSystemInfo",
        "ListProcesses",
        "FileList",
        "FileSearch",
        "RegRead",
        "ServiceList",
        "TaskList",
        "EventLog",
        "Ping",
        "PortCheck",
        "NetConnections",
        "OCR",
        "ScreenRecord",
        "Notification",
        "Wait",
        "GetTaskStatus",
        "GetRunningTasks",
    },
    "tier2": {
        "Click",
        "Type",
        "Move",
        "Scroll",
        "Shortcut",
        "FocusWindow",
        "MinimizeAll",
        "Scrape",
        "CancelTask",
        "ReconnectSession",
    },
    "tier3": {
        "Shell",
        "App",
        "PlaySound",
        "FileRead",
        "FileWrite",
        "FileDownload",
        "FileUpload",
        "KillProcess",
        "RegWrite",
        "ServiceStart",
        "ServiceStop",
        "TaskCreate",
        "TaskDelete",
        "SetClipboard",
        "LockScreen",
    },
}

ALL_TOOLS = TOOL_TIERS["tier1"] | TOOL_TIERS["tier2"] | TOOL_TIERS["tier3"]
_NAME_LOOKUP = {name.lower(): name for name in ALL_TOOLS}


def parse_tool_csv(raw: str | None) -> list[str]:
    if not raw:
        return []
    return [part.strip() for part in raw.split(",") if part.strip()]


def normalize_tool_names(tool_names: list[str]) -> list[str]:
    normalized = []
    unknown = []
    for name in tool_names:
        hit = _NAME_LOOKUP.get(name.lower())
        if hit:
            normalized.append(hit)
        else:
            unknown.append(name)
    if unknown:
        allowed = ", ".join(sorted(ALL_TOOLS))
        raise ValueError(f"Unknown tools: {', '.join(unknown)}. Allowed tools: {allowed}")
    return normalized


def resolve_enabled_tools(
    *,
    enable_tier3: bool = False,
    disable_tier2: bool = False,
    enable_all: bool = False,
    explicit_tools: list[str] | None = None,
    exclude_tools: list[str] | None = None,
) -> set[str]:
    """Resolve active tools.

    Precedence: explicit tools > tier toggles.
    """
    explicit_tools = explicit_tools or []
    exclude_tools = exclude_tools or []

    if explicit_tools:
        enabled = set(normalize_tool_names(explicit_tools))
    elif enable_all:
        enabled = set(ALL_TOOLS)
    else:
        enabled = set(TOOL_TIERS["tier1"])
        if not disable_tier2:
            enabled |= TOOL_TIERS["tier2"]
        if enable_tier3:
            enabled |= TOOL_TIERS["tier3"]

    if exclude_tools:
        enabled -= set(normalize_tool_names(exclude_tools))

    return enabled


def is_tailscale_address(host: str) -> bool:
    """Return True only for addresses inside Tailscale's IPv4/IPv6 ranges."""
    try:
        address = ipaddress.ip_address(host.strip("[]"))
    except ValueError:
        return False
    return any(address in network for network in TAILSCALE_NETWORKS)


def validate_winbridge_profile(
    *,
    transport: str,
    host: str,
    port: int,
    auth_key: str | None,
    allow_insecure_remote: bool,
    enable_all: bool,
    enable_tier3: bool,
    enabled_tools: set[str],
) -> None:
    """Fail closed when WinBridge is started outside its reviewed profile.

    The bootstrap sets WINBRIDGE_STRICT=1. This guard is intentionally
    independent of the defaults so a stale config or a manual command line
    cannot widen the profile by accident.
    """
    if transport != "streamable-http":
        raise ValueError("WinBridge strict profile requires streamable-http transport")
    if not 1024 <= int(port) <= 65535:
        raise ValueError("WinBridge strict profile requires a port between 1024 and 65535")
    if not is_tailscale_address(host):
        raise ValueError(
            "WinBridge strict profile requires a Tailscale IPv4/IPv6 bind address; "
            "0.0.0.0, loopback, LAN and public addresses are refused"
        )
    if not auth_key:
        raise ValueError("WinBridge strict profile requires an auth key")
    if allow_insecure_remote:
        raise ValueError("WinBridge strict profile refuses allow_insecure_remote")
    if enable_all or enable_tier3:
        raise ValueError("WinBridge strict profile refuses --enable-all and --enable-tier3")
    if set(enabled_tools) != set(WINBRIDGE_PROFILE):
        missing = sorted(WINBRIDGE_PROFILE - set(enabled_tools))
        extra = sorted(set(enabled_tools) - WINBRIDGE_PROFILE)
        raise ValueError(
            "WinBridge strict profile requires exactly the 9 reviewed tools "
            f"(missing: {missing or 'none'}; extra: {extra or 'none'})"
        )


def get_tier_names(enabled_tools: set[str]) -> list[str]:
    enabled_tiers = []
    if TOOL_TIERS["tier1"] & enabled_tools:
        enabled_tiers.append("1")
    if TOOL_TIERS["tier2"] & enabled_tools:
        enabled_tiers.append("2")
    if TOOL_TIERS["tier3"] & enabled_tools:
        enabled_tiers.append("3")
    return enabled_tiers


def _get_registered_tools(mcp) -> dict[str, object]:
    # fastmcp 2.x
    tool_mgr = getattr(mcp, "_tool_manager", None)
    tools = getattr(tool_mgr, "_tools", None)
    if isinstance(tools, dict):
        return tools

    # fastmcp 3.x
    provider = getattr(mcp, "_local_provider", None)
    components = getattr(provider, "_components", None)
    if isinstance(components, dict):
        out: dict[str, object] = {}
        for comp_key, comp in components.items():
            if not isinstance(comp_key, str) or not comp_key.startswith("tool:"):
                continue
            name = getattr(comp, "name", None)
            if not isinstance(name, str) or not name:
                name = comp_key.split(":", 1)[1].split("@", 1)[0]
            out[name] = comp
        return out

    raise RuntimeError("Unsupported fastmcp internals: cannot locate registered tools")


def _remove_tool(mcp, name: str) -> None:
    # fastmcp 2.x
    tool_mgr = getattr(mcp, "_tool_manager", None)
    tools = getattr(tool_mgr, "_tools", None)
    if isinstance(tools, dict):
        tools.pop(name, None)
        return

    # fastmcp 3.x
    provider = getattr(mcp, "_local_provider", None)
    components = getattr(provider, "_components", None)
    if isinstance(components, dict):
        keys_to_remove = [
            k
            for k, v in components.items()
            if isinstance(k, str)
            and k.startswith("tool:")
            and ((getattr(v, "name", None) == name) or k.split(":", 1)[1].split("@", 1)[0] == name)
        ]
        for k in keys_to_remove:
            components.pop(k, None)


def filter_tools(mcp, enabled_tools: set[str]) -> dict[str, int]:
    all_tools = list(_get_registered_tools(mcp).keys())
    total_count = len(all_tools)
    for name in all_tools:
        if name not in enabled_tools:
            _remove_tool(mcp, name)
    return {"enabled": len(enabled_tools), "disabled": total_count - len(enabled_tools), "total": total_count}
