package httpx

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func status(h http.Handler, path, remote, auth string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestAuthAcceptsOnlyTheExactBearerKey(t *testing.T) {
	h := Auth("secret-key", map[string]bool{"/health": true}, ok)
	for name, c := range map[string]struct {
		path, auth string
		want       int
	}{
		"right key":        {"/x", "Bearer secret-key", 200},
		"wrong key":        {"/x", "Bearer secret-kez", 401},
		"prefix of key":    {"/x", "Bearer secret", 401},
		"longer than key":  {"/x", "Bearer secret-key-and-more", 401},
		"no header":        {"/x", "", 401},
		"wrong scheme":     {"/x", "Basic secret-key", 401},
		"lowercase scheme": {"/x", "bearer secret-key", 401},
		"public path":      {"/health", "", 200},
		"empty token":      {"/x", "Bearer ", 401},
	} {
		if got := status(h, c.path, "100.64.0.1:5555", c.auth); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

func TestAllowlistUsesThePeerAddressOnly(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("100.64.1.2/32")
	h := Allowlist([]*net.IPNet{allowed}, map[string]bool{"/health": true}, ok)
	if got := status(h, "/x", "100.64.1.2:1234", ""); got != 200 {
		t.Errorf("listed client: %d", got)
	}
	if got := status(h, "/x", "100.64.1.3:1234", ""); got != 403 {
		t.Errorf("unlisted client: %d", got)
	}
	if got := status(h, "/health", "8.8.8.8:1234", ""); got != 200 {
		t.Errorf("/health is exempt: %d", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	req.Header.Set("X-Forwarded-For", "100.64.1.2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("a forwarded-for header must not grant access: %d", rec.Code)
	}
	if got := status(h, "/x", "[::1]:1", ""); got != 403 {
		t.Errorf("IPv6 loopback is not listed: %d", got)
	}
}

func TestAuthWithAnEmptyKeyRejectsEverything(t *testing.T) {
	h := Auth("", map[string]bool{"/health": true}, ok)
	for _, auth := range []string{"", "Bearer ", "Bearer anything"} {
		if got := status(h, "/x", "100.64.0.1:5555", auth); got != 401 {
			t.Errorf("empty key with %q: got %d, want 401", auth, got)
		}
	}
	if got := status(h, "/health", "100.64.0.1:5555", ""); got != 200 {
		t.Errorf("public path: got %d", got)
	}
}
