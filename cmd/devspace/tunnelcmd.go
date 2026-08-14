package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/tunnel"
)

const (
	// cloudflaredStepTimeout bounds one ordinary cloudflared call. These talk to
	// the Cloudflare API and answer in seconds, so a minute and a half is already
	// generous and a hung hour is not useful to anyone.
	cloudflaredStepTimeout = 90 * time.Second
	// cloudflaredLoginTimeout has to cover a person opening a browser, signing in
	// and picking a zone.
	cloudflaredLoginTimeout = 5 * time.Minute
)

// cloudflaredRunner runs one cloudflared command and returns everything it
// printed. It is injected so tests can drive setup without a Cloudflare account
// or a network.
type cloudflaredRunner func(args ...string) (string, error)

// runTunnel handles "mcp-webcoder tunnel ...".
func runTunnel(args []string, out io.Writer) int {
	if len(args) == 0 {
		printTunnelUsage(out)
		return 2
	}

	switch args[0] {
	case "setup":
		return runTunnelSetup(args[1:], out)
	case "help", "--help", "-h":
		printTunnelUsage(out)
		return 0
	default:
		fmt.Fprintf(out, "Unknown tunnel command: %s\n", args[0])
		printTunnelUsage(out)
		return 2
	}
}

func printTunnelUsage(out io.Writer) {
	fmt.Fprint(out, `Usage:
  mcp-webcoder tunnel setup <hostname> [name]

Points a hostname on one of your Cloudflare zones at this server and stores what
the server needs to run the tunnel itself. The only thing you have to bring is a
domain already on Cloudflare.

Running it again is safe: an existing tunnel and an existing DNS record are
reused rather than replaced.
`)
}

func runTunnelSetup(args []string, out io.Writer) int {
	if len(args) == 0 || len(args) > 2 {
		printTunnelUsage(out)
		return 2
	}

	hostname, err := tunnelHostname(args[0])
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 2
	}
	requested := ""
	if len(args) == 2 {
		requested = strings.TrimSpace(args[1])
	}

	exe, downloaded, err := tunnel.EnsureCloudflared(tunnel.ToolsDir(), nil)
	if err != nil {
		fmt.Fprintf(out, "cloudflared is needed and could not be installed: %v\n", err)
		return 1
	}
	if downloaded {
		fmt.Fprintf(out, "Downloaded cloudflared to %s\n", exe)
	} else {
		fmt.Fprintf(out, "Using cloudflared at %s\n", exe)
	}

	cfg := loadStoredConfig()
	if err := setupTunnel(cfg, hostname, requested, cloudflaredCommand(exe, out), out); err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}
	if err := writeConfigFile(cfg); err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}

	fmt.Fprintf(out, "Saved %s\n", configFilePath(cfg))
	printTunnelNextSteps(hostname, out)
	return 0
}

