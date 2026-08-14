package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/snakex21/devspace-go/internal/config"
)

const configFileName = "config.json"

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// runConfig implements the config command and returns the process exit code, so
// that a script can tell a refused value from an applied one.
func runConfig(args []string, in io.Reader, out io.Writer) int {
	if len(args) == 0 {
		return runConfigWizard(in, out)
	}

	switch strings.ToLower(args[0]) {
	case "get", "show", "list":
		return runConfigGet(args[1:], out)
	case "set":
		return runConfigSet(args[1:], out)
	case "unset", "reset":
		return runConfigUnset(args[1:], out)
	case "keys":
		for _, key := range settingKeys() {
			fmt.Fprintln(out, key)
		}
		return 0
	case "path":
		return runConfigPath(out)
	case "edit":
		return runConfigEdit(out)
	case "help", "--help", "-h":
		printConfigUsage(out)
		return 0
	default:
		fmt.Fprintf(out, "Unknown config command: %s\n\n", args[0])
		printConfigUsage(out)
		return 2
	}
}

func runConfigGet(args []string, out io.Writer) int {
	cfg := loadStoredConfig()
	if len(args) == 0 {
		printConfigTable(cfg, out)
		return 0
	}
	s, ok := findSetting(args[0])
	if !ok {
		return reportUnknownKey(args[0], out)
	}
	fmt.Fprintln(out, s.display(cfg))
	return 0
}

func runConfigSet(args []string, out io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(out, "Usage: mcp-webcoder config set <key> <value>")
		return 2
	}
	s, ok := findSetting(args[0])
	if !ok {
		return reportUnknownKey(args[0], out)
	}
	cfg := loadStoredConfig()
	// The remaining arguments are joined so that an unquoted Windows path with
	// spaces in it still arrives as one value.
	if err := s.parse(cfg, strings.Join(args[1:], " ")); err != nil {
		fmt.Fprintf(out, "Cannot set %s: %v\n", s.key, err)
		return 1
	}
	if err := writeConfigFile(cfg); err != nil {
		fmt.Fprintf(out, "Cannot save: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "%s = %s\n", s.key, s.display(cfg))
	fmt.Fprintf(out, "Saved %s\n", configFilePath(cfg))
	warnIfShadowed(s, out)
	return 0
}

func runConfigUnset(args []string, out io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(out, "Usage: mcp-webcoder config unset <key>")
		return 2
	}
	s, ok := findSetting(args[0])
	if !ok {
		return reportUnknownKey(args[0], out)
	}
	cfg := loadStoredConfig()
	defaults := config.DefaultConfig()
	if s.reset != nil {
		s.reset(cfg)
	} else if err := s.parse(cfg, s.show(defaults)); err != nil {
		fmt.Fprintf(out, "Cannot restore the default for %s: %v\n", s.key, err)
		return 1
	}
	if err := writeConfigFile(cfg); err != nil {
		fmt.Fprintf(out, "Cannot save: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "%s = %s (default)\n", s.key, s.display(cfg))
	fmt.Fprintf(out, "Saved %s\n", configFilePath(cfg))
	warnIfShadowed(s, out)
	return 0
}

func runConfigPath(out io.Writer) int {
	cfg := loadStoredConfig()
	path := configFilePath(cfg)
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(out, "%s (not created yet)\n", path)
		return 0
	}
	fmt.Fprintln(out, path)
	return 0
}

// runConfigEdit opens the file in the editor named by the environment, which is
// the one thing a text interface cannot reasonably guess.
func runConfigEdit(out io.Writer) int {
	cfg := loadStoredConfig()
	path := configFilePath(cfg)
	if _, err := os.Stat(path); err != nil {
		if err := writeConfigFile(cfg); err != nil {
			fmt.Fprintf(out, "Cannot create %s: %v\n", path, err)
			return 1
		}
	}
	name, args := editorCommand()
	if name == "" {
		fmt.Fprintf(out, "Set VISUAL or EDITOR to choose an editor. The file is %s\n", path)
		return 1
	}
	cmd := exec.Command(name, append(args, path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(out, "%s did not finish cleanly: %v\n", name, err)
		return 1
	}
	// A hand edited file is the most likely way to end up with JSON the server
	// cannot read, so it is worth saying so now rather than at the next start.
	if err := checkConfigFile(path); err != nil {
		fmt.Fprintf(out, "Warning: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Edited %s\n", path)
	return 0
}

func editorCommand() (string, []string) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			fields := strings.Fields(value)
			return fields[0], fields[1:]
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil
	}
	return "", nil
}

func checkConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	var parsed config.Config
	if err := json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &parsed); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return nil
}

