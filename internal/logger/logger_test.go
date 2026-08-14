package logger

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestLevelOfMapsEveryConfiguredName(t *testing.T) {
	want := map[string]zerolog.Level{
		"silent": zerolog.Disabled,
		"error":  zerolog.ErrorLevel,
		"warn":   zerolog.WarnLevel,
		"info":   zerolog.InfoLevel,
		"debug":  zerolog.DebugLevel,
	}
	for name, level := range want {
		if got := levelOf(name); got != level {
			t.Errorf("levelOf(%q) = %v, want %v", name, got, level)
		}
	}
}

func TestAnUnknownLevelFallsBackToInfo(t *testing.T) {
	for _, name := range []string{"", "nonsense", "INFO"} {
		if got := levelOf(name); got != zerolog.InfoLevel {
			t.Errorf("levelOf(%q) = %v, want info", name, got)
		}
	}

	var buf bytes.Buffer
	New("nonsense", "json", &buf).Info().Msg("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("a typo in the level silenced the logger: %q", buf.String())
	}
}

func TestSilentWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	l := New("silent", "json", &buf)
	l.Error().Msg("boom")
	l.Info().Msg("hello")
	if buf.Len() != 0 {
		t.Errorf("silent wrote %q", buf.String())
	}
}

func TestWarnKeepsWarningsAndDropsInfo(t *testing.T) {
	var buf bytes.Buffer
	l := New("warn", "json", &buf)
	l.Info().Msg("chatter")
	l.Warn().Msg("listen")
	out := buf.String()
	if strings.Contains(out, "chatter") {
		t.Errorf("info survived a warn logger: %q", out)
	}
	if !strings.Contains(out, "listen") {
		t.Errorf("warn was dropped: %q", out)
	}
}

func TestDebugReachesTheWriter(t *testing.T) {
	var buf bytes.Buffer
	New("debug", "json", &buf).Debug().Msg("details")
	if !strings.Contains(buf.String(), "details") {
		t.Errorf("debug was dropped: %q", buf.String())
	}
}

func TestTextFormatIsNotJSON(t *testing.T) {
	var buf bytes.Buffer
	New("info", "text", &buf).Info().Msg("hello")
	out := buf.String()
	if !strings.Contains(out, "hello") {
		t.Errorf("text output lost the message: %q", out)
	}
	if strings.Contains(out, "\"level\"") {
		t.Errorf("text output still looks like JSON: %q", out)
	}
}

func TestANilWriterIsAccepted(t *testing.T) {
	if New("info", "json", nil) == nil {
		t.Fatal("New returned nil for a nil writer")
	}
}
