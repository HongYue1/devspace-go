// Package tunnel finds, and when asked fetches, the agents that publish the
// local server on the internet.
package tunnel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ngrokDownloadTemplate is the endpoint ngrok documents for the v3 agent. The
// channel id is part of the published URL, not a secret.
const ngrokDownloadTemplate = "https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable-%s-%s.%s"

const (
	// downloadTimeout bounds the whole fetch. The agent is one binary of a few
	// tens of megabytes, so a slow minute is fine and a hung hour is not.
	downloadTimeout = 3 * time.Minute
	// maxArchiveBytes caps what a download may expand to, so a wrong URL or a
	// hostile mirror cannot fill the disk.
	maxArchiveBytes = 200 << 20
)

// AgentName returns the ngrok agent's file name on this platform.
func AgentName() string {
	return agentNameFor(runtime.GOOS)
}

func agentNameFor(goos string) string {
	if goos == "windows" {
		return "ngrok.exe"
	}
	return "ngrok"
}

// NgrokDownloadURL returns where to fetch the agent for a platform, or an error
// for a platform ngrok does not publish.
func NgrokDownloadURL(goos, goarch string) (string, error) {
	switch goos {
	case "windows", "linux", "darwin", "freebsd":
	default:
		return "", fmt.Errorf("ngrok publishes no agent for %s", goos)
	}
	switch goarch {
	case "amd64", "arm64", "386", "arm":
	default:
		return "", fmt.Errorf("ngrok publishes no %s agent for %s", goarch, goos)
	}

	archiveExt := "tgz"
	if goos == "windows" {
		archiveExt = "zip"
	}
	return fmt.Sprintf(ngrokDownloadTemplate, goos, goarch, archiveExt), nil
}

// ToolsDir is where the server keeps downloaded helpers: beside the executable,
// so a portable install stays portable.
func ToolsDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "tools")
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "tools")
	}
	return "tools"
}

// FindNgrok returns an agent that is already on this machine, preferring one
// shipped beside the server over whatever is on PATH.
func FindNgrok() string {
	name := AgentName()
	for _, dir := range searchDirs() {
		if candidate := filepath.Join(dir, name); isFile(candidate) {
			return candidate
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	return ""
}

func searchDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		dirs = append(dirs, filepath.Join(exeDir, "tools"), exeDir)
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "tools"), wd)
	}

	seen := make(map[string]bool, len(dirs))
	unique := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		unique = append(unique, clean)
	}
	return unique
}

// Fetcher retrieves an archive. It is injected so tests never reach the network.
type Fetcher func(url string) (io.ReadCloser, error)

// EnsureNgrok returns a usable agent path, downloading into dir only when this
// machine has no agent yet. The second result reports whether it downloaded one,
// so a caller can say so instead of stalling silently on a slow link.
func EnsureNgrok(dir string, fetch Fetcher) (string, bool, error) {
	if candidate := filepath.Join(dir, AgentName()); isFile(candidate) {
		return candidate, false, nil
	}
	if existing := FindNgrok(); existing != "" {
		return existing, false, nil
	}

	downloadURL, err := NgrokDownloadURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", false, err
	}
	if fetch == nil {
		fetch = httpFetch
	}
	body, err := fetch(downloadURL)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", downloadURL, err)
	}
	defer body.Close()

	target := filepath.Join(dir, AgentName())
	if err := installAgent(body, target); err != nil {
		return "", false, err
	}
	return target, true, nil
}

