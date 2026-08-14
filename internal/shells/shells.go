// Package shells discovers the command interpreters that are actually
// installed on this machine and turns a configured preference into a shell to
// run.
//
// The bash tool used to choose its shell from runtime.GOOS alone. On Windows
// that meant every command went to PowerShell.exe: configuring "bash" or "sh"
// fell through to the same PowerShell branch and was silently ignored, even
// with Git for Windows installed. The configuration UI made it worse by
// offering a fixed list of four shells whether or not they existed.
//
// Detection is deliberately conservative. A shell is offered only when its
// executable is found, and "auto" keeps the historical order - PowerShell first
// on Windows, bash first everywhere else - so upgrading does not change which
// shell an existing configuration ends up using.
package shells

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// Kind describes how a shell expects a command to be handed to it.
type Kind string

const (
	// KindPosix takes -c "command": bash, sh, ash, dash, zsh.
	KindPosix Kind = "posix"

	// KindPowerShell takes -Command "command": powershell.exe, pwsh.
	KindPowerShell Kind = "powershell"

	// KindCmd takes /C "command": cmd.exe.
	KindCmd Kind = "cmd"
)

// knownIDs are every shell this package can detect, on any platform. It exists
// so that an unavailable shell and a misspelled one produce different errors.
var knownIDs = []string{"powershell", "pwsh", "cmd", "bash", "sh", "ash", "dash", "zsh"}

// Shell is an interpreter found on this machine.
type Shell struct {
	// ID is the stable value stored in configuration and shown in the UI.
	ID string

	// Kind describes how to pass a command to it.
	Kind Kind

	// Path is the executable that was found.
	Path string

	// Note flags an install that does not behave the way its ID suggests.
	Note string
}

// Label renders a shell for humans, for diagnostics and startup output.
func (s Shell) Label() string {
	if s.Note != "" {
		return fmt.Sprintf("%s (%s) at %s", s.ID, s.Note, s.Path)
	}
	return fmt.Sprintf("%s at %s", s.ID, s.Path)
}

// prober is the machine as far as detection is concerned. Injecting it keeps
// the tests independent of what happens to be installed on the test runner.
type prober struct {
	lookPath func(exe string) (string, error)
	exists   func(path string) bool
	getenv   func(key string) string
	goos     string
}

func defaultProber() prober {
	return prober{
		lookPath: exec.LookPath,
		exists: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && !info.IsDir()
		},
		getenv: os.Getenv,
		goos:   runtime.GOOS,
	}
}

// candidate is one shell to look for, in auto-preference order.
type candidate struct {
	id   string
	kind Kind
	exes []string
	dirs []string
}

var (
	cacheMu sync.Mutex
	cached  []Shell
	cacheOK bool
)

// Detect returns the shells installed on this machine, in auto-preference
// order. The result is cached because it stats a handful of directories and is
// consulted on every bash call.
func Detect() []Shell {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if !cacheOK {
		cached = detect(defaultProber())
		cacheOK = true
	}
	return slices.Clone(cached)
}

// Reset clears the detection cache. Configuration changes and tests need it.
func Reset() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached, cacheOK = nil, false
}

func detect(p prober) []Shell {
	var found []Shell
	for _, c := range candidates(p) {
		if path, ok := locate(p, c); ok {
			found = append(found, annotate(Shell{ID: c.id, Kind: c.kind, Path: path}, p))
		}
	}
	return found
}

// locate prefers PATH, then the well known install directories. Git for
// Windows is the reason the directory sweep exists: its bash, sh and ash are
// frequently installed without ever being added to PATH.
func locate(p prober, c candidate) (string, bool) {
	for _, exe := range c.exes {
		if path, err := p.lookPath(exe); err == nil && path != "" {
			return path, true
		}
	}
	for _, dir := range c.dirs {
		for _, exe := range c.exes {
			path := filepath.Join(dir, exe)
			if p.exists(path) {
				return path, true
			}
		}
	}
	return "", false
}

// annotate marks installs that do not behave the way their ID suggests.
// Windows ships bash.exe in System32 as the WSL launcher: it runs inside the
// Linux distribution, so a Windows working directory and Windows paths in the
// command are not what the command actually sees.
func annotate(sh Shell, p prober) Shell {
	if p.goos != "windows" || sh.ID != "bash" {
		return sh
	}
	if strings.EqualFold(filepath.Dir(sh.Path), filepath.Join(systemRoot(p), "System32")) {
		sh.Note = "WSL"
	}
	return sh
}

func candidates(p prober) []candidate {
	if p.goos == "windows" {
		git := gitDirs(p)
		system32 := filepath.Join(systemRoot(p), "System32")
		return []candidate{
			{id: "powershell", kind: KindPowerShell, exes: []string{"powershell.exe"}, dirs: []string{filepath.Join(system32, "WindowsPowerShell", "v1.0")}},
			{id: "pwsh", kind: KindPowerShell, exes: []string{"pwsh.exe"}, dirs: pwshDirs(p)},
			{id: "cmd", kind: KindCmd, exes: []string{"cmd.exe"}, dirs: []string{system32}},
			{id: "bash", kind: KindPosix, exes: []string{"bash.exe"}, dirs: git},
			{id: "sh", kind: KindPosix, exes: []string{"sh.exe"}, dirs: git},
			{id: "ash", kind: KindPosix, exes: []string{"ash.exe"}, dirs: git},
			{id: "zsh", kind: KindPosix, exes: []string{"zsh.exe"}, dirs: git},
		}
	}
	return []candidate{
		{id: "bash", kind: KindPosix, exes: []string{"bash"}, dirs: unixDirs},
		{id: "sh", kind: KindPosix, exes: []string{"sh"}, dirs: unixDirs},
		{id: "zsh", kind: KindPosix, exes: []string{"zsh"}, dirs: unixDirs},
		{id: "ash", kind: KindPosix, exes: []string{"ash"}, dirs: unixDirs},
		{id: "dash", kind: KindPosix, exes: []string{"dash"}, dirs: unixDirs},
		{id: "pwsh", kind: KindPowerShell, exes: []string{"pwsh"}, dirs: []string{"/usr/bin", "/usr/local/bin", "/opt/microsoft/powershell/7"}},
	}
}

