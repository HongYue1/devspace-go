package main

import (
	"strings"
	"testing"
)

// sampleBearerToken is long enough to satisfy the setting's own length check.
const sampleBearerToken = "0123456789abcdef0123456789abcdef"

func TestTokenSaysWhenNothingIsRequired(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"token"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token exited with %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "No token is set") {
		t.Errorf("token did not report an open server:\n%s", out.String())
	}
}

func TestTokenPrintsTheStoredToken(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "authToken", sampleBearerToken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}

	out.Reset()
	if code := runConfig([]string{"token"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token exited with %d: %s", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != sampleBearerToken {
		t.Errorf("token printed %q, want the stored token", got)
	}
}

// The generated value is the only copy there will ever be, so what is printed
// and what is stored have to be the same string.
func TestTokenNewPrintsWhatItSaved(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"token", "new"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token new exited with %d: %s", code, out.String())
	}

	printed := firstLine(out.String())
	if len(printed) != 2*generatedTokenBytes {
		t.Fatalf("printed %q, want %d hex characters", printed, 2*generatedTokenBytes)
	}
	if saved := readStoredConfig(t, dir); saved.AuthToken != printed {
		t.Errorf("saved %q but printed %q", saved.AuthToken, printed)
	}
}

func TestTokenNewReplacesTheOldOne(t *testing.T) {
	useTempConfig(t)

	first := generateToken(t)
	second := generateToken(t)
	if first == second {
		t.Errorf("both runs produced %q", first)
	}
}

// Rotating cuts off clients holding the old token, which is worth saying out
// loud rather than leaving to be discovered as a connection failure.
func TestTokenNewWarnsThatTheOldTokenIsDead(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "authToken", sampleBearerToken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}

	out.Reset()
	if code := runConfig([]string{"token", "new"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token new exited with %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "no longer works") {
		t.Errorf("token new did not warn about the replaced token:\n%s", out.String())
	}
}

func TestTokenRefusesAnUnknownWord(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"token", "frobnicate"}, strings.NewReader(""), &out); code != 2 {
		t.Fatalf("token frobnicate exited with %d, want 2: %s", code, out.String())
	}
}

// A printed token the running server will not accept is worse than none, so an
// environment override is reported next to it.
func TestTokenReportsAnEnvironmentOverride(t *testing.T) {
	useTempConfig(t)
	t.Setenv("WEBCODER_AUTH_TOKEN", "an-overriding-token-value")
	var out strings.Builder

	if code := runConfig([]string{"token", "new"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token new exited with %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "WEBCODER_AUTH_TOKEN") {
		t.Errorf("token new did not mention the override:\n%s", out.String())
	}
}

func generateToken(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	if code := runConfig([]string{"token", "new"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("token new exited with %d: %s", code, out.String())
	}
	return firstLine(out.String())
}

func firstLine(text string) string {
	return strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
}
