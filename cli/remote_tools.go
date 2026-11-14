package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// takeHost removes `-host NAME` (or `--host NAME`) from args and returns NAME.
func takeHost(args []string) (string, []string) {
	var host string
	var rest []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-host" || args[i] == "--host") && i+1 < len(args) {
			host = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return host, rest
}

// remoteTools runs `call` and `jobs`: it resolves the host, reads its key from the Keychain and opens an MCP session.
func remoteTools(cmd string, args []string, cfg Config, s Secrets, out io.Writer) error {
	hostName, rest := takeHost(args)
	name, host, err := cfg.resolve(hostName)
	if err != nil {
		return err
	}
	if err := host.validate(); err != nil {
		return fmt.Errorf("host %s: %w", name, err)
	}
	key, err := s.Get(host.KeychainService)
	if err != nil {
		return err
	}
	c, err := dialMCP(newHTTPClientNoTimeout(), host, key)
	if err != nil {
		return fmt.Errorf("host %s: %w", name, err)
	}
	if cmd == "jobs" {
		return jobsCommand(c, rest, out)
	}
	return callCommand(c, rest, out)
}

// callCommand runs `winbridge call TOOL [-args JSON | -args-file FILE]` and prints the tool's text.
func callCommand(c toolCaller, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	raw := fs.String("args", "", "tool arguments as JSON")
	file := fs.String("args-file", "", "read the tool arguments from a JSON file")
	timeout := fs.Int("timeout", 120, "seconds the call may take")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: winbridge call TOOL [-args JSON | -args-file FILE] [-timeout SECONDS]")
	}
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		*raw = string(data)
	}
	var toolArgs map[string]any
	if strings.TrimSpace(*raw) != "" {
		if err := json.Unmarshal([]byte(*raw), &toolArgs); err != nil {
			return fmt.Errorf("arguments are not a JSON object: %w", err)
		}
	}
	text, isErr, err := c.callTool(time.Duration(*timeout)*time.Second, pos[0], toolArgs)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, text)
	if isErr {
		return fmt.Errorf("%s reported an error", pos[0])
	}
	return nil
}
