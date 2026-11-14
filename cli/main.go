// Command winbridge manages WinBridge hosts (Windows PCs on Tailscale) from the Mac.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
)

// version is set at build time with -ldflags "-X main.version=<semver>"; a plain `go build` says dev.
var version = "dev"

const usage = `winbridge manages WinBridge hosts from the Mac.

Usage:
  winbridge hosts                       list configured hosts
  winbridge hosts add NAME [flags]      add or replace a host (--address --port --user --ssh-key --client-ip)
  winbridge status [HOST]               check /health, /status and that the key is required
  winbridge doctor [HOST]               run the host's doctor over SSH
  winbridge logs [HOST] [-n LINES]      show the host's logs (redacted by the host)
  winbridge restart [HOST]              restart the server through its supervisor
  winbridge adopt [HOST]                create a new key, keep it in the Keychain and pair the host
  winbridge update [HOST] --bundle ZIP  update a Go install from a bundle ZIP over SSH, no UAC; rolls back on failure
  winbridge call [--host H] TOOL [-args JSON | -args-file F]   call any tool of the host
  winbridge jobs [--host H] list|busy|submit JOB [K=V...]|run JOB [K=V...]|status ID|wait ID|cancel ID
                                        run jobs on the host's elevated runner (-queue, -timeout, -offset, -limit)
  winbridge version
`

// main dispatches the subcommand and exits non-zero on failure.
func main() {
	if err := run(os.Args[1:], execRunner{}, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "winbridge:", err)
		os.Exit(1)
	}
}

// run executes one command line; Runner and the output writer are injectable for tests.
func run(args []string, r Runner, out *os.File) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	case "version", "--version":
		fmt.Fprintln(out, "winbridge", version)
		return nil
	}
	path := configPath()
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if cmd == "hosts" {
		return hostsCommand(rest, path, cfg, out)
	}
	if cmd == "call" || cmd == "jobs" {
		return remoteTools(cmd, rest, cfg, macKeychain{run: r}, out)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	lines := fs.Int("n", 80, "log lines")
	bundle := fs.String("bundle", "", "bundle ZIP for update")
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	hostArg := ""
	if len(pos) > 0 {
		hostArg = pos[0]
	}
	name, host, err := cfg.resolve(hostArg)
	if err != nil {
		return err
	}
	if err := host.validate(); err != nil {
		return fmt.Errorf("host %s: %w", name, err)
	}
	secrets := macKeychain{run: r}
	switch cmd {
	case "status":
		report, err := status(newHTTPClient(), secrets, host)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: ok, version %s, upstream %s, profile %s, strict, %d tools\n",
			name, report.Version, report.Upstream, report.Profile, report.ToolsCount)
	case "doctor":
		text, err := remote(r, host, "", "Test-WinBridgeInstall.ps1")
		fmt.Fprint(out, text)
		return err
	case "logs":
		text, err := remote(r, host, "", "Get-WinBridgeLogs.ps1", "-Lines", strconv.Itoa(*lines))
		fmt.Fprint(out, text)
		return err
	case "restart":
		text, err := remote(r, host, "", "Restart-WinBridge.ps1", "-Json")
		fmt.Fprint(out, text)
		return err
	case "update":
		if *bundle == "" {
			return errors.New("update needs --bundle ZIP")
		}
		text, err := update(r, host, *bundle)
		fmt.Fprint(out, text)
		return err
	case "adopt":
		result, err := adopt(r, secrets, host)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: paired, key fingerprint %s, %d tools, profile %s\n",
			name, result.Fingerprint, result.ToolsCount, result.Profile)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

// hostsCommand lists hosts or adds one.
func hostsCommand(args []string, path string, cfg Config, out *os.File) error {
	if len(args) == 0 {
		for _, n := range cfg.names() {
			h := cfg.Hosts[n]
			fmt.Fprintf(out, "%s\t%s:%d\t%s\n", n, h.Address, h.Port, h.User)
		}
		return nil
	}
	if args[0] != "add" || len(args) < 2 {
		return fmt.Errorf("usage: winbridge hosts add NAME [flags]")
	}
	name := args[1]
	fs := flag.NewFlagSet("hosts add", flag.ContinueOnError)
	host := Host{Port: 8091, SSHKey: "~/.ssh/id_ed25519"}
	fs.StringVar(&host.Address, "address", "", "Tailscale address of the PC")
	fs.IntVar(&host.Port, "port", host.Port, "MCP port")
	fs.StringVar(&host.User, "user", "", "Windows user for SSH")
	fs.StringVar(&host.SSHKey, "ssh-key", host.SSHKey, "SSH private key")
	fs.StringVar(&host.ClientIP, "client-ip", "", "this Mac's Tailscale address")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	host.KeychainService = "winbridge-" + name + "-" + strconv.Itoa(host.Port)
	if err := host.validate(); err != nil {
		return err
	}
	cfg.Hosts[name] = host
	if err := saveConfig(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "host %s saved in %s\n", name, path)
	return nil
}
