package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snakex21/devspace-go/internal/config"
)

// useTempConfig points the configurator at a throwaway folder so a test never
// reads or writes the real configuration.
func useTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBCODER_CONFIG_DIR", dir)
	return dir
}

func readStoredConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("reading saved config: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	return cfg
}

func TestSetThenGetRoundTripsThroughTheFile(t *testing.T) {
	dir := useTempConfig(t)

	var out strings.Builder
	if code := runConfig([]string{"set", "port", "7777"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); saved.Port != 7777 {
		t.Fatalf("saved port is %d, want 7777", saved.Port)
	}

	out.Reset()
	if code := runConfig([]string{"get", "port"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("get exited with %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "7777" {
		t.Fatalf("get printed %q, want 7777", got)
	}
}

// Saving one setting must not quietly rewrite the others, which is what happens
// when a configurator starts from the defaults instead of the stored file.
func TestSetKeepsTheOtherStoredValues(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "host", "0.0.0.0"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set host exited with %d: %s", code, out.String())
	}
	if code := runConfig([]string{"set", "port", "9100"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set port exited with %d: %s", code, out.String())
	}

	saved := readStoredConfig(t, dir)
	if saved.Host != "0.0.0.0" || saved.Port != 9100 {
		t.Fatalf("saved host/port are %q/%d, want 0.0.0.0/9100", saved.Host, saved.Port)
	}
}

// An environment variable outranks the file at run time, so it must not be
// baked into the file by a later save.
func TestSetDoesNotPersistEnvironmentOverrides(t *testing.T) {
	dir := useTempConfig(t)
	t.Setenv("HOST", "10.0.0.9")
	var out strings.Builder

	if code := runConfig([]string{"set", "port", "7000"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); saved.Host == "10.0.0.9" {
		t.Fatal("the HOST environment variable was written into the config file")
	}
}

func TestSetReportsAnEnvironmentOverride(t *testing.T) {
	useTempConfig(t)
	t.Setenv("PORT", "9999")
	var out strings.Builder

	if code := runConfig([]string{"set", "port", "7000"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "PORT=9999") {
		t.Fatalf("expected a note about the PORT override, got:\n%s", out.String())
	}
}

func TestSetRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		exit int
	}{
		{"port is not a number", []string{"set", "port", "http"}, "want a number", 1},
		{"port out of range", []string{"set", "port", "70000"}, "between 1 and 65535", 1},
		{"url without a scheme", []string{"set", "publicUrl", "example.com"}, "http://", 1},
		{"not a choice", []string{"set", "toolMode", "medium"}, "want one of", 1},
		{"not a boolean", []string{"set", "log.requests", "sometimes"}, "want true or false", 1},
		{"missing folder", []string{"set", "roots", filepath.Join("nope", "nowhere")}, "nowhere", 1},
		{"unknown key", []string{"set", "colour", "blue"}, "Unknown setting", 2},
		{"no value", []string{"set", "port"}, "Usage", 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := useTempConfig(t)
			var out strings.Builder
			if code := runConfig(c.args, strings.NewReader(""), &out); code != c.exit {
				t.Fatalf("exit %d, want %d: %s", code, c.exit, out.String())
			}
			if !strings.Contains(out.String(), c.want) {
				t.Fatalf("message %q does not mention %q", out.String(), c.want)
			}
			if _, err := os.Stat(filepath.Join(dir, configFileName)); err == nil {
				t.Fatal("a refused value still wrote the config file")
			}
		})
	}
}

func TestRootsAreStoredAsAbsoluteForwardSlashPaths(t *testing.T) {
	useTempConfig(t)
	folder := t.TempDir()
	var out strings.Builder

	if code := runConfig([]string{"set", "roots", folder}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set roots exited with %d: %s", code, out.String())
	}
	roots, err := parseRoots(folder)
	if err != nil {
		t.Fatalf("parseRoots: %v", err)
	}
	if len(roots) != 1 || roots[0] != filepath.ToSlash(folder) {
		t.Fatalf("roots are %v, want [%s]", roots, filepath.ToSlash(folder))
	}
	if strings.Contains(roots[0], "\\") {
		t.Fatalf("root %q still contains a backslash", roots[0])
	}
}

// A value with spaces is the normal case on Windows, where paths are rarely
// quoted at a prompt.
func TestSetJoinsAValueThatWasNotQuoted(t *testing.T) {
	useTempConfig(t)
	parent := t.TempDir()
	folder := filepath.Join(parent, "my code")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatalf("creating the folder: %v", err)
	}
	var out strings.Builder

	args := append([]string{"set", "roots"}, strings.Fields(folder)...)
	if code := runConfig(args, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "my code") {
		t.Fatalf("expected the folder in the output, got:\n%s", out.String())
	}
}

func TestUnsetRestoresTheDefault(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "port", "7777"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if code := runConfig([]string{"unset", "port"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("unset exited with %d: %s", code, out.String())
	}
	if saved, want := readStoredConfig(t, dir), config.DefaultConfig().Port; saved.Port != want {
		t.Fatalf("port is %d after unset, want %d", saved.Port, want)
	}
}

func TestUnsetClearsTheRoots(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "roots", t.TempDir()}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if code := runConfig([]string{"unset", "roots"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("unset exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); len(saved.AllowedRoots) != 0 {
		t.Fatalf("roots are %v after unset, want none", saved.AllowedRoots)
	}
}

func TestShowListsEverySettingAndTheFile(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"show"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("show exited with %d", code)
	}
	printed := out.String()
	if !strings.Contains(printed, dir) {
		t.Fatalf("show did not name the config folder %s:\n%s", dir, printed)
	}
	for _, key := range settingKeys() {
		if !strings.Contains(printed, key) {
			t.Fatalf("show did not list %q", key)
		}
	}
}

func TestUnknownSubcommandExplainsItself(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"frobnicate"}, strings.NewReader(""), &out); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected usage text, got:\n%s", out.String())
	}
}