// installAgent buffers the download, then extracts just the agent from it.
func installAgent(body io.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	archive, err := os.CreateTemp(filepath.Dir(target), "ngrok-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()

	size, err := io.Copy(archive, io.LimitReader(body, maxArchiveBytes))
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if size == 0 {
		return errors.New("the download was empty")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractAgent(archive, size, target)
}

// extractAgent picks the format from the bytes rather than the URL, so a mirror
// that renames files still works.
func extractAgent(archive *os.File, size int64, target string) error {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(archive, magic); err != nil {
		return errors.New("the download was too short to be an archive")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}

	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")):
		return extractZip(archive, size, target)
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		return extractTarGz(archive, target)
	default:
		return errors.New("the download was not a zip or gzip archive")
	}
}

func extractZip(archive io.ReaderAt, size int64, target string) error {
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return fmt.Errorf("read zip: %w", err)
	}
	for _, entry := range reader.File {
		if !isAgentEntry(entry.Name) {
			continue
		}
		content, err := entry.Open()
		if err != nil {
			return fmt.Errorf("read zip entry: %w", err)
		}
		defer content.Close()
		return writeExecutable(content, target)
	}
	return errors.New("the archive contained no ngrok agent")
}

func extractTarGz(archive io.Reader, target string) error {
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("read gzip: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the archive contained no ngrok agent")
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !isAgentEntry(header.Name) {
			continue
		}
		return writeExecutable(tarReader, target)
	}
}

// isAgentEntry matches on the base name only. An entry's own path is never used
// to build a destination, so a crafted archive cannot escape the target folder.
func isAgentEntry(name string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(name)))
	return base == "ngrok" || base == "ngrok.exe"
}

// writeExecutable lands the agent in one atomic step, so an interrupted install
// cannot leave a half-written binary that looks usable.
func writeExecutable(content io.Reader, target string) error {
	temp, err := os.CreateTemp(filepath.Dir(target), "ngrok-agent-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()

	written, err := io.Copy(temp, io.LimitReader(content, maxArchiveBytes))
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tempName)
		return fmt.Errorf("write agent: %w", err)
	}
	if written == 0 {
		os.Remove(tempName)
		return errors.New("the agent in the archive was empty")
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, target); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("install agent: %w", err)
	}
	return nil
}

func httpFetch(downloadURL string) (io.ReadCloser, error) {
	client := &http.Client{Timeout: downloadTimeout}
	response, err := client.Get(downloadURL)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("the server answered %s", response.Status)
	}
	return response.Body, nil
}

func isFile(candidate string) bool {
	info, err := os.Stat(candidate)
	return err == nil && !info.IsDir()
}

// NgrokArgs builds the agent command line for one local address. A configured
// domain is what makes the URL stable; without one the agent issues a random
// hostname that changes on every restart.
func NgrokArgs(host string, port int, domain string) []string {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	args := []string{
		"http",
		fmt.Sprintf("%s:%d", host, port),
		"--log=stdout",
		"--log-format=logfmt",
	}
	if domain != "" {
		args = append(args, "--url=https://"+domain)
	}
	return args
}

// ngrokURLPattern matches the hostnames ngrok hands out for free.
var ngrokURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9][a-zA-Z0-9.-]*\.(?:ngrok-free\.app|ngrok\.app|ngrok\.dev|ngrok\.io)`)

// URLFromNgrokLine picks the public URL out of one line of agent output.
//
// The known hostnames are tried first because they are unambiguous. The logfmt
// url= field is the fallback, which is how a custom domain is found.
func URLFromNgrokLine(line string) string {
	if match := ngrokURLPattern.FindString(line); match != "" {
		return match
	}

	index := strings.Index(line, "url=https://")
	if index < 0 {
		return ""
	}
	value := line[index+len("url="):]
	if end := strings.IndexAny(value, " \t\"'"); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSuffix(value, "/")
}

// ngrokErrorCodePattern matches the codes the agent prints when it gives up.
var ngrokErrorCodePattern = regexp.MustCompile(`ERR_NGROK_[0-9]+`)

// NgrokFatalLine reports the error code when a line says the agent has given up.
//
// Without this a bad authtoken costs the caller the whole URL timeout before
// anything is explained, which reads like a hang rather than a refusal.
func NgrokFatalLine(line string) string {
	return ngrokErrorCodePattern.FindString(line)
}
