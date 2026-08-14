package tunnel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNgrokDownloadURLCoversThePlatformsNgrokPublishes(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"windows", "amd64", "ngrok-v3-stable-windows-amd64.zip"},
		{"linux", "amd64", "ngrok-v3-stable-linux-amd64.tgz"},
		{"linux", "arm64", "ngrok-v3-stable-linux-arm64.tgz"},
		{"darwin", "arm64", "ngrok-v3-stable-darwin-arm64.tgz"},
	}

	for _, test := range tests {
		got, err := NgrokDownloadURL(test.goos, test.goarch)
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

func TestNgrokDownloadURLRefusesPlatformsWithNoAgent(t *testing.T) {
	if url, err := NgrokDownloadURL("plan9", "amd64"); err == nil {
		t.Errorf("plan9 was accepted and given %q", url)
	}
	if url, err := NgrokDownloadURL("linux", "riscv64"); err == nil {
		t.Errorf("riscv64 was accepted and given %q", url)
	}
}

func TestAgentNameFollowsThePlatform(t *testing.T) {
	if got := agentNameFor("windows"); got != "ngrok.exe" {
		t.Errorf("the windows agent is %q", got)
	}
	if got := agentNameFor("linux"); got != "ngrok" {
		t.Errorf("the linux agent is %q", got)
	}
}

func TestEnsureNgrokKeepsAnAgentThatIsAlreadyThere(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, AgentName())
	if err := os.WriteFile(existing, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, downloaded, err := EnsureNgrok(dir, refusingFetcher(t))
	if err != nil {
		t.Fatalf("EnsureNgrok: %v", err)
	}
	if downloaded {
		t.Error("reported a download for an agent that was already installed")
	}
	if path != existing {
		t.Errorf("returned %q, want %q", path, existing)
	}
}

func TestEnsureNgrokInstallsFromAZip(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	path, downloaded, err := EnsureNgrok(dir, fetcherFor(zipArchive(t, map[string]string{
		"ngrok.exe": "windows agent",
	})))
	if err != nil {
		t.Fatalf("EnsureNgrok: %v", err)
	}
	if !downloaded {
		t.Error("did not report that it downloaded the agent")
	}
	assertAgent(t, dir, path, "windows agent")
}

func TestEnsureNgrokInstallsFromATarGzWithANestedPath(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	path, _, err := EnsureNgrok(dir, fetcherFor(tarGzArchive(t, map[string]string{
		"ngrok-v3-stable-linux-amd64/ngrok": "posix agent",
	})))
	if err != nil {
		t.Fatalf("EnsureNgrok: %v", err)
	}
	assertAgent(t, dir, path, "posix agent")
}

// The archive is untrusted input, so an entry that tries to climb out of the
// target directory must land inside it anyway.
func TestEnsureNgrokIgnoresThePathsInsideTheArchive(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	path, _, err := EnsureNgrok(dir, fetcherFor(tarGzArchive(t, map[string]string{
		"../../../ngrok": "escape attempt",
	})))
	if err != nil {
		t.Fatalf("EnsureNgrok: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("installed at %q, which is outside %q", path, dir)
	}
	assertAgent(t, dir, path, "escape attempt")
}

func TestEnsureNgrokRejectsAnArchiveWithoutAnAgent(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureNgrok(dir, fetcherFor(zipArchive(t, map[string]string{
		"README.md": "no agent in here",
	})))
	if err == nil {
		t.Fatal("an archive with no agent was accepted")
	}
	if !strings.Contains(err.Error(), "no ngrok agent") {
		t.Errorf("error was %q, want it to say the agent was missing", err)
	}
	if got := names(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v, want nothing", got)
	}
}

// A wrong URL usually answers with an HTML error page, which must not be
// installed as if it were the agent.
func TestEnsureNgrokRejectsSomethingThatIsNotAnArchive(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureNgrok(dir, fetcherFor([]byte("<html>404 not found</html>")))
	if err == nil {
		t.Fatal("an HTML page was accepted as an archive")
	}
	if got := names(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v, want nothing", got)
	}
}

func TestEnsureNgrokRejectsAnEmptyDownload(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureNgrok(dir, fetcherFor(nil))
	if err == nil {
		t.Fatal("an empty download was accepted")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error was %q, want it to say the download was empty", err)
	}
}

func TestEnsureNgrokReportsWhyADownloadFailed(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	_, _, err := EnsureNgrok(dir, func(string) (io.ReadCloser, error) {
		return nil, errors.New("no route to host")
	})
	if err == nil {
		t.Fatal("a failed download was reported as success")
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Errorf("error was %q, want the underlying reason", err)
	}
	if !strings.Contains(err.Error(), "bin.equinox.io") {
		t.Errorf("error was %q, want the URL it tried", err)
	}
}

func TestEnsureNgrokLeavesNoTemporaryFilesBehind(t *testing.T) {
	isolatePath(t)
	dir := t.TempDir()

	if _, _, err := EnsureNgrok(dir, fetcherFor(tarGzArchive(t, map[string]string{
		"ngrok": "agent",
	}))); err != nil {
		t.Fatalf("EnsureNgrok: %v", err)
	}

	got := names(t, dir)
	if len(got) != 1 || got[0] != AgentName() {
		t.Errorf("directory holds %v, want just %s", got, AgentName())
	}
}

func TestNgrokArgsAskForTheConfiguredDomain(t *testing.T) {
	args := strings.Join(NgrokArgs("127.0.0.1", 7676, "example.ngrok-free.app"), " ")

	for _, want := range []string{
		"http",
		"127.0.0.1:7676",
		"--url=https://example.ngrok-free.app",
		"--log=stdout",
		"--log-format=logfmt",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}

func TestNgrokArgsWithoutADomainAskForNoURL(t *testing.T) {
	args := strings.Join(NgrokArgs("127.0.0.1", 7676, ""), " ")
	if strings.Contains(args, "--url") {
		t.Errorf("args %q ask for a URL when none is configured", args)
	}
}

// The agent has to connect to something it can reach; a wildcard bind address is
// not it.
func TestNgrokArgsForwardToLoopbackWhenTheServerListensEverywhere(t *testing.T) {
	args := strings.Join(NgrokArgs("0.0.0.0", 8080, ""), " ")
	if !strings.Contains(args, "127.0.0.1:8080") {
		t.Errorf("args %q, want a loopback address", args)
	}
}

func TestURLFromNgrokLineFindsThePublicURL(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "started tunnel",
			line: `t=2026-08-14T11:00:00+0000 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://127.0.0.1:7676 url=https://calm-owl-42.ngrok-free.app`,
			want: "https://calm-owl-42.ngrok-free.app",
		},
		{
			name: "quoted value",
			line: `lvl=info msg="started tunnel" url="https://calm-owl-42.ngrok.app"`,
			want: "https://calm-owl-42.ngrok.app",
		},
		{
			name: "custom domain",
			line: `lvl=info msg="started tunnel" url=https://mcp.example.com`,
			want: "https://mcp.example.com",
		},
		{
			name: "ordinary traffic line",
			line: `lvl=info msg="join connections" obj=join id=7f3a l=127.0.0.1:7676`,
			want: "",
		},
		{
			name: "authentication failure",
			line: `lvl=eror msg="failed to auth" err="authentication failed"`,
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := URLFromNgrokLine(test.line); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestNgrokFatalLineSpotsAnAgentGivingUp(t *testing.T) {
	fatal := `t=2026-08-14T11:00:00+0000 lvl=eror msg="failed to reconnect session" obj=csess err="authentication failed: the authtoken is not valid. ERR_NGROK_105"`
	if got := NgrokFatalLine(fatal); got != "ERR_NGROK_105" {
		t.Errorf("got %q, want ERR_NGROK_105", got)
	}

	ordinary := `t=2026-08-14T11:00:00+0000 lvl=info msg="started tunnel" name=command_line`
	if got := NgrokFatalLine(ordinary); got != "" {
		t.Errorf("an ordinary line was read as fatal: %q", got)
	}
}

// isolatePath keeps the search for an existing agent from finding a real ngrok
// installed on whichever machine runs the tests.
func isolatePath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// refusingFetcher fails the test if the code tries to download anything.
func refusingFetcher(t *testing.T) Fetcher {
	t.Helper()
	return func(string) (io.ReadCloser, error) {
		t.Error("a download was attempted when an agent was already present")
		return nil, errors.New("must not download")
	}
}

func fetcherFor(data []byte) Fetcher {
	return func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

func zipArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func tarGzArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range entries {
		header := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func names(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	return got
}

func assertAgent(t *testing.T, dir, path, want string) {
	t.Helper()

	expected := filepath.Join(dir, AgentName())
	if path != expected {
		t.Errorf("installed at %q, want %q", path, expected)
	}
	content, err := os.ReadFile(expected)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Errorf("the agent holds %q, want %q", content, want)
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("directory holds %v, want just the agent", got)
	}
}
