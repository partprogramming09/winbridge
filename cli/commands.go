package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// expectedTools is the number of profile tools the strict WinBridge profile exposes. The agent also has built-in
// tools (Diagnose, Processes, and the jobs and gateway ones when configured); /status keeps them out of tools_count.
const expectedTools = 9

// StatusReport is what `winbridge status` learns from /health and /status.
type StatusReport struct {
	Version    string `json:"version"`
	Upstream   string `json:"upstream"`
	Profile    string `json:"profile"`
	Strict     bool   `json:"strict"`
	ToolsCount int    `json:"tools_count"`
}

// baseURL returns the MCP server address of a host.
func baseURL(h Host) string {
	return "http://" + h.Address + ":" + strconv.Itoa(h.Port)
}

// getJSON fetches a URL with an optional bearer key and decodes the JSON body.
func getJSON(client *http.Client, url, key string, v any) (int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("%s answered %d: %s", url, resp.StatusCode, explainHTTPStatus(resp.StatusCode))
	}
	return resp.StatusCode, json.Unmarshal(body, v)
}

// explainHTTPStatus says what an HTTP error from the host most likely means and how to fix it.
func explainHTTPStatus(code int) string {
	switch code {
	case http.StatusUnauthorized:
		return "the key was rejected: pair again with `winbridge adopt`"
	case http.StatusForbidden:
		return "most likely this Mac's Tailscale address is not in the server allowlist; check client_ip and pair again with `winbridge adopt`"
	}
	return "unexpected answer from the host"
}

// status checks /health without a key, then /status with the Keychain key, and that the key is required.
func status(client *http.Client, s Secrets, h Host) (StatusReport, error) {
	var report StatusReport
	if code, err := getJSON(client, baseURL(h)+"/health", "", &report); err != nil {
		if code == http.StatusForbidden {
			return report, fmt.Errorf("403 on /health: %s", explainHTTPStatus(code))
		}
		return report, fmt.Errorf("the server does not answer /health: %w", err)
	}
	key, err := s.Get(h.KeychainService)
	if err != nil {
		return report, err
	}
	if _, err := getJSON(client, baseURL(h)+"/status", key, &report); err != nil {
		return report, fmt.Errorf("/status with the Keychain key failed: %w", err)
	}
	var anonymous StatusReport
	if code, _ := getJSON(client, baseURL(h)+"/status", "", &anonymous); code != http.StatusUnauthorized {
		return report, fmt.Errorf("/status without a key answered %d, expected 401", code)
	}
	if report.Profile != "winbridge" || !report.Strict || report.ToolsCount != expectedTools {
		return report, fmt.Errorf("unexpected profile: %+v", report)
	}
	return report, nil
}

// newKey returns a random URL-safe auth key of 64 characters.
func newKey() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// AdoptResult is the JSON line Set-WinBridgeKey.ps1 prints when pairing works.
type AdoptResult struct {
	OK          bool   `json:"ok"`
	Fingerprint string `json:"fingerprint"`
	ToolsCount  int    `json:"tools_count"`
	Profile     string `json:"profile"`
	Port        int    `json:"port"`
}

// lastJSONLine returns the last output line that looks like a JSON object.
func lastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}

// adopt generates a key, keeps it in the Keychain and sends it to the host on stdin; a failure restores the old key.
func adopt(r Runner, s Secrets, h Host) (AdoptResult, error) {
	var result AdoptResult
	if h.ClientIP == "" {
		return result, errors.New("client_ip (this Mac's Tailscale address) is required to adopt")
	}
	key, err := newKey()
	if err != nil {
		return result, err
	}
	previous, hadPrevious := "", false
	if old, err := s.Get(h.KeychainService); err == nil {
		previous, hadPrevious = old, true
	}
	if err := s.Set(h.KeychainService, key); err != nil {
		return result, fmt.Errorf("could not store the key in the Keychain: %w", err)
	}
	out, err := remote(r, h, key+"\n", "Set-WinBridgeKey.ps1", "-Json", "-ClientIp", h.ClientIP, "-TimeoutSeconds", "100")
	line := lastJSONLine(out)
	if err == nil && line != "" {
		err = json.Unmarshal([]byte(line), &result)
	}
	if err == nil && (!result.OK || result.ToolsCount != expectedTools) {
		err = fmt.Errorf("the host did not confirm the strict profile: %s", line)
	}
	if err != nil {
		if hadPrevious {
			_ = s.Set(h.KeychainService, previous)
		}
		return result, fmt.Errorf("adopt failed: %w", err)
	}
	return result, nil
}

// newHTTPClient returns the client used for status checks.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 8 * time.Second}
}

// newHTTPClientNoTimeout returns a client whose calls are bounded by their own contexts, for long tool calls.
func newHTTPClientNoTimeout() *http.Client { return &http.Client{} }

// update copies a bundle ZIP to the host over scp, runs the host's update script (no elevation) with the ZIP's SHA-256,
// and removes the copy. The script puts the previous agent back when the new one does not come up.
func update(r Runner, h Host, bundle string) (string, error) {
	data, err := os.ReadFile(bundle)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	copyArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes"}
	if h.SSHKey != "" {
		copyArgs = append(copyArgs, "-i", expandHome(h.SSHKey))
	}
	copyArgs = append(copyArgs, "--", bundle, h.User+"@"+h.Address+":wb-update.zip")
	if out, err := r.Run("scp", copyArgs, ""); err != nil {
		return out, fmt.Errorf("copying the bundle failed: %w", err)
	}
	text, err := remote(r, h, "", "Update-WinBridgeGo.ps1", "-BundlePath", `%USERPROFILE%\wb-update.zip`, "-Sha256", hash, "-Json")
	_, _ = r.Run("ssh", sshArgs(h, `del "%USERPROFILE%\wb-update.zip"`), "")
	return text, err
}
