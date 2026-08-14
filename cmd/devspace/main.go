package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/snakex21/devspace-go/internal/config"
	"github.com/snakex21/devspace-go/internal/locales"
	"github.com/snakex21/devspace-go/internal/server"
	"github.com/snakex21/devspace-go/internal/shells"
	"github.com/snakex21/devspace-go/internal/tools"
	"github.com/snakex21/devspace-go/internal/version"
)

func main() {
	// Parse CLI arguments
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve", "start":
			runServer()
		case "init":
			os.Exit(runConfig(nil, os.Stdin, os.Stdout))
		case "doctor":
			runDoctor()
		case "config":
			os.Exit(runConfig(os.Args[2:], os.Stdin, os.Stdout))
		case "tunnel":
			os.Exit(runTunnel(os.Args[2:], os.Stdout))
		case "version", "--version", "-v":
			fmt.Println(version.String())
		case "help", "--help", "-h":
			printHelp()
		default:
			fmt.Printf("Unknown command: %s\n", os.Args[1])
			fmt.Println("Run 'mcp-webcoder help' for usage.")
		}
		return
	}

	// Default: run the server, or say how to configure it first.
	cfg := config.LoadConfig()

	if len(cfg.AllowedRoots) == 0 {
		fmt.Fprintln(os.Stderr, "MCP WebCoder is not configured: no folders are allowed yet.")
		fmt.Fprintln(os.Stderr, "Run 'mcp-webcoder config' to set one up.")
		os.Exit(1)
	}
	runServerWithConfig(cfg)
}

func runServer() {
	cfg := config.LoadConfig()

	if len(cfg.AllowedRoots) == 0 {
		fmt.Fprintln(os.Stderr, "Error: WEBCODER_ALLOWED_ROOTS must be set.")
		fmt.Fprintln(os.Stderr, "Run 'mcp-webcoder config' to configure.")
		os.Exit(1)
	}

	runServerWithConfig(cfg)
}

func runServerWithConfig(cfg *config.Config) {
	// Initialize locale system. An empty or unknown language falls back to
	// English inside SetLocale, so there is nothing to special-case here.
	locales.Init(cfg.Lang)

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func runDoctor() {
	fmt.Println("MCP WebCoder Doctor")
	fmt.Println("=====================")
	fmt.Println()

	// Check Go version
	fmt.Println("[ok] Go runtime")

	// Check config
	cfg := config.LoadConfig()
	if len(cfg.AllowedRoots) > 0 {
		fmt.Printf("[ok] Allowed roots: %s\n", strings.Join(cfg.AllowedRoots, ", "))
	} else {
		fmt.Println("[!] No allowed roots configured")
	}

	// Check shell availability. This used to claim PowerShell on Windows and
	// bash on Unix without looking, which said nothing about whether the
	// configured shell exists.
	labels := shells.Describe()
	if len(labels) == 0 {
		fmt.Println("[!] No supported shell was found")
	} else {
		fmt.Printf("[ok] Shells found: %d\n", len(labels))
		for _, label := range labels {
			fmt.Printf("     %s\n", label)
		}
	}

	tools.SetShell(cfg.Shell)
	if label, fallback, err := tools.ShellStatus(); err != nil {
		fmt.Printf("[!] bash tool: %v\n", err)
	} else {
		fmt.Printf("[ok] bash tool will use %s\n", label)
		if fallback != "" {
			fmt.Printf("     configured shell unusable: %s\n", fallback)
		}
	}

	// Check state directory
	if _, err := os.Stat(cfg.StateDir); os.IsNotExist(err) {
		fmt.Printf("[ok] State directory will be created: %s\n", cfg.StateDir)
	} else {
		fmt.Printf("[ok] State directory exists: %s\n", cfg.StateDir)
	}

	fmt.Println()
	fmt.Println("Ready to serve!")
}

func printHelp() {
	fmt.Print(`MCP WebCoder - MCP coding workspace over HTTP

Usage:
  mcp-webcoder [command]

Commands:
  serve       Start the MCP server (default)
  config      Configure interactively, or with get and set
  tunnel      Set up the Cloudflare tunnel the server runs
  doctor      Diagnostic checks
  init        Same as config, kept for older instructions
  version     Print the version of this build
  help        Show this help

Configuration:
  mcp-webcoder config                    Interactive prompts
  mcp-webcoder config show               Print every setting
  mcp-webcoder config set <key> <value>  Change one setting
  mcp-webcoder config path               Print the config file location
  mcp-webcoder config token              Print the bearer token, or add new to replace it

Tunnel:
  mcp-webcoder tunnel setup <hostname>   Create or reuse a tunnel for a hostname

Environment:
  WEBCODER_ALLOWED_ROOTS       Comma-separated folders the tools may use.
  WEBCODER_PUBLIC_BASE_URL     Public base URL (default: http://127.0.0.1:7676)
  HOST                         Listen host (default: 127.0.0.1)
  PORT                         Listen port (default: 7676)
`)
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	var result []string
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
