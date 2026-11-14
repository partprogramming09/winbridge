package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeSecrets keeps keys in memory.
type fakeSecrets struct {
	keys    map[string]string
	failSet bool
}

// Get returns the stored key.
func (f *fakeSecrets) Get(service string) (string, error) {
	k, ok := f.keys[service]
	if !ok {
		return "", errors.New("missing")
	}
	return k, nil
}

// Set stores a key unless failSet is true.
func (f *fakeSecrets) Set(service, key string) error {
	if f.failSet {
		return errors.New("keychain locked")
	}
	f.keys[service] = key
	return nil
}

// fakeRunner records the calls and answers with a canned output.
type fakeRunner struct {
	output string
	err    error
	name   string
	args   []string
	stdin  string
}

// Run records the call.
func (f *fakeRunner) Run(name string, args []string, stdin string) (string, error) {
	f.name, f.args, f.stdin = name, args, stdin
	return f.output, f.err
}

var testHost = Host{Address: "100.64.0.9", Port: 8091, User: "alice", SSHKey: "~/.ssh/k", KeychainService: "svc", ClientIP: "100.64.0.2"}

func TestConfigRoundTripAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "hosts.json")
	cfg, err := loadConfig(path)
	if err != nil || len(cfg.Hosts) != 0 {
		t.Fatalf("a missing file must be an empty config, got %v %v", cfg, err)
	}
	if _, _, err := cfg.resolve(""); err == nil {
		t.Fatal("an empty config must not resolve a host")
	}
	cfg.Hosts["laptop"] = testHost
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	name, host, err := loaded.resolve("")
	if err != nil || name != "laptop" || host != testHost {
		t.Fatalf("the only host must resolve, got %q %v %v", name, host, err)
	}
	loaded.Hosts["other"] = testHost
	if _, _, err := loaded.resolve(""); err == nil {
		t.Fatal("several hosts must require a name")
	}
	if _, _, err := loaded.resolve("nope"); err == nil {
		t.Fatal("an unknown name must fail")
	}
}

func TestHostValidation(t *testing.T) {
	bad := []Host{{}, {Address: "a", Port: 80, User: "u", KeychainService: "s"}, {Address: "a", Port: 8091, KeychainService: "s"}}
	for _, h := range bad {
		if h.validate() == nil {
			t.Fatalf("%+v must be invalid", h)
		}
	}
	if err := testHost.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewKeyIsUrlSafeAndUnique(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)
	a, _ := newKey()
	b, _ := newKey()
	if !re.MatchString(a) || a == b {
		t.Fatalf("keys must be 64 URL-safe characters and unique: %q %q", a, b)
	}
}

