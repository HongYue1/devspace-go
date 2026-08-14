package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// generatedTokenBytes is how much randomness a generated token carries. It
// matches the "openssl rand -hex 32" the setting suggests: far past what an
// attacker can search, and still one line to paste.
const generatedTokenBytes = 32

// runConfigToken prints the bearer token, or replaces it with a fresh one.
//
// Settings hide secrets so a pasted terminal cannot leak one, but that left no
// way to read the token a client must send: the config file was the only copy.
// Generating one with an outside tool was worse, because the value was never
// displayed, so a command that reported nothing but success locked out every
// client that had been working.
func runConfigToken(args []string, out io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(out, "Usage: mcp-webcoder config token [new]")
		return 2
	}
	if len(args) == 1 {
		switch strings.ToLower(args[0]) {
		case "new", "rotate", "generate":
			return replaceAuthToken(out)
		default:
			fmt.Fprintf(out, "Unknown token command: %s\n", args[0])
			fmt.Fprintln(out, "Usage: mcp-webcoder config token [new]")
			return 2
		}
	}

	cfg := loadStoredConfig()
	if cfg.AuthToken == "" {
		fmt.Fprintln(out, "No token is set, so every request is accepted.")
		fmt.Fprintln(out, "Run 'mcp-webcoder config token new' to require one.")
		return 0
	}
	fmt.Fprintln(out, cfg.AuthToken)
	reportTokenShadow(out)
	return 0
}

// replaceAuthToken stores a new token and prints it once. Printing is the point
// of the command, so it happens even though the settings table will not.
func replaceAuthToken(out io.Writer) int {
	token, err := newAuthToken()
	if err != nil {
		fmt.Fprintf(out, "Cannot generate a token: %v\n", err)
		return 1
	}

	cfg := loadStoredConfig()
	replaced := cfg.AuthToken != ""
	cfg.AuthToken = token
	if err := writeConfigFile(cfg); err != nil {
		fmt.Fprintf(out, "Cannot save: %v\n", err)
		return 1
	}

	fmt.Fprintln(out, token)
	fmt.Fprintf(out, "Saved %s\n", configFilePath(cfg))
	if replaced {
		fmt.Fprintln(out, "The previous token no longer works; update every client that used it.")
	}
	fmt.Fprintln(out, "Restart the server to require it.")
	reportTokenShadow(out)
	return 0
}

// newAuthToken draws from the system source. A token that guards a remote shell
// is not the place to economise on randomness.
func newAuthToken() (string, error) {
	buffer := make([]byte, generatedTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// reportTokenShadow warns when the environment overrides the stored token, so a
// printed value the server will not accept is never a silent surprise.
func reportTokenShadow(out io.Writer) {
	if s, ok := findSetting("authToken"); ok {
		warnIfShadowed(s, out)
	}
}
