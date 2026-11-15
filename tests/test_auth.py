"""Unit tests for auth middleware."""

from __future__ import annotations

import hmac

from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route
from starlette.testclient import TestClient

from winremote.auth import AuthKeyMiddleware


def _make_app(auth_key: str):
    async def homepage(request):
        return JSONResponse({"ok": True})

    async def health(request):
        return JSONResponse({"status": "ok"})

    app = Starlette(
        routes=[
            Route("/", homepage),
            Route("/health", health),
        ]
    )
    app.add_middleware(AuthKeyMiddleware, auth_key=auth_key)
    return app


class TestAuthMiddleware:
    def test_health_no_auth(self):
        app = _make_app("secret123")
        client = TestClient(app)
        resp = client.get("/health")
        assert resp.status_code == 200

    def test_unauthorized_without_key(self):
        app = _make_app("secret123")
        client = TestClient(app)
        resp = client.get("/")
        assert resp.status_code == 401

    def test_authorized_with_key(self):
        app = _make_app("secret123")
        client = TestClient(app)
        resp = client.get("/", headers={"Authorization": "Bearer secret123"})
        assert resp.status_code == 200

    def test_wrong_key(self):
        app = _make_app("secret123")
        client = TestClient(app)
        resp = client.get("/", headers={"Authorization": "Bearer wrong"})
        assert resp.status_code == 401


class TestConstantTimeComparison:
    def test_token_comparison_uses_constant_time(self, monkeypatch):
        calls = []
        real = hmac.compare_digest

        def spy(a, b):
            calls.append((a, b))
            return real(a, b)

        monkeypatch.setattr("winremote.auth.hmac.compare_digest", spy)
        client = TestClient(_make_app("secret123"))
        assert client.get("/", headers={"Authorization": "Bearer secret123"}).status_code == 200
        assert calls and all(isinstance(x, bytes) for pair in calls for x in pair)

    def test_empty_and_wrong_length_tokens_are_rejected(self):
        client = TestClient(_make_app("secret123"))
        for header in ("Bearer ", "Bearer", "Bearer secret1234", "Bearer secret12", "bearer secret123"):
            assert client.get("/", headers={"Authorization": header}).status_code == 401, header

    def test_same_length_different_token_is_rejected(self):
        client = TestClient(_make_app("secret123"))
        assert client.get("/", headers={"Authorization": "Bearer secret124"}).status_code == 401

    def test_non_ascii_header_is_rejected_not_a_crash(self):
        client = TestClient(_make_app("secret123"))
        resp = client.get("/", headers={"Authorization": "Bearer sécret123".encode("latin-1")})
        assert resp.status_code == 401


# ---- Adversarial tests -------------------------------------------------------------------------------------------


def _asgi_status(app, raw_headers, path="/"):
    """Calls the ASGI app with raw header bytes (bypasses httpx header validation). Returns the status code."""
    import asyncio

    sent = []

    async def receive():
        return {"type": "http.request", "body": b"", "more_body": False}

    async def send(message):
        sent.append(message)

    scope = {
        "type": "http",
        "asgi": {"version": "3.0"},
        "http_version": "1.1",
        "method": "GET",
        "scheme": "http",
        "path": path,
        "raw_path": path.encode(),
        "query_string": b"",
        "root_path": "",
        "headers": raw_headers,
        "client": ("100.64.1.2", 1234),
        "server": ("100.64.1.1", 80),
    }
    asyncio.run(app(scope, receive, send))
    return next(m["status"] for m in sent if m["type"] == "http.response.start")


def _make_app_oauth(auth_key: str, validator):
    async def homepage(request):
        return JSONResponse({"ok": True})

    app = Starlette(routes=[Route("/", homepage)])
    app.add_middleware(AuthKeyMiddleware, auth_key=auth_key, oauth_validator=validator)
    return app


class TestAdversarialHeaderShapes:
    KEY = "secret123"

    def test_bearer_scheme_is_case_sensitive(self):
        client = TestClient(_make_app(self.KEY))
        for scheme in ("bearer", "BEARER", "BeArEr"):
            assert client.get("/", headers={"Authorization": f"{scheme} {self.KEY}"}).status_code == 401, scheme

    def test_key_is_case_sensitive(self):
        client = TestClient(_make_app(self.KEY))
        assert client.get("/", headers={"Authorization": "Bearer SECRET123"}).status_code == 401

    def test_extra_whitespace_around_token_is_rejected(self):
        app = _make_app(self.KEY)
        for value in (
            b"Bearer  secret123",
            b"Bearer secret123 ",
            b" Bearer secret123",
            b"Bearer\tsecret123",
            b"Bearer secret123\t",
            b"Bearer secret123\x00",
            b"Bearer \x00secret123",
            b"Bearer secret123\r",
            b"Bearer secret123\n",
        ):
            assert _asgi_status(app, [(b"authorization", value)]) == 401, value

    def test_raw_non_ascii_and_control_bytes_never_crash_or_pass(self):
        app = _make_app(self.KEY)
        for value in (
            b"Bearer \xff\xfe",
            b"Bearer \x80",
            b"\xc3\xa9",
            b"Bearer s\xc3\xa9cret123",
            b"\x00",
            b"Bearer " + bytes(range(256)),
        ):
            assert _asgi_status(app, [(b"authorization", value)]) == 401, value

    def test_missing_header_and_empty_header(self):
        app = _make_app(self.KEY)
        assert _asgi_status(app, []) == 401
        assert _asgi_status(app, [(b"authorization", b"")]) == 401
        assert _asgi_status(app, [(b"authorization", b"   ")]) == 401

    def test_wrong_header_name_does_not_authenticate(self):
        app = _make_app(self.KEY)
        for name in (b"x-authorization", b"proxy-authorization", b"authorisation", b"x-api-key"):
            assert _asgi_status(app, [(name, b"Bearer secret123")]) == 401, name

    def test_key_is_not_accepted_as_prefix_or_substring(self):
        client = TestClient(_make_app(self.KEY))
        for header in (
            "Bearer secret123secret123",
            "Bearer secret",
            "Bearer Bearer secret123",
            "secret123",
            "Basic secret123",
            "Token secret123",
        ):
            assert client.get("/", headers={"Authorization": header}).status_code == 401, header

    def test_huge_token_is_rejected(self):
        client = TestClient(_make_app(self.KEY))
        assert client.get("/", headers={"Authorization": "Bearer " + "a" * 1_000_000}).status_code == 401

    def test_public_path_lookalikes_need_auth(self):
        app = _make_app(self.KEY)
        for path in ("/health/", "/HEALTH", "/health%2F", "/healthz", "//health", "/health "):
            assert _asgi_status(app, [], path=path) in (401, 404), path
            assert _asgi_status(app, [], path=path) != 200, path


