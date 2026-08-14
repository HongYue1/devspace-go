package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/locales"
	"github.com/snakex21/devspace-go/internal/shells"
)

// setting describes one configurable field: how to render it, how to parse a
// new value for it, and which values a caller may pick from. The desktop
// configurator kept this knowledge inside widget callbacks, which is why a
// headless machine could not reach half of it.
type setting struct {
	key     string
	help    string
	env     string
	secret  bool
	show    func(*config.Config) string
	parse   func(*config.Config, string) error
	choices func(*config.Config) []string
	reset   func(*config.Config)
}

// settings lists every field the config command can read and write, in the
// order the interactive prompt presents them.
// display renders a value for the terminal. A secret is reported as set or not
// set rather than printed, because this output ends up in screenshots and pasted
// terminal sessions.
func (s setting) display(cfg *config.Config) string {
	value := s.show(cfg)
	switch {
	case !s.secret:
		return value
	case value == "":
		return "(none)"
	default:
		return "set (hidden)"
	}
}

func settings() []setting {
	return []setting{
		rootsSetting(),
		hostSetting(),
		portSetting(),
		publicURLSetting(),
		tunnelProviderSetting(),
		tunnelDomainSetting(),
		tunnelAuthtokenSetting(),
		tunnelCloudflaredSetting(),
		authTokenSetting(),
		shellSetting(),
		langSetting(),
		toolModeSetting(),
		toolNamingSetting(),
		widgetsSetting(),
		boolSetting("skills", "Load skills from the configured skill folders", "",
			func(c *config.Config) *bool { return &c.SkillsEnabled }),
		logLevelSetting(),
		logFormatSetting(),
		boolSetting("log.requests", "Log one line per HTTP request", "WEBCODER_LOG_REQUESTS",
			func(c *config.Config) *bool { return &c.Logging.Requests }),
		boolSetting("log.assets", "Include asset requests when request logging is on", "WEBCODER_LOG_ASSETS",
			func(c *config.Config) *bool { return &c.Logging.Assets }),
		boolSetting("log.toolCalls", "Log each tool call", "WEBCODER_LOG_TOOL_CALLS",
			func(c *config.Config) *bool { return &c.Logging.ToolCalls }),
		boolSetting("log.shellCommands", "Log the command line the bash tool runs", "WEBCODER_LOG_SHELL_COMMANDS",
			func(c *config.Config) *bool { return &c.Logging.ShellCommands }),
		boolSetting("log.trustProxy", "Trust X-Forwarded-For from a reverse proxy", "",
			func(c *config.Config) *bool { return &c.Logging.TrustProxy }),
		pathSetting("stateDir", "Where workspace state is stored",
			func(c *config.Config) *string { return &c.StateDir }),
		pathSetting("agentDir", "Where agent files are stored",
			func(c *config.Config) *string { return &c.AgentDir }),
		pathSetting("worktreeRoot", "Where git worktrees are created",
			func(c *config.Config) *string { return &c.WorktreeRoot }),
	}
}

// findSetting looks a key up the way people type it, so "Port" and "port" are
// the same setting.
func findSetting(key string) (setting, bool) {
	want := strings.ToLower(strings.TrimSpace(key))
	for _, s := range settings() {
		if strings.ToLower(s.key) == want {
			return s, true
		}
	}
	return setting{}, false
}

func settingKeys() []string {
	all := settings()
	keys := make([]string, 0, len(all))
	for _, s := range all {
		keys = append(keys, s.key)
	}
	return keys
}

func rootsSetting() setting {
	return setting{
		key:  "roots",
		help: "Folders the tools may read and write, separated by commas",
		env:  "WEBCODER_ALLOWED_ROOTS",
		show: func(c *config.Config) string { return joinValues(c.AllowedRoots) },
		parse: func(c *config.Config, value string) error {
			roots, err := parseRoots(value)
			if err != nil {
				return err
			}
			c.AllowedRoots = roots
			return nil
		},
		reset: func(c *config.Config) { c.AllowedRoots = []string{} },
	}
}