var unixDirs = []string{"/bin", "/usr/bin", "/usr/local/bin", "/opt/homebrew/bin"}

func systemRoot(p prober) string {
	if root := p.getenv("SystemRoot"); root != "" {
		return root
	}
	return `C:\Windows`
}

// gitDirs lists the bin directories of every Git for Windows install worth
// probing, including the per-user install that does not touch PATH.
func gitDirs(p prober) []string {
	var roots []string
	add := func(base string, rest ...string) {
		if base == "" {
			return
		}
		roots = append(roots, filepath.Join(append([]string{base}, rest...)...))
	}
	add(p.getenv("ProgramFiles"), "Git")
	add(p.getenv("ProgramW6432"), "Git")
	add(p.getenv("ProgramFiles(x86)"), "Git")
	add(p.getenv("LOCALAPPDATA"), "Programs", "Git")
	roots = append(roots, `C:\Program Files\Git`, `C:\Program Files (x86)\Git`)

	var dirs []string
	for _, root := range roots {
		dirs = append(dirs,
			filepath.Join(root, "bin"),
			filepath.Join(root, "usr", "bin"),
			filepath.Join(root, "mingw64", "bin"),
		)
	}
	return dirs
}

func pwshDirs(p prober) []string {
	var dirs []string
	for _, base := range []string{p.getenv("ProgramFiles"), p.getenv("ProgramW6432"), p.getenv("ProgramFiles(x86)"), `C:\Program Files`} {
		if base == "" {
			continue
		}
		for _, major := range []string{"7", "6"} {
			dirs = append(dirs, filepath.Join(base, "PowerShell", major))
		}
	}
	return dirs
}

// Resolve turns a configured preference into the shell to run. An absolute
// path is honoured as-is so that an install this package does not know about
// can still be used.
func Resolve(preference string) (Shell, error) {
	p := defaultProber()
	if sh, ok := resolveExplicitPath(p, preference); ok {
		return sh, nil
	}
	return resolve(Detect(), preference)
}

func resolveExplicitPath(p prober, preference string) (Shell, bool) {
	pref := strings.TrimSpace(preference)
	if pref == "" || !strings.ContainsAny(pref, `/\`) {
		return Shell{}, false
	}
	if !p.exists(pref) {
		return Shell{}, false
	}
	base := filepath.Base(pref)
	sh := Shell{ID: normalizeID(base), Kind: kindForName(base), Path: pref}
	return annotate(sh, p), true
}

func resolve(available []Shell, preference string) (Shell, error) {
	if len(available) == 0 {
		return Shell{}, errors.New("no supported shell was found on this machine")
	}
	pref := normalizeID(preference)
	if pref == "" || pref == "auto" {
		return available[0], nil
	}
	for _, sh := range available {
		if sh.ID == pref {
			return sh, nil
		}
	}
	if slices.Contains(knownIDs, pref) {
		return Shell{}, fmt.Errorf("configured shell %q is not installed; available: %s", strings.TrimSpace(preference), strings.Join(IDs(available), ", "))
	}
	return Shell{}, fmt.Errorf("unknown shell %q; available: %s", strings.TrimSpace(preference), strings.Join(IDs(available), ", "))
}

// normalizeID accepts what people actually write in configuration:
// "PowerShell.EXE", " bash ", "cmd.exe".
func normalizeID(name string) string {
	id := strings.ToLower(strings.TrimSpace(name))
	return strings.TrimSuffix(id, ".exe")
}

func kindForName(name string) Kind {
	switch normalizeID(name) {
	case "powershell", "pwsh":
		return KindPowerShell
	case "cmd":
		return KindCmd
	default:
		return KindPosix
	}
}

// IDs lists the identifiers of the given shells.
func IDs(shells []Shell) []string {
	ids := make([]string, 0, len(shells))
	for _, sh := range shells {
		ids = append(ids, sh.ID)
	}
	return ids
}

// Options lists the values a configuration UI should offer: "auto" first, then
// every shell found on this machine. A configured value that is not installed
// is kept in the list so that opening the UI cannot silently change it.
func Options(configured string) []string {
	return options(Detect(), configured)
}

func options(available []Shell, configured string) []string {
	opts := append([]string{"auto"}, IDs(available)...)
	current := strings.TrimSpace(configured)
	if current == "" || slices.Contains(opts, current) {
		return opts
	}
	return append(opts, current)
}

// Describe renders the detected shells for diagnostics output.
func Describe() []string {
	shells := Detect()
	labels := make([]string, 0, len(shells))
	for _, sh := range shells {
		labels = append(labels, sh.Label())
	}
	return labels
}