class TestAdversarialKeys:
    def test_empty_key_accepts_nothing(self):
        app = _make_app("")
        for value in (b"", b"Bearer ", b"Bearer", b"Bearer  ", b"bearer ", b"Bearer x"):
            assert _asgi_status(app, [(b"authorization", value)]) == 401, value
        assert _asgi_status(app, []) == 401

    def test_empty_key_with_oauth_validator_only_lets_valid_oauth_tokens(self):
        app = _make_app_oauth("", lambda token: token == "oauth-ok")
        assert _asgi_status(app, [(b"authorization", b"Bearer ")]) == 401
        assert _asgi_status(app, [(b"authorization", b"")]) == 401
        assert _asgi_status(app, [(b"authorization", b"Bearer oauth-ok")]) == 200

    def test_whitespace_only_key_matches_only_itself(self):
        app = _make_app("   ")
        assert _asgi_status(app, [(b"authorization", b"Bearer    ")]) == 200
        assert _asgi_status(app, [(b"authorization", b"Bearer  ")]) == 401
        assert _asgi_status(app, [(b"authorization", b"Bearer ")]) == 401
        assert _asgi_status(app, []) == 401

    def test_key_with_special_characters_matches_exactly(self):
        for key in ("a b", "a+b/c=d==", "ke\\y'\"%s{}", "a" * 10_000, "tab\tkey", "-._~+/", "Bearer x"):
            app = _make_app(key)
            ok = _asgi_status(app, [(b"authorization", b"Bearer " + key.encode("latin-1"))])
            assert ok == 200, key
            assert _asgi_status(app, [(b"authorization", b"Bearer " + key.encode("latin-1") + b"x")]) == 401, key
            assert _asgi_status(app, [(b"authorization", key.encode("latin-1"))]) == 401, key

    def test_non_ascii_key_authenticates_with_latin1_bytes(self):
        # Same key sent as latin-1 bytes (what httpx does for str headers with chars < 256).
        app = _make_app("sécret")
        assert _asgi_status(app, [(b"authorization", "Bearer sécret".encode("latin-1"))]) == 200

    def test_non_ascii_key_rejects_lookalike_bytes(self):
        app = _make_app("sécret")
        for value in (b"Bearer secret", "Bearer sécret".encode("utf-8") + b"x", b"Bearer s\xc3\xa9cre"):
            assert _asgi_status(app, [(b"authorization", value)]) != 200 or value == "Bearer sécret".encode("utf-8"), (
                value
            )


class TestAdversarialOAuthFallback:
    def test_validator_is_not_called_for_the_exact_api_key(self):
        calls = []
        app = _make_app_oauth("secret123", lambda t: calls.append(t) or False)
        assert _asgi_status(app, [(b"authorization", b"Bearer secret123")]) == 200
        assert calls == []

    def test_validator_returning_falsy_values_rejects(self):
        for falsy in (False, None, 0, "", [], {}):
            app = _make_app_oauth("secret123", lambda t, f=falsy: f)
            assert _asgi_status(app, [(b"authorization", b"Bearer other")]) == 401, repr(falsy)

    def test_validator_only_called_with_bearer_prefix_and_gets_the_exact_token(self):
        seen = []
        app = _make_app_oauth("secret123", lambda t: seen.append(t) or False)
        for value in (b"bearer tok", b"Basic tok", b"", b"Bearer", b"Bearer tok ", b"Bearer  tok"):
            _asgi_status(app, [(b"authorization", value)])
        assert seen == ["tok ", " tok"]

    def test_validator_raising_never_authenticates(self):
        def boom(token):
            raise RuntimeError("validator down")

        app = _make_app_oauth("secret123", boom)
        try:
            status = _asgi_status(app, [(b"authorization", b"Bearer anything")])
        except RuntimeError:
            return  # propagating the error is not a grant of access
        assert status != 200

    def test_api_key_still_works_when_validator_raises(self):
        def boom(token):
            raise RuntimeError("validator down")

        app = _make_app_oauth("secret123", boom)
        assert _asgi_status(app, [(b"authorization", b"Bearer secret123")]) == 200

    def test_validator_raising_on_non_bearer_header_is_not_reached(self):
        def boom(token):
            raise RuntimeError("should not be called")

        app = _make_app_oauth("secret123", boom)
        assert _asgi_status(app, [(b"authorization", b"Basic xyz")]) == 401
        assert _asgi_status(app, []) == 401