// parseRoots stores what the server already expects: absolute paths with
// forward slashes. A folder that is not there is refused here, because the
// alternative is a server that starts and then rejects every path.
func parseRoots(value string) ([]string, error) {
	entries := splitAndTrim(value, ",")
	if len(entries) == 0 {
		return nil, errors.New("name at least one folder")
	}
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		abs, err := filepath.Abs(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("cannot find %s", abs)
			}
			return nil, fmt.Errorf("cannot read %s: %w", abs, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is a file, not a folder", abs)
		}
		roots = append(roots, filepath.ToSlash(abs))
	}
	return roots, nil
}

func hostSetting() setting {
	return setting{
		key:  "host",
		help: "Address to listen on; 127.0.0.1 keeps the server off the network",
		env:  "HOST",
		show: func(c *config.Config) string { return c.Host },
		parse: func(c *config.Config, value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return errors.New("name an address")
			}
			c.Host = value
			return nil
		},
	}
}

func portSetting() setting {
	return setting{
		key:  "port",
		help: "TCP port to listen on",
		env:  "PORT",
		show: func(c *config.Config) string { return strconv.Itoa(c.Port) },
		parse: func(c *config.Config, value string) error {
			value = strings.TrimSpace(value)
			port, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("want a number, got %q", value)
			}
			if port < 1 || port > 65535 {
				return fmt.Errorf("want a port between 1 and 65535, got %d", port)
			}
			c.Port = port
			return nil
		},
	}
}

func publicURLSetting() setting {
	return setting{
		key:  "publicUrl",
		help: "Base URL clients use to reach this server",
		env:  "WEBCODER_PUBLIC_BASE_URL",
		show: func(c *config.Config) string { return c.PublicBaseURL },
		parse: func(c *config.Config, value string) error {
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				return fmt.Errorf("want a URL starting with http:// or https://, got %q", value)
			}
			c.PublicBaseURL = strings.TrimRight(value, "/")
			return nil
		},
	}
}

func tunnelProviderSetting() setting {
	providers := config.TunnelProviders()
	allowed := make([]string, 0, len(providers))
	for _, provider := range providers {
		allowed = append(allowed, string(provider))
	}

	return choiceSetting("tunnel.provider", "Which service publishes this server; ngrok is the one that can keep a URL", "WEBCODER_TUNNEL_PROVIDER",
		allowed,
		func(c *config.Config) string { return string(c.Tunnel.Provider) },
		func(c *config.Config, value string) { c.Tunnel.Provider = config.TunnelProvider(value) })
}

// tunnelDomainSetting takes a reserved ngrok domain, which is the one thing that
// makes the public URL survive a restart.
func tunnelDomainSetting() setting {
	return setting{
		key:  "tunnel.domain",
		help: "Reserved ngrok domain such as example.ngrok-free.app; blank means a random URL",
		env:  "WEBCODER_TUNNEL_DOMAIN",
		show: func(c *config.Config) string { return c.Tunnel.Domain },
		parse: func(c *config.Config, value string) error {
			domain := config.NormalizeTunnelDomain(value)
			if domain == "" {
				return errors.New("name a domain, or reset it to go back to a random URL")
			}
			if strings.ContainsAny(domain, "/ ") {
				return fmt.Errorf("want a host name, got %q", value)
			}
			if !strings.Contains(domain, ".") {
				return fmt.Errorf("want a full host name such as example.ngrok-free.app, got %q", value)
			}
			c.Tunnel.Domain = domain
			return nil
		},
		reset: func(c *config.Config) { c.Tunnel.Domain = "" },
	}
}