// setupTunnel brings Cloudflare and the stored config into the state the server
// needs, and is written to be run twice: every step either finds its work
// already done or does it.
func setupTunnel(cfg *config.Config, hostname, requestedName string, run cloudflaredRunner, out io.Writer) error {
	warnAboutIngressConfig(out)

	name := requestedName
	if name == "" {
		name = strings.TrimSpace(cfg.Tunnel.Cloudflared)
	}
	if name == "" {
		name = tunnelNameFor(hostname)
	}

	list, err := listTunnels(run, out)
	if err != nil {
		return err
	}

	id, resolved := existingTunnel(list, name)
	if resolved != "" {
		name = resolved
	}
	credentials := filepath.Join(cfg.ConfigDir, credentialsFileName(name))

	if id == "" {
		created, err := run("tunnel", "create", "--credentials-file", credentials, name)
		if err != nil {
			return fmt.Errorf("cloudflared could not create the tunnel %s: %w\n%s", name, err, strings.TrimSpace(created))
		}
		if id = tunnelIDFromCreate(created); id == "" {
			return fmt.Errorf("cloudflared created %s but did not report its id\n%s", name, strings.TrimSpace(created))
		}
		fmt.Fprintf(out, "Created tunnel %s\n", name)
	} else {
		fmt.Fprintf(out, "Reusing tunnel %s\n", name)
		// A tunnel's secret cannot be read back out of Cloudflare, but cloudflared
		// will write a fresh credentials file for a tunnel that already exists,
		// which is what makes this safe to run on a second machine.
		if !credentialsPresent(credentials) {
			if token, err := run("tunnel", "token", "--cred-file", credentials, name); err != nil {
				return fmt.Errorf("cloudflared could not write credentials for %s: %w\n%s", name, err, strings.TrimSpace(token))
			}
			fmt.Fprintf(out, "Wrote credentials to %s\n", credentials)
		}
	}
	if !credentialsPresent(credentials) {
		return fmt.Errorf("cloudflared did not write %s", credentials)
	}

	if err := routeHostname(run, name, hostname, out); err != nil {
		return err
	}

	cfg.Tunnel.Provider = config.TunnelCloudflared
	// The id rather than the name, because credentials identify a tunnel by id and
	// running it that way needs no login certificate on the machine.
	cfg.Tunnel.Cloudflared = id
	cfg.Tunnel.Credentials = credentialsFileName(name)
	cfg.PublicBaseURL = "https://" + hostname

	if strings.TrimSpace(cfg.AuthToken) == "" {
		token, err := newAuthToken()
		if err != nil {
			return err
		}
		cfg.AuthToken = token
		fmt.Fprintln(out, "Generated a bearer token, because a tunnel puts this server on the internet")
	}
	return nil
}

// listTunnels asks Cloudflare what already exists, logging in first when this
// machine has never been authorised.
func listTunnels(run cloudflaredRunner, out io.Writer) (string, error) {
	list, err := run("tunnel", "list", "--output", "json")
	if err == nil {
		return list, nil
	}
	if !needsLogin(list) {
		return "", fmt.Errorf("cloudflared could not list your tunnels: %w\n%s", err, strings.TrimSpace(list))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Cloudflare has to authorise this machine once.")
	fmt.Fprintln(out, "A browser will open: sign in and pick the zone that holds your hostname.")
	if login, err := run("tunnel", "login"); err != nil {
		return "", fmt.Errorf("cloudflared login failed: %w\n%s", err, strings.TrimSpace(login))
	}

	list, err = run("tunnel", "list", "--output", "json")
	if err != nil {
		return "", fmt.Errorf("cloudflared could not list your tunnels: %w\n%s", err, strings.TrimSpace(list))
	}
	return list, nil
}

// routeHostname points the hostname at the tunnel. A record that is already
// there is the expected outcome of a second run, so cloudflared's own words
// decide whether a failure matters.
func routeHostname(run cloudflaredRunner, name, hostname string, out io.Writer) error {
	routed, err := run("tunnel", "route", "dns", name, hostname)
	if err == nil {
		fmt.Fprintf(out, "Pointed %s at the tunnel\n", hostname)
		return nil
	}
	if routeAlreadyThere(routed) {
		fmt.Fprintf(out, "%s already has a DNS record; leaving it alone\n", hostname)
		return nil
	}
	return fmt.Errorf("cloudflared could not point %s at the tunnel: %w\n%s", hostname, err, strings.TrimSpace(routed))
}

// warnAboutIngressConfig names the file that would otherwise decide the route
// silently. cloudflared loads it whether or not this server passes --url, and
// depending on the version it either refuses the flag or quietly prefers the
// file, which is the one thing that breaks the server running its own tunnel.
func warnAboutIngressConfig(out io.Writer) {
	conflict := tunnel.ConfigFileWithIngress(tunnel.CloudflaredConfigCandidates())
	if conflict == "" {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Careful: %s defines ingress rules.\n", conflict)
	fmt.Fprintln(out, "cloudflared loads that file too, and it can outrank the port this server publishes.")
	fmt.Fprintln(out, "Rename it if the tunnel does not reach this server.")
}

// tunnelHostname accepts what a person is likely to paste and returns a bare
// hostname.
func tunnelHostname(raw string) (string, error) {
	host := config.NormalizeTunnelDomain(raw)
	switch {
	case host == "":
		return "", errors.New("give the hostname you want to use, such as mcp.example.com")
	case strings.ContainsAny(host, "/ \t"):
		return "", fmt.Errorf("%q is more than a hostname; give just the host, such as mcp.example.com", raw)
	case !strings.Contains(host, "."):
		return "", fmt.Errorf("%q has no domain; give a hostname on a zone Cloudflare serves for you", raw)
	}
	return host, nil
}

// tunnelNameFor derives a tunnel name from a hostname, so a second setup for the
// same hostname finds the tunnel the first one made. It doubles as the sanitiser
// for file names, since both have to survive being written down.
func tunnelNameFor(hostname string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return '-'
	}, strings.ToLower(strings.TrimSpace(hostname)))

	if name = strings.Trim(name, "-"); name == "" {
		return "webcoder"
	}
	return name
}

