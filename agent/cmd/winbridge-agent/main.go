// Command winbridge-agent is the WinBridge MCP server for Windows: nine desktop tools over Tailscale.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/partprogramming09/winbridge/agent/internal/config"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/diag"
	"github.com/partprogramming09/winbridge/agent/internal/gateway"
	"github.com/partprogramming09/winbridge/agent/internal/jobs"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
	"github.com/partprogramming09/winbridge/agent/internal/tasks"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.2.0-dev"

const upstream = "native-go"

// main runs the agent and exits non-zero on a start-up error or a panic, logging the exit cause.
func main() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("exit cause: panic", "panic", r, "stack", string(debug.Stack()))
			os.Exit(2)
		}
	}()
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "winbridge-agent:", err)
		os.Exit(1)
	}
}

// run parses the flags, checks the profile and serves until a signal arrives.
func run(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("winbridge-agent", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to winbridge.toml")
	showVersion := fs.Bool("version", false, "print the version and exit")
	printConfig := fs.Bool("print-config", false, "print the config summary as JSON and exit")
	hostFlag := fs.String("host", "", "listen address, overrides server.host")
	portFlag := fs.Int("port", 0, "listen port, overrides server.port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("winbridge-agent", version)
		return nil
	}
	if *configPath == "" {
		return errors.New("--config is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *printConfig {
		if _, err := buildGateway(cfg); err != nil {
			return err
		}
		if _, err := buildJobs(cfg); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"host": cfg.Server.Host, "port": cfg.Server.Port, "ip_allowlist": cfg.Security.IPAllowlist,
			"enable_tier3": cfg.Security.EnableTier3, "tools": cfg.Tools.Enable, "exclude": cfg.Tools.Exclude,
		})
	}
	if *hostFlag != "" {
		cfg.Server.Host = *hostFlag
	}
	if *portFlag != 0 {
		cfg.Server.Port = *portFlag
	}
	key := getenv("WINREMOTE_AUTH_KEY")
	allow, err := cfg.AllowedNetworks()
	if err != nil {
		return err
	}
	tools := cfg.EnabledTools()
	strict := getenv("WINBRIDGE_STRICT") == "1"
	if strict {
		check := profile.Strict{
			Host: cfg.Server.Host, Port: cfg.Server.Port, HasKey: key != "",
			AllowInsecureRemote: cfg.Server.AllowInsecureRemote, EnableTier3: cfg.Security.EnableTier3,
			EnabledTools: tools, Allowlist: allow,
		}
		if err := check.Validate(); err != nil {
			return err
		}
	} else if key == "" {
		return errors.New("WINREMOTE_AUTH_KEY is required: the agent has no unauthenticated mode")
	}
	dsk, err := desktop.New()
	if err != nil {
		return err
	}
	var runner *tasks.Runner
	if len(cfg.Tasks) > 0 {
		specs := make(map[string]tasks.Spec, len(cfg.Tasks))
		for name, t := range cfg.Tasks {
			specs[name] = tasks.Spec{Script: t.Script, SHA256: t.SHA256}
		}
		if runner, err = tasks.New(specs, tasks.DefaultShell); err != nil {
			return err
		}
	}
	gw, err := buildGateway(cfg)
	if err != nil {
		return err
	}
	if gw != nil {
		defer gw.Close()
	}
	jobStore, err := buildJobs(cfg)
	if err != nil {
		return err
	}
	opts := server.Options{
		Version: version, Upstream: upstream, Key: key, Allowlist: allow, Desktop: dsk, Tools: tools, Strict: strict,
	}
	if runner != nil {
		opts.Tasks = runner
	}
	if gw != nil {
		opts.Gateway = gw
	}
	if jobStore != nil {
		opts.Jobs = jobStore
	}
	opts.Procs = procs.List
	root := ""
	if local := getenv("LOCALAPPDATA"); local != "" {
		root = filepath.Join(local, "winbridge")
	}
	opts.Diag = diag.New(diag.Options{
		Version: version, Upstream: upstream, Strict: strict, Host: cfg.Server.Host, Port: cfg.Server.Port,
		Tools: tools, Root: root, Desktop: dsk, Secret: key,
	}).Collect
	handler := server.New(opts)
	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(os.Stderr, "winbridge-agent v%s [auth ON] [%s] [tools: %d/%d]\n", version, addr, len(tools), len(profile.Tools))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err := <-done:
		slog.Error("exit cause: server stopped", "error", err)
		return err
	case <-ctx.Done():
		slog.Info("exit cause: interrupt or SIGTERM received, shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// buildJobs returns the job store for the [jobs] table, or nil when it is absent. The root must be an absolute path
// that holds manifest.json, so a typo stops the agent at start instead of at the first call.
func buildJobs(cfg config.Config) (*jobs.Store, error) {
	if cfg.Jobs.Root == "" {
		return nil, nil
	}
	if !filepath.IsAbs(cfg.Jobs.Root) {
		return nil, fmt.Errorf("jobs.root must be an absolute path, got %q", cfg.Jobs.Root)
	}
	return jobs.New(cfg.Jobs.Root)
}

// buildGateway returns the gateway for the [servers.*] tables, or nil when there are none.
func buildGateway(cfg config.Config) (*gateway.Gateway, error) {
	if len(cfg.Servers) == 0 {
		return nil, nil
	}
	specs := make(map[string]gateway.Spec, len(cfg.Servers))
	for name, c := range cfg.Servers {
		specs[name] = gateway.Spec{
			Command: c.Command, Args: c.Args, SHA256: c.SHA256, Files: c.Files, Env: c.Env, AllowTools: c.AllowTools, DenyArgs: c.DenyArgs,
			Timeout: time.Duration(c.Timeout) * time.Second, Idle: time.Duration(c.Idle) * time.Second, MaxText: c.MaxOutput,
		}
	}
	return gateway.New(specs)
}
