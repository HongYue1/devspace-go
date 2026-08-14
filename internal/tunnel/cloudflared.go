package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// cloudflaredDownloadTemplate is the release asset Cloudflare publishes per
// platform. The "latest" path is a redirect they maintain, so a fresh install
// gets a connector the edge still accepts.
const cloudflaredDownloadTemplate = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-%s-%s%s"

// CloudflaredName returns the connector's file name on this platform.
func CloudflaredName() string {
	return cloudflaredNameFor(runtime.GOOS)
}

func cloudflaredNameFor(goos string) string {
	if goos == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// cloudflaredNames lists what the connector may already be called here. A copy
// fetched by hand often keeps the asset name, which on Windows can arrive
// without the .exe suffix, and Windows runs it regardless.
func cloudflaredNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"cloudflared.exe", "cloudflared"}
	}
	return []string{"cloudflared"}
}

// CloudflaredDownloadURL returns where to fetch the connector, or an error for a
// platform Cloudflare does not publish.
//
// Windows and Linux assets are bare executables and only macOS ships an
// archive, which is why installing has to handle both shapes.
func CloudflaredDownloadURL(goos, goarch string) (string, error) {
	var suffix string
	var arches []string
	switch goos {
	case "windows":
		suffix, arches = ".exe", []string{"amd64", "386"}
	case "linux":
		suffix, arches = "", []string{"amd64", "386", "arm64", "arm"}
	case "darwin":
		suffix, arches = ".tgz", []string{"amd64", "arm64"}
	default:
		return "", fmt.Errorf("cloudflare publishes no connector for %s", goos)
	}

	supported := false
	for _, arch := range arches {
		if arch == goarch {
			supported = true
			break
		}
	}
	if !supported {
		return "", fmt.Errorf("cloudflare publishes no %s connector for %s", goarch, goos)
	}
	return fmt.Sprintf(cloudflaredDownloadTemplate, goos, goarch, suffix), nil
}

// FindCloudflared returns a connector already on this machine, preferring one
// shipped beside the server over whatever is on PATH.
func FindCloudflared() string {
	for _, dir := range searchDirs() {
		for _, name := range cloudflaredNames() {
			if candidate := filepath.Join(dir, name); isFile(candidate) {
				return candidate
			}
		}
	}
	for _, name := range cloudflaredNames() {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	return ""
}

// EnsureCloudflared returns a usable connector path, downloading into dir only
// when this machine has none. The second result reports whether it downloaded
// one, so a caller can say so instead of stalling silently on a slow link.
func EnsureCloudflared(dir string, fetch Fetcher) (string, bool, error) {
	for _, name := range cloudflaredNames() {
		if candidate := filepath.Join(dir, name); isFile(candidate) {
			return candidate, false, nil
		}
	}
	if existing := FindCloudflared(); existing != "" {
		return existing, false, nil
	}

	downloadURL, err := CloudflaredDownloadURL(runtime.GOOS, runtime.GOARCH)
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

	target := filepath.Join(dir, CloudflaredName())
	if err := installConnector(body, target); err != nil {
		return "", false, err
	}
	return target, true, nil
}

// installConnector buffers the download, then lands either the archived
// connector or the bare executable.
func installConnector(body io.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	download, err := os.CreateTemp(filepath.Dir(target), "cloudflared-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(download.Name())
	defer download.Close()

	size, err := io.Copy(download, io.LimitReader(body, maxArchiveBytes))
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if size == 0 {
		return errors.New("the download was empty")
	}
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractConnector(download, target)
}

// extractConnector picks the format from the bytes rather than the URL, so a
// mirror that renames files still works. A bare executable has to carry its own
// magic number, so the HTML error page a wrong URL answers with is never
// installed as if it were the connector.
func extractConnector(download *os.File, target string) error {
	magic := make([]byte, 4)
	if _, err := io.ReadFull(download, magic); err != nil {
		return errors.New("the download was too short to be a program")
	}
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if bytes.HasPrefix(magic, []byte{0x1f, 0x8b}) {
		return extractConnectorTarGz(download, target)
	}
	if !isExecutableMagic(magic) {
		return errors.New("the download was neither a gzip archive nor a program")
	}
	return writeExecutable(download, target)
}

func extractConnectorTarGz(archive io.Reader, target string) error {
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("read gzip: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the archive contained no cloudflared connector")
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !isConnectorEntry(header.Name) {
			continue
		}
		return writeExecutable(tarReader, target)
	}
}

// isConnectorEntry matches on the base name only. An entry's own path is never
// used to build a destination, so a crafted archive cannot escape the folder.
func isConnectorEntry(name string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(name)))
	return base == "cloudflared" || base == "cloudflared.exe"
}

// isExecutableMagic reports whether the bytes start like a program: PE for
// Windows, ELF for Linux, Mach-O or a universal binary for macOS.
func isExecutableMagic(magic []byte) bool {
	prefixes := [][]byte{
		[]byte("MZ"),
		{0x7f, 'E', 'L', 'F'},
		{0xfe, 0xed, 0xfa, 0xce},
		{0xfe, 0xed, 0xfa, 0xcf},
		{0xcf, 0xfa, 0xed, 0xfe},
		{0xce, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe},
	}
	for _, prefix := range prefixes {
		if bytes.HasPrefix(magic, prefix) {
			return true
		}
	}
	return false
}
