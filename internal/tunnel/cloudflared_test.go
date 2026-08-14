package tunnel

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bare executable is what Windows and Linux downloads are, so the magic
// number stands in for a real connector here.
var fakeConnector = []byte("MZ\x90\x00 pretend connector")

func TestCloudflaredDownloadURLCoversThePlatformsCloudflarePublishes(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"windows", "amd64", "cloudflared-windows-amd64.exe"},
		{"windows", "386", "cloudflared-windows-386.exe"},
		{"linux", "amd64", "cloudflared-linux-amd64"},
		{"linux", "arm64", "cloudflared-linux-arm64"},
		{"darwin", "arm64", "cloudflared-darwin-arm64.tgz"},
	}

	for _, test := range tests {
		got, err := CloudflaredDownloadURL(test.goos, test.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", test.goos, test.goarch, err)
		}
		if !strings.HasPrefix(got, "https://") {
			t.Errorf("%s/%s gave %q, want an https URL", test.goos, test.goarch, got)
		}
		if !strings.HasSuffix(got, test.want) {
			t.Errorf("%s/%s gave %q, want it to end with %q", test.goos, test.goarch, got, test.want)
		}
	}
}

func TestCloudflaredDownloadURLRefusesPlatformsWithNoConnector(t *testing.T) {
	if url, err := CloudflaredDownloadURL("plan9", "amd64"); err == nil {
		t.Errorf("plan9 was accepted and given %q", url)
	}
	if url, err := CloudflaredDownloadURL("linux", "riscv64"); err == nil {
		t.Errorf("riscv64 was accepted and given %q", url)
	}
	if url, err := CloudflaredDownloadURL("darwin", "386"); err == nil {
		t.Errorf("darwin/386 was accepted and given %q", url)
	}
}

func TestCloudflaredNameFollowsThePlatform(t *testing.T) {
	if got := cloudflaredNameFor("windows"); got != "cloudflared.exe" {
		t.Errorf("the windows connector is %q", got)
	}
	if got := cloudflaredNameFor("linux"); got != "cloudflared" {
		t.Errorf("the linux connector is %q", got)
	}
}

func TestEnsureCloudflaredKeepsAConnectorThatIsAlreadyThere(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, CloudflaredName())
	if err := os.WriteFile(existing, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, downloaded, err := EnsureCloudflared(dir, refusingFetcher(t))
	if err != nil {
		t.Fatalf("EnsureCloudflared: %v", err)
	}
	if downloaded {
		t.Error("reported a download for a connector that was already installed")
	}
	if path != existing {
		t.Errorf("returned %q, want %q", path, existing)
	}
}

// Windows and Linux publish the connector as a plain binary, with no archive to
// unpack.
func TestEnsureCloudflaredInstallsABareExecutable(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	path, downloaded, err := EnsureCloudflared(dir, fetcherFor(fakeConnector))
	if err != nil {
		t.Fatalf("EnsureCloudflared: %v", err)
	}
	if !downloaded {
		t.Error("did not report that it downloaded the connector")
	}
	assertConnector(t, dir, path, string(fakeConnector))
}

func TestEnsureCloudflaredInstallsFromATarGz(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	path, _, err := EnsureCloudflared(dir, fetcherFor(tarGzArchive(t, map[string]string{
		"cloudflared-darwin-arm64/cloudflared": "mac connector",
	})))
	if err != nil {
		t.Fatalf("EnsureCloudflared: %v", err)
	}
	assertConnector(t, dir, path, "mac connector")
}

func TestEnsureCloudflaredRejectsAnArchiveWithoutAConnector(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureCloudflared(dir, fetcherFor(tarGzArchive(t, map[string]string{
		"README.md": "no connector in here",
	})))
	if err == nil {
		t.Fatal("an archive with no connector was accepted")
	}
	if !strings.Contains(err.Error(), "no cloudflared connector") {
		t.Errorf("error was %q, want it to say the connector was missing", err)
	}
	if got := names(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v, want nothing", got)
	}
}

// A wrong URL answers with an HTML error page, which must not be installed and
// then run as if it were the connector.
func TestEnsureCloudflaredRejectsSomethingThatIsNotAProgram(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureCloudflared(dir, fetcherFor([]byte("<html>404 not found</html>")))
	if err == nil {
		t.Fatal("an HTML page was accepted as the connector")
	}
	if got := names(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v, want nothing", got)
	}
}

func TestEnsureCloudflaredRejectsAnEmptyDownload(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureCloudflared(dir, fetcherFor(nil))
	if err == nil {
		t.Fatal("an empty download was accepted")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error was %q, want it to say the download was empty", err)
	}
}

func TestEnsureCloudflaredReportsWhyADownloadFailed(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureCloudflared(dir, func(string) (io.ReadCloser, error) {
		return nil, errors.New("no route to host")
	})
	if err == nil {
		t.Fatal("a failed download was reported as success")
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Errorf("error was %q, want the underlying reason", err)
	}
	if !strings.Contains(err.Error(), "github.com/cloudflare/cloudflared") {
		t.Errorf("error was %q, want the URL it tried", err)
	}
}

func TestEnsureCloudflaredLeavesNoTemporaryFilesBehind(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	if _, _, err := EnsureCloudflared(dir, fetcherFor(fakeConnector)); err != nil {
		t.Fatalf("EnsureCloudflared: %v", err)
	}

	got := names(t, dir)
	if len(got) != 1 || got[0] != CloudflaredName() {
		t.Errorf("directory holds %v, want just %s", got, CloudflaredName())
	}
}

func assertConnector(t *testing.T, dir, path, want string) {
	t.Helper()

	expected := filepath.Join(dir, CloudflaredName())
	if path != expected {
		t.Errorf("installed at %q, want %q", path, expected)
	}
	content, err := os.ReadFile(expected)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Errorf("the connector holds %q, want %q", content, want)
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("directory holds %v, want just the connector", got)
	}
}