func credentialsFileName(name string) string {
	return "cloudflared-" + tunnelNameFor(name) + ".json"
}

// existingTunnel finds a tunnel by name or by id and returns both, so a config
// that stores the id still recognises the tunnel it named on the first run.
func existingTunnel(list, wanted string) (string, string) {
	var entries []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(jsonSpan(list, '[', ']')), &entries); err != nil {
		return "", ""
	}
	for _, entry := range entries {
		if entry.ID == wanted || strings.EqualFold(entry.Name, wanted) {
			return entry.ID, entry.Name
		}
	}
	return "", ""
}

var (
	// tunnelIDPattern matches the id in "Created tunnel x with id <uuid>".
	tunnelIDPattern = regexp.MustCompile(`(?i)\bid\b\D{0,4}([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	// anyUUIDPattern is the fallback for a version that words the line differently.
	anyUUIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

// tunnelIDFromCreate reads the new tunnel's id out of whatever cloudflared
// printed, because the id is what the server needs and the text is not a
// contract.
func tunnelIDFromCreate(output string) string {
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(jsonSpan(output, '{', '}')), &created); err == nil && created.ID != "" {
		return created.ID
	}
	if match := tunnelIDPattern.FindStringSubmatch(output); len(match) == 2 {
		return match[1]
	}
	return anyUUIDPattern.FindString(output)
}

// jsonSpan cuts the JSON out of output that may also carry log lines, since
// cloudflared logs and prints to the same place.
func jsonSpan(output string, open, close byte) string {
	start := strings.IndexByte(output, open)
	end := strings.LastIndexByte(output, close)
	if start < 0 || end <= start {
		return ""
	}
	return output[start : end+1]
}

// needsLogin reports whether cloudflared failed for want of the certificate that
// "tunnel login" writes, rather than for a reason worth reporting.
func needsLogin(output string) bool {
	lowered := strings.ToLower(output)
	for _, marker := range []string{
		"cert.pem",
		"origin certificate",
		"cloudflared login",
		"cloudflared tunnel login",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// routeAlreadyThere reports whether the DNS record setup wanted is already in
// place, which is success on a second run and not a failure.
func routeAlreadyThere(output string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "already exists") ||
		strings.Contains(lowered, "already configured to point to")
}

func credentialsPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// cloudflaredCommand runs the real thing. Output is collected so a failure can
// be quoted, and login also streams, because it prints a URL that has to be
// visible while it waits.
func cloudflaredCommand(exe string, out io.Writer) cloudflaredRunner {
	return func(args ...string) (string, error) {
		interactive := len(args) > 1 && args[1] == "login"
		timeout := cloudflaredStepTimeout
		if interactive {
			timeout = cloudflaredLoginTimeout
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		var collected bytes.Buffer
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Stdout = &collected
		cmd.Stderr = &collected
		if interactive {
			cmd.Stdout = io.MultiWriter(&collected, out)
			cmd.Stderr = io.MultiWriter(&collected, out)
			cmd.Stdin = os.Stdin
		}

		err := cmd.Run()
		return collected.String(), err
	}
}

func printTunnelNextSteps(hostname string, out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The server starts and stops the tunnel with itself, so nothing else to install.")
	fmt.Fprintln(out, "  mcp-webcoder serve")
	fmt.Fprintf(out, "  point your client at https://%s/mcp\n", hostname)
	fmt.Fprintln(out, "  mcp-webcoder config token   prints the bearer token the client has to send")
}
