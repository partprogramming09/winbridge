"""Snapshot must send its image once: FastMCP repeats a list result in structuredContent unless told not to."""

import asyncio
import base64
from types import SimpleNamespace

from fastmcp import Client

from winremote import __main__ as app


def _fake_desktop(monkeypatch, image: str) -> None:
    window = SimpleNamespace(handle=1, title="Test", width=10, height=10, rect=(0, 0, 10, 10))
    monkeypatch.setattr(app.desktop, "take_screenshot", lambda **_: image)
    monkeypatch.setattr(app.desktop, "enumerate_windows", lambda: [window])
    monkeypatch.setattr(app.desktop, "get_interactive_elements", lambda: [])
    monkeypatch.setattr(app.desktop, "_get_system_language", lambda: "en_US")


def test_snapshot_has_no_output_schema():
    tool = asyncio.run(app.mcp.get_tool("Snapshot"))
    assert tool.output_schema is None


def test_snapshot_result_carries_the_image_only_once(monkeypatch):
    image = base64.b64encode(b"x" * 30000).decode()
    _fake_desktop(monkeypatch, image)

    async def call():
        async with Client(app.mcp) as client:
            return await client.call_tool("Snapshot", {})

    result = asyncio.run(call())
    assert [block.type for block in result.content] == ["image", "text"]
    assert result.structured_content is None