// tunnelCloudflaredSetting names a tunnel made with "cloudflared tunnel create".
//
// The hostname routed to it is not asked for here. cloudflared never reports
// one, so publicUrl supplies it and the tunnel refuses to start without it,
// which keeps the public URL in a single place.
func tunnelCloudflaredSetting() setting {
	return setting{
		key:  "tunnel.cloudflared",
		help: "Named Cloudflare tunnel to run, with publicUrl set to its hostname; blank means a new URL each run",
		env:  "WEBCODER_TUNNEL_CLOUDFLARED",
		show: func(c *config.Config) string { return c.Tunnel.Cloudflared },
		parse: func(c *config.Config, value string) error {
			name := strings.TrimSpace(value)
			if name == "" {
				return errors.New("name the tunnel, or reset it to go back to a quick tunnel")
			}
			if strings.ContainsAny(name, "/ ") {
				return fmt.Errorf("want a tunnel name or UUID, got %q", value)
			}
			c.Tunnel.Cloudflared = name
			return nil
		},
		reset: func(c *config.Config) { c.Tunnel.Cloudflared = "" },
	}
}

// minAuthTokenLength is the shortest token accepted. A public URL is scanned
// within minutes of being routed, so a memorable token is a weak lock on a
// remote shell; 16 characters is the point where guessing stops being the
// cheapest way in.
const minAuthTokenLength = 16

// authTokenSetting protects every endpoint but the health check with a bearer
// token. It is stored in the config file, which is created private, and never
// printed back.
func authTokenSetting() setting {
	return setting{
		key:    "authToken",
		help:   "Bearer token required on every request; generate one with: openssl rand -hex 32",
		env:    "WEBCODER_AUTH_TOKEN",
		secret: true,
		show:   func(c *config.Config) string { return c.AuthToken },
		parse: func(c *config.Config, value string) error {
			token := strings.TrimSpace(value)
			if token == "" {
				return errors.New("paste a token, or reset it to serve without authentication")
			}
			if strings.ContainsAny(token, " \t\r\n") {
				return errors.New("a bearer token cannot contain whitespace")
			}
			if len(token) < minAuthTokenLength {
				return fmt.Errorf("want at least %d characters, got %d: try openssl rand -hex 32", minAuthTokenLength, len(token))
			}
			c.AuthToken = token
			return nil
		},
		reset: func(c *config.Config) { c.AuthToken = "" },
	}
}

// tunnelAuthtokenSetting stores the token the ngrok agent authenticates with. It
// is written to the config file, which is created private, and never printed.
func tunnelAuthtokenSetting() setting {
	return setting{
		key:    "tunnel.authtoken",
		help:   "ngrok authtoken from the dashboard; stored locally and never printed back",
		env:    "WEBCODER_TUNNEL_AUTHTOKEN",
		secret: true,
		show:   func(c *config.Config) string { return c.Tunnel.Authtoken },
		parse: func(c *config.Config, value string) error {
			token := strings.TrimSpace(value)
			if token == "" {
				return errors.New("paste the authtoken, or reset it to remove the stored one")
			}
			c.Tunnel.Authtoken = token
			return nil
		},
		reset: func(c *config.Config) { c.Tunnel.Authtoken = "" },
	}
}

// shellSetting reuses the detection the bash tool uses, so an unusable choice
// is refused here with the same explanation doctor would print later.
func shellSetting() setting {
	return setting{
		key:     "shell",
		help:    "Shell the bash tool runs commands with; auto picks the first one detected",
		env:     "WEBCODER_SHELL",
		show:    func(c *config.Config) string { return c.Shell },
		choices: func(c *config.Config) []string { return shells.Options(c.Shell) },
		parse: func(c *config.Config, value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return errors.New("name a shell, or auto to pick the first one detected")
			}
			if _, err := shells.Resolve(value); err != nil {
				return err
			}
			c.Shell = value
			return nil
		},
	}
}

func langSetting() setting {
	return choiceSetting("lang", "Interface language; auto follows the operating system", "WEBCODER_LANG",
		langChoices(),
		func(c *config.Config) string { return c.Lang },
		func(c *config.Config, value string) { c.Lang = value })
}