func TestSSHArgsCarryNoSecretAndTargetTheScript(t *testing.T) {
	args := sshArgs(testHost, psScript("Set-WinBridgeKey.ps1", "-Json"))
	joined := strings.Join(args, " ")
	for _, want := range []string{"BatchMode=yes", "alice@100.64.0.9", `%LOCALAPPDATA%\winbridge\bin\Set-WinBridgeKey.ps1`, "-Json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestAdoptSendsTheKeyOnStdinOnly(t *testing.T) {
	secrets := &fakeSecrets{keys: map[string]string{}}
	runner := &fakeRunner{output: "noise\n{\"ok\":true,\"fingerprint\":\"abc\",\"tools_count\":9,\"profile\":\"winbridge\",\"port\":8091}\n"}
	result, err := adopt(runner, secrets, testHost)
	if err != nil || !result.OK || result.Fingerprint != "abc" {
		t.Fatalf("adopt must succeed, got %+v %v", result, err)
	}
	key := secrets.keys["svc"]
	if key == "" || strings.TrimSpace(runner.stdin) != key {
		t.Fatal("the key must be stored in the Keychain and sent on stdin")
	}
	if strings.Contains(strings.Join(runner.args, " "), key) {
		t.Fatal("the key must never appear in the ssh arguments")
	}
}

func TestAdoptRestoresThePreviousKeyOnFailure(t *testing.T) {
	secrets := &fakeSecrets{keys: map[string]string{"svc": "old-key"}}
	runner := &fakeRunner{output: "{\"ok\":false}", err: nil}
	if _, err := adopt(runner, secrets, testHost); err == nil {
		t.Fatal("an unconfirmed pairing must fail")
	}
	if secrets.keys["svc"] != "old-key" {
		t.Fatal("the previous key must be restored")
	}
	if _, err := adopt(&fakeRunner{err: errors.New("ssh down")}, secrets, testHost); err == nil || secrets.keys["svc"] != "old-key" {
		t.Fatal("an SSH failure must fail and keep the previous key")
	}
}

func TestAdoptNeedsAClientAddress(t *testing.T) {
	h := testHost
	h.ClientIP = ""
	if _, err := adopt(&fakeRunner{}, &fakeSecrets{keys: map[string]string{}}, h); err == nil {
		t.Fatal("adopt without client_ip must fail")
	}
}

func TestLastJSONLine(t *testing.T) {
	if lastJSONLine("a\n{\"x\":1}\nb\n") != "{\"x\":1}" || lastJSONLine("nothing") != "" {
		t.Fatal("lastJSONLine picked the wrong line")
	}
}

// statusServer fakes /health and /status with a required bearer key.
func statusServer(key string, profile string, strict bool, tools int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(StatusReport{Version: "0.1.0", Upstream: "0.4.24", Profile: profile})
		case "/status":
			if r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(StatusReport{Version: "0.1.0", Upstream: "0.4.24", Profile: profile, Strict: strict, ToolsCount: tools})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// hostOf converts a test server into a Host.
func hostOf(t *testing.T, srv *httptest.Server) Host {
	t.Helper()
	h := testHost
	addr := strings.TrimPrefix(srv.URL, "http://")
	i := strings.LastIndex(addr, ":")
	h.Address = addr[:i]
	if _, err := fmt.Sscan(addr[i+1:], &h.Port); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestStatusAcceptsTheStrictProfile(t *testing.T) {
	srv := statusServer("k1", "winbridge", true, 9)
	defer srv.Close()
	secrets := &fakeSecrets{keys: map[string]string{"svc": "k1"}}
	report, err := status(newHTTPClient(), secrets, hostOf(t, srv))
	if err != nil || report.ToolsCount != 9 {
		t.Fatalf("status must pass, got %+v %v", report, err)
	}
}

func TestStatusRejectsAWrongProfileOrKey(t *testing.T) {
	srv := statusServer("k1", "upstream", false, 45)
	defer srv.Close()
	h := hostOf(t, srv)
	if _, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{"svc": "k1"}}, h); err == nil {
		t.Fatal("a non-strict profile must fail")
	}
	good := statusServer("k1", "winbridge", true, 9)
	defer good.Close()
	if _, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{"svc": "wrong"}}, hostOf(t, good)); err == nil {
		t.Fatal("a wrong key must fail")
	}
	if _, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{}}, hostOf(t, good)); err == nil {
		t.Fatal("a missing Keychain key must fail")
	}
}

func TestStatusExplainsA403OnHealthAsAnAllowlistProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{"svc": "k1"}}, hostOf(t, srv))
	if err == nil || !strings.Contains(err.Error(), "allowlist") || strings.Contains(err.Error(), "does not answer") ||
		!strings.Contains(err.Error(), "winbridge adopt") {
		t.Fatalf("a 403 on /health must be reported as an allowlist problem with the fix, got %v", err)
	}
}

func TestStatusKeepsDoesNotAnswerForOtherHealthFailures(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h500 := hostOf(t, failing)
	failing.Close() // the same host now refuses the connection: status code 0
	down := h500
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer live.Close()
	for name, h := range map[string]Host{"500": hostOf(t, live), "connection refused": down} {
		_, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{"svc": "k1"}}, h)
		if err == nil || !strings.Contains(err.Error(), "does not answer") || strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("%s on /health must stay a 'does not answer' error, got %v", name, err)
		}
	}
}

func TestHostValidationRejectsInjectionAndSSHArgsEndOptions(t *testing.T) {
	bad := []Host{
		{Address: "100.64.0.9", Port: 8091, User: "-oProxyCommand=x", KeychainService: "s"},
		{Address: "100.64.0.9", Port: 8091, User: "alice", ClientIP: "100.64.0.2 & calc", KeychainService: "s"},
		{Address: "host; rm -rf", Port: 8091, User: "alice", KeychainService: "s"},
		{Address: "-oHost", Port: 8091, User: "alice", KeychainService: "s"},
	}
	for _, h := range bad {
		if err := h.validate(); err == nil {
			t.Errorf("%+v should be rejected", h)
		}
	}
	if err := testHost.validate(); err != nil {
		t.Errorf("the normal host must stay valid: %v", err)
	}
	args := sshArgs(testHost, "cmd")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") || !strings.Contains(joined, "-- alice@100.64.0.9") {
		t.Errorf("ssh args must pin host keys and end the options before the target: %v", args)
	}
}

// callLog is a Runner that records every call.
type callLog struct{ calls [][]string }

func (c *callLog) Run(name string, args []string, _ string) (string, error) {
	c.calls = append(c.calls, append([]string{name}, args...))
	return `{"ok":true}`, nil
}

