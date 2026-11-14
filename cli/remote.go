package main

import (
	"bytes"
	"os/exec"
	"strings"
)

// Runner starts a process, feeds it stdin and returns its combined output.
type Runner interface {
	Run(name string, args []string, stdin string) (string, error)
}

// execRunner runs real processes.
type execRunner struct{}

// Run executes the command and returns stdout and stderr together.
func (execRunner) Run(name string, args []string, stdin string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// sshArgs builds the ssh arguments for one remote command; nothing secret goes in them.
func sshArgs(h Host, remote string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes"}
	if h.SSHKey != "" {
		args = append(args, "-i", expandHome(h.SSHKey))
	}
	return append(args, "--", h.User+"@"+h.Address, remote)
}

// psScript builds the cmd.exe line that runs an installed WinBridge script.
func psScript(name string, args ...string) string {
	parts := []string{
		"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
		`%LOCALAPPDATA%\winbridge\bin\` + name,
	}
	return strings.Join(append(parts, args...), " ")
}

// decodeRemote reads the host's output as UTF-8: it drops a BOM and turns bytes that are not UTF-8
// (a host that still writes its OEM code page) into U+FFFD, one per run of bytes, instead of passing them on.
func decodeRemote(s string) string {
	return strings.ToValidUTF8(strings.TrimPrefix(s, "\xef\xbb\xbf"), "\ufffd")
}

// remote runs an installed script on the host and returns its output.
func remote(r Runner, h Host, stdin, script string, args ...string) (string, error) {
	out, err := r.Run("ssh", sshArgs(h, psScript(script, args...)), stdin)
	return decodeRemote(out), err
}