func langChoices() []string {
	return append([]string{"auto"}, locales.AvailableLocales()...)
}

func toolModeSetting() setting {
	return choiceSetting("toolMode", "full exposes every tool, minimal exposes the core file tools", "",
		[]string{string(config.ToolModeFull), string(config.ToolModeMinimal)},
		func(c *config.Config) string { return string(c.ToolMode) },
		func(c *config.Config, value string) { c.ToolMode = config.ToolMode(value) })
}

func toolNamingSetting() setting {
	return choiceSetting("toolNaming", "short names tools read and write, legacy keeps the older prefixed names", "",
		[]string{string(config.NamingShort), string(config.NamingLegacy)},
		func(c *config.Config) string { return string(c.ToolNaming) },
		func(c *config.Config, value string) { c.ToolNaming = config.ToolNaming(value) })
}

func widgetsSetting() setting {
	return choiceSetting("widgets", "How much interface is attached to tool responses", "",
		[]string{string(config.WidgetFull), string(config.WidgetChanges), string(config.WidgetOff)},
		func(c *config.Config) string { return string(c.Widgets) },
		func(c *config.Config, value string) { c.Widgets = config.WidgetMode(value) })
}

func logLevelSetting() setting {
	return choiceSetting("log.level", "How much the server writes to its log", "WEBCODER_LOG_LEVEL",
		[]string{
			string(config.LogSilent),
			string(config.LogError),
			string(config.LogWarn),
			string(config.LogInfo),
			string(config.LogDebug),
		},
		func(c *config.Config) string { return string(c.Logging.Level) },
		func(c *config.Config, value string) { c.Logging.Level = config.LogLevel(value) })
}

func logFormatSetting() setting {
	return choiceSetting("log.format", "text is for people, json is for log collectors", "WEBCODER_LOG_FORMAT",
		[]string{string(config.LogText), string(config.LogJSON)},
		func(c *config.Config) string { return string(c.Logging.Format) },
		func(c *config.Config, value string) { c.Logging.Format = config.LogFormat(value) })
}

func choiceSetting(key, help, env string, allowed []string, show func(*config.Config) string, apply func(*config.Config, string)) setting {
	return setting{
		key:     key,
		help:    help,
		env:     env,
		show:    show,
		choices: func(*config.Config) []string { return allowed },
		parse: func(c *config.Config, value string) error {
			picked, err := parseChoice(value, allowed)
			if err != nil {
				return err
			}
			apply(c, picked)
			return nil
		},
	}
}

func boolSetting(key, help, env string, field func(*config.Config) *bool) setting {
	return setting{
		key:     key,
		help:    help,
		env:     env,
		show:    func(c *config.Config) string { return strconv.FormatBool(*field(c)) },
		choices: func(*config.Config) []string { return []string{"true", "false"} },
		parse: func(c *config.Config, value string) error {
			parsed, err := parseBool(value)
			if err != nil {
				return err
			}
			*field(c) = parsed
			return nil
		},
	}
}

func pathSetting(key, help string, field func(*config.Config) *string) setting {
	return setting{
		key:  key,
		help: help,
		show: func(c *config.Config) string { return *field(c) },
		parse: func(c *config.Config, value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return errors.New("name a folder")
			}
			abs, err := filepath.Abs(value)
			if err != nil {
				return fmt.Errorf("%s: %w", value, err)
			}
			*field(c) = abs
			return nil
		},
	}
}

// parseBool accepts what people actually type at a prompt, not only the two
// words Go's parser knows.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "y", "on", "true", "yes":
		return true, nil
	case "0", "f", "n", "off", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("want true or false, got %q", strings.TrimSpace(value))
}

func parseChoice(value string, allowed []string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if strings.ToLower(candidate) == want {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("want one of %s, got %q", strings.Join(allowed, ", "), strings.TrimSpace(value))
}

func joinValues(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
