package main

import (
	"strings"
	"testing"
)

const sampleAuthtoken = "2xPRETENDtokenPRETENDtoken"

func TestTheProviderDefaultsToAuto(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"get", "tunnel.provider"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("get exited with %d: %s", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "auto" {
		t.Errorf("the provider reads as %q, want auto", got)
	}
}

func TestSetRejectsAnUnknownProvider(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.provider", "tailscale"}, strings.NewReader(""), &out); code == 0 {
		t.Fatal("an unknown provider was accepted")
	}
	if !strings.Contains(out.String(), "ngrok") {
		t.Errorf("the refusal does not list the providers: %s", out.String())
	}
}

// The dashboard shows a reserved domain as a link, so a link is what people
// paste.
func TestSetNormalizesADomainPastedAsALink(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.domain", "https://Example.ngrok-free.app/"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); saved.Tunnel.Domain != "example.ngrok-free.app" {
		t.Errorf("stored domain is %q, want example.ngrok-free.app", saved.Tunnel.Domain)
	}
}

func TestSetRejectsADomainThatIsNotAHostName(t *testing.T) {
	useTempConfig(t)

	for _, value := range []string{
		"https://example.ngrok-free.app/mcp",
		"localhost",
		"   ",
	} {
		var out strings.Builder
		if code := runConfig([]string{"set", "tunnel.domain", value}, strings.NewReader(""), &out); code == 0 {
			t.Errorf("%q was accepted as a domain", value)
		}
	}
}

func TestUnsetGoesBackToARandomURL(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.domain", "example.ngrok-free.app"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if code := runConfig([]string{"unset", "tunnel.domain"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("unset exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); saved.Tunnel.Domain != "" {
		t.Errorf("the domain is still stored as %q", saved.Tunnel.Domain)
	}
}

// Terminal output gets pasted into chats and screenshots, so the token is
// reported as present rather than printed.
func TestGetHidesTheAuthtoken(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.authtoken", sampleAuthtoken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), sampleAuthtoken) {
		t.Errorf("set echoed the token back: %s", out.String())
	}

	out.Reset()
	if code := runConfig([]string{"get", "tunnel.authtoken"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("get exited with %d: %s", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "set (hidden)" {
		t.Errorf("get printed %q, want it hidden", got)
	}
}

func TestAnUnsetAuthtokenReadsAsNone(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"get", "tunnel.authtoken"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("get exited with %d: %s", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "(none)" {
		t.Errorf("get printed %q, want (none)", got)
	}
}

func TestShowListsTheAuthtokenWithoutPrintingIt(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.authtoken", sampleAuthtoken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}

	out.Reset()
	if code := runConfig([]string{"show"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("show exited with %d: %s", code, out.String())
	}

	text := out.String()
	for _, want := range []string{"tunnel.provider", "tunnel.domain", "tunnel.authtoken", "set (hidden)"} {
		if !strings.Contains(text, want) {
			t.Errorf("show does not mention %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, sampleAuthtoken) {
		t.Errorf("show printed the token:\n%s", text)
	}
}

func TestUnsetRemovesTheStoredAuthtoken(t *testing.T) {
	dir := useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.authtoken", sampleAuthtoken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}
	if code := runConfig([]string{"unset", "tunnel.authtoken"}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("unset exited with %d: %s", code, out.String())
	}
	if saved := readStoredConfig(t, dir); saved.Tunnel.Authtoken != "" {
		t.Errorf("the token is still stored as %q", saved.Tunnel.Authtoken)
	}
}

// The wizard is the path most people take, so the token must stay hidden there
// too.
func TestTheWizardHidesTheAuthtoken(t *testing.T) {
	useTempConfig(t)
	var out strings.Builder

	if code := runConfig([]string{"set", "tunnel.authtoken", sampleAuthtoken}, strings.NewReader(""), &out); code != 0 {
		t.Fatalf("set exited with %d: %s", code, out.String())
	}

	out.Reset()
	if code := runConfig(nil, strings.NewReader("q\n"), &out); code != 0 {
		t.Fatalf("the wizard exited with %d: %s", code, out.String())
	}

	text := out.String()
	if !strings.Contains(text, "set (hidden)") {
		t.Errorf("the wizard does not hide the token:\n%s", text)
	}
	if strings.Contains(text, sampleAuthtoken) {
		t.Errorf("the wizard printed the token:\n%s", text)
	}
}