func TestUpdateCopiesThenRunsTheScriptWithTheHashAndCleansUp(t *testing.T) {
	zip := filepath.Join(t.TempDir(), "b.zip")
	if err := os.WriteFile(zip, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	if _, err := update(log, testHost, zip); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) != 3 || log.calls[0][0] != "scp" || log.calls[1][0] != "ssh" || log.calls[2][0] != "ssh" {
		t.Fatalf("want scp, ssh (update), ssh (cleanup), got %v", log.calls)
	}
	script := strings.Join(log.calls[1], " ")
	if !strings.Contains(script, "Update-WinBridgeGo.ps1") || !strings.Contains(script, "-Sha256 ") || !strings.Contains(script, "-Json") {
		t.Errorf("the update script call is wrong: %s", script)
	}
	if !strings.Contains(strings.Join(log.calls[0], " "), "StrictHostKeyChecking=yes") {
		t.Errorf("scp must pin host keys: %v", log.calls[0])
	}
	if _, err := update(log, testHost, filepath.Join(t.TempDir(), "missing.zip")); err == nil {
		t.Error("a missing bundle must fail before any copy")
	}
}

func TestRemoteOutputIsDecodedAsUTF8(t *testing.T) {
	host := Host{Address: "100.64.0.9", Port: 8091, User: "alice", KeychainService: "svc"}
	cases := []struct{ name, raw, want string }{
		{"accents pass through", "Excepción sesión", "Excepción sesión"},
		{"BOM is dropped", "\xef\xbb\xbf[OK] config", "[OK] config"},
		{"a run of invalid bytes becomes one U+FFFD", "a\xa2\xa3b", "a\ufffdb"},
		{"empty output stays empty", "\xef\xbb\xbf", ""},
		{"OEM bytes from a host not updated become U+FFFD", "Excepci\xa2n", "Excepci�n"},
	}
	for _, c := range cases {
		got, err := remote(&fakeRunner{output: c.raw}, host, "", "Test-WinBridgeInstall.ps1")
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// runCapture runs one command line against a hosts file in a temp folder and returns what it printed.
func runCapture(t *testing.T, cfg Config, r Runner, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts.json")
	if cfg.Hosts != nil {
		if err := saveConfig(path, cfg); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WINBRIDGE_CONFIG", path)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	runErr := run(args, r, out)
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestVersionDefaultsToDevWhenNotInjected(t *testing.T) {
	text, err := runCapture(t, Config{}, &callLog{}, "version")
	if err != nil || !strings.Contains(text, "dev") {
		t.Fatalf("an uninjected build must say dev, got %q (%v)", text, err)
	}
}

func TestHelpWorksWithoutAConfig(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "help"} {
		text, err := runCapture(t, Config{}, &callLog{}, arg)
		if err != nil {
			t.Fatalf("%s must not need a config: %v", arg, err)
		}
		for _, word := range []string{"adopt", "status", "update", "doctor", "logs"} {
			if !strings.Contains(text, word) {
				t.Errorf("%s: the help must list %q", arg, word)
			}
		}
	}
}

func TestFlagsAfterTheHostAreHonoured(t *testing.T) {
	cfg := Config{Hosts: map[string]Host{"laptop": testHost}}
	log := &callLog{}
	if _, err := runCapture(t, cfg, log, "logs", "laptop", "-n", "50"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log.calls[0], " "); !strings.Contains(got, "-Lines 50") {
		t.Errorf("logs HOST -n 50 must ask for 50 lines: %s", got)
	}
	zip := filepath.Join(t.TempDir(), "z.zip")
	if err := os.WriteFile(zip, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	log = &callLog{}
	if _, err := runCapture(t, cfg, log, "update", "laptop", "--bundle", zip); err != nil {
		t.Fatal(err)
	}
	if len(log.calls) == 0 || !strings.Contains(strings.Join(log.calls[0], " "), zip) {
		t.Errorf("update HOST --bundle Z must copy Z: %v", log.calls)
	}
}

func TestExplainHTTPStatus(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{401, "winbridge adopt"},
		{403, "allowlist"},
		{500, "unexpected"},
	}
	for _, c := range cases {
		if got := explainHTTPStatus(c.code); !strings.Contains(got, c.want) {
			t.Errorf("explainHTTPStatus(%d) = %q, want it to mention %q", c.code, got, c.want)
		}
	}
}

func TestCallExplainsA401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := dialMCP(newHTTPClient(), hostOf(t, srv), "wrong")
	if err == nil || !strings.Contains(err.Error(), "winbridge adopt") {
		t.Fatalf("a 401 on /mcp must say how to fix it, got %v", err)
	}
}

func TestStatusExplainsA401OnStatusWithTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := status(newHTTPClient(), &fakeSecrets{keys: map[string]string{"svc": "old"}}, hostOf(t, srv))
	if err == nil || !strings.Contains(err.Error(), "winbridge adopt") {
		t.Fatalf("a rejected key must say how to pair again, got %v", err)
	}
}