// Every listed choice must be accepted, so the prompt can never offer a value
// that its own parser refuses. The shell list is skipped because it depends on
// what is installed on the machine running the test.
func TestEveryOfferedChoiceIsAccepted(t *testing.T) {
	for _, s := range settings() {
		if s.choices == nil || s.key == "shell" {
			continue
		}
		cfg := config.DefaultConfig()
		for _, choice := range s.choices(cfg) {
			if err := s.parse(cfg, choice); err != nil {
				t.Errorf("%s offers %q but refuses it: %v", s.key, choice, err)
				continue
			}
			if got := s.show(cfg); !strings.EqualFold(got, choice) {
				t.Errorf("%s = %q after setting %q", s.key, got, choice)
			}
		}
	}
}

func TestParseBoolAcceptsWhatPeopleType(t *testing.T) {
	for _, yes := range []string{"y", "Yes", "1", "on", "TRUE", " t "} {
		if value, err := parseBool(yes); err != nil || !value {
			t.Errorf("parseBool(%q) = %v, %v", yes, value, err)
		}
	}
	for _, no := range []string{"n", "No", "0", "off", "FALSE", " f "} {
		if value, err := parseBool(no); err != nil || value {
			t.Errorf("parseBool(%q) = %v, %v", no, value, err)
		}
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Fatal("parseBool accepted maybe")
	}
}

// The interactive prompt has to survive a typo, keep going, and save what was
// accepted.
func TestWizardRetriesAfterABadAnswerAndSaves(t *testing.T) {
	dir := useTempConfig(t)
	folder := t.TempDir()
	input := strings.Join([]string{
		folder,     // the roots question a first run opens with
		"99",       // not on the menu
		"3",        // port
		"notaport", // refused, ask again
		"7777",
		"s",
	}, "\n") + "\n"

	var out strings.Builder
	if code := runConfig(nil, strings.NewReader(input), &out); code != 0 {
		t.Fatalf("the prompt exited with %d:\n%s", code, out.String())
	}

	printed := out.String()
	for _, want := range []string{"Not one of the choices", "want a number", "Saved"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("expected %q in:\n%s", want, printed)
		}
	}
	saved := readStoredConfig(t, dir)
	if saved.Port != 7777 {
		t.Fatalf("saved port is %d, want 7777", saved.Port)
	}
	if len(saved.AllowedRoots) != 1 || saved.AllowedRoots[0] != filepath.ToSlash(folder) {
		t.Fatalf("saved roots are %v, want [%s]", saved.AllowedRoots, filepath.ToSlash(folder))
	}
}

func TestWizardQuitsWithoutWriting(t *testing.T) {
	dir := useTempConfig(t)
	input := strings.Join([]string{t.TempDir(), "q"}, "\n") + "\n"

	var out strings.Builder
	if code := runConfig(nil, strings.NewReader(input), &out); code != 0 {
		t.Fatalf("the prompt exited with %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); err == nil {
		t.Fatal("quitting still wrote the config file")
	}
}

// Run from a script or a pipe there is no input at all, and the prompt must
// end rather than spin.
func TestWizardStopsWhenThereIsNoInput(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig(nil, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("the prompt exited with %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "No more input") {
		t.Fatalf("expected a note about missing input, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, configFileName)); err == nil {
		t.Fatal("an empty run wrote the config file")
	}
}

// A half written config file is worse than none, so the save is atomic and
// leaves nothing behind.
func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "port", "7001"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the config folder: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != configFileName {
			t.Fatalf("unexpected leftover file %q", entry.Name())
		}
	}
}

func TestStoredConfigReadsTheLegacyLocation(t *testing.T) {
	parent := t.TempDir()
	current := filepath.Join(parent, ".webcoder")
	legacy := filepath.Join(parent, ".devspace")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("creating the legacy folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, configFileName), []byte(`{"port":8123}`), 0o600); err != nil {
		t.Fatalf("writing the legacy file: %v", err)
	}
	t.Setenv("WEBCODER_CONFIG_DIR", current)

	if cfg := loadStoredConfig(); cfg.Port != 8123 {
		t.Fatalf("port is %d, want the legacy 8123", cfg.Port)
	}
}

func TestStoredConfigIgnoresAByteOrderMark(t *testing.T) {
	dir := useTempConfig(t)
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"port":8222}`)...)
	if err := os.WriteFile(filepath.Join(dir, configFileName), data, 0o600); err != nil {
		t.Fatalf("writing the config file: %v", err)
	}

	if cfg := loadStoredConfig(); cfg.Port != 8222 {
		t.Fatalf("port is %d, want 8222", cfg.Port)
	}
}
