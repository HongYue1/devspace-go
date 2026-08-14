package config

import "testing"

// TestDefaultLoggingIsReadableAndQuiet pins the console defaults. JSON at info
// with per-request logging on meant a plain "serve" printed a wall of machine
// readable lines and buried the tunnel URL.
func TestDefaultLoggingIsReadableAndQuiet(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Logging.Format != LogText {
		t.Fatalf("default log format is %q, want %q", cfg.Logging.Format, LogText)
	}
	if cfg.Logging.Requests {
		t.Fatal("per-request logging is on by default")
	}
	if !cfg.Logging.ToolCalls {
		t.Fatal("tool call logging is off by default, so nothing useful is reported")
	}
}

// TestLogRequestsEnvTogglesBothWays covers the env var that could previously
// only turn request logging off, which left no way to turn it back on once the
// default changed.
func TestLogRequestsEnvTogglesBothWays(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"0", false},
	} {
		t.Setenv("WEBCODER_CONFIG_DIR", t.TempDir())
		t.Setenv("WEBCODER_LOG_REQUESTS", c.value)

		if got := LoadConfig().Logging.Requests; got != c.want {
			t.Fatalf("WEBCODER_LOG_REQUESTS=%q gave requests=%v, want %v", c.value, got, c.want)
		}
	}
}

func TestLogRequestsEnvUnsetKeepsTheDefault(t *testing.T) {
	t.Setenv("WEBCODER_CONFIG_DIR", t.TempDir())
	t.Setenv("WEBCODER_LOG_REQUESTS", "")

	if LoadConfig().Logging.Requests {
		t.Fatal("an empty env value turned request logging on")
	}
}