func printConfigTable(cfg *config.Config, out io.Writer) {
	all := settings()
	width := len("file")
	for _, s := range all {
		if len(s.key) > width {
			width = len(s.key)
		}
	}
	fmt.Fprintf(out, "%-*s  %s\n", width, "file", configFilePath(cfg))
	for _, s := range all {
		fmt.Fprintf(out, "%-*s  %s\n", width, s.key, s.display(cfg))
	}
	if shadows := envShadows(); len(shadows) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Overridden by the environment for this session:")
		for _, shadow := range shadows {
			fmt.Fprintf(out, "  %s\n", shadow)
		}
	}
}

// envShadows reports settings whose stored value is currently overridden by an
// environment variable, so a value on screen that the server will not use is
// never a silent surprise.
func envShadows() []string {
	var shadows []string
	for _, s := range settings() {
		if value, ok := lookupShadow(s); ok {
			shadows = append(shadows, fmt.Sprintf("%s from %s=%s", s.key, s.env, value))
		}
	}
	return shadows
}

func lookupShadow(s setting) (string, bool) {
	if s.env == "" {
		return "", false
	}
	value := strings.TrimSpace(os.Getenv(s.env))
	if value == "" {
		return "", false
	}
	return value, true
}

func warnIfShadowed(s setting, out io.Writer) {
	if value, ok := lookupShadow(s); ok {
		fmt.Fprintf(out, "Note: %s=%s overrides this until it is cleared.\n", s.env, value)
	}
}

func reportUnknownKey(key string, out io.Writer) int {
	fmt.Fprintf(out, "Unknown setting: %s\n", key)
	fmt.Fprintf(out, "Known settings: %s\n", strings.Join(settingKeys(), ", "))
	return 2
}

// loadStoredConfig reads the values on disk without applying environment
// overrides, so that saving cannot bake a temporary environment variable into
// the file.
func loadStoredConfig() *config.Config {
	cfg := config.DefaultConfig()
	if dir := strings.TrimSpace(os.Getenv("WEBCODER_CONFIG_DIR")); dir != "" {
		cfg.ConfigDir = dir
	}
	for _, path := range []string{configFilePath(cfg), legacyConfigFilePath(cfg)} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), cfg); err != nil {
			continue
		}
		break
	}
	return cfg
}

func configFilePath(cfg *config.Config) string {
	return filepath.Join(cfg.ConfigDir, configFileName)
}

// legacyConfigFilePath mirrors the older location the loader still reads, so
// that editing settings shows what the server actually uses.
func legacyConfigFilePath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.ConfigDir), ".devspace", configFileName)
}

// writeConfigFile saves the configuration atomically. The desktop configurator
// wrote in place, which leaves unreadable JSON behind if the write is
// interrupted.
func writeConfigFile(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create config folder: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(cfg.ConfigDir, "config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	path := configFilePath(cfg)
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func printConfigUsage(out io.Writer) {
	fmt.Fprint(out, `Usage:
  mcp-webcoder config                    Interactive configuration
  mcp-webcoder config show               Print every setting
  mcp-webcoder config get <key>          Print one setting
  mcp-webcoder config set <key> <value>  Change one setting
  mcp-webcoder config unset <key>        Restore the default
  mcp-webcoder config keys               List the setting names
  mcp-webcoder config path               Print the config file location
  mcp-webcoder config edit               Open the config file in an editor
`)
}
