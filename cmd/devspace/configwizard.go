package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/snakex21/devspace-go/internal/config"
)

// A language list is long enough to bury the prompt, so only the first few are
// printed and the rest are counted.
const maxListedChoices = 12

// runConfigWizard walks the settings interactively. It is what replaces the
// desktop configurator: the same fields, without a window or a GPU.
func runConfigWizard(in io.Reader, out io.Writer) int {
	cfg := loadStoredConfig()
	reader := bufio.NewReader(in)
	all := settings()
	changed := false

	fmt.Fprintln(out, "MCP WebCoder configuration")
	fmt.Fprintf(out, "File: %s\n", configFilePath(cfg))

	// A first run has nothing to serve, so start where the answer is required
	// instead of making people find it in the list.
	if len(cfg.AllowedRoots) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No folders are allowed yet. The server cannot start without one.")
		if s, ok := findSetting("roots"); ok && editSetting(cfg, s, reader, out) {
			changed = true
		}
	}

	for {
		printWizardMenu(cfg, all, out)
		choice, err := prompt(reader, out, "Number to change, s to save, q to quit: ")
		if err != nil {
			// Input ended, which is what happens when this runs from a script
			// or a pipe. Printing the table is the useful half of the job.
			fmt.Fprintln(out)
			fmt.Fprintln(out, "No more input; nothing was saved.")
			return 0
		}

		switch strings.ToLower(choice) {
		case "":
			continue
		case "q", "quit", "exit":
			if changed {
				fmt.Fprintln(out, "Left unsaved; nothing was written.")
			}
			return 0
		case "s", "save":
			return saveFromWizard(cfg, changed, out)
		}

		index, ok := menuIndex(choice, len(all))
		if !ok {
			fmt.Fprintf(out, "Not one of the choices: %s\n", choice)
			continue
		}
		if editSetting(cfg, all[index], reader, out) {
			changed = true
		}
	}
}

func saveFromWizard(cfg *config.Config, changed bool, out io.Writer) int {
	if !changed {
		fmt.Fprintln(out, "Nothing changed, so nothing was written.")
		return 0
	}
	if err := writeConfigFile(cfg); err != nil {
		fmt.Fprintf(out, "Cannot save: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Saved %s\n", configFilePath(cfg))
	if len(cfg.AllowedRoots) == 0 {
		fmt.Fprintln(out, "No folders are allowed, so the server will refuse to start.")
		return 0
	}
	fmt.Fprintln(out, "Run 'mcp-webcoder serve' to start the server.")
	return 0
}

func printWizardMenu(cfg *config.Config, all []setting, out io.Writer) {
	width := 0
	for _, s := range all {
		if len(s.key) > width {
			width = len(s.key)
		}
	}
	fmt.Fprintln(out)
	for i, s := range all {
		fmt.Fprintf(out, "%2d. %-*s  %s\n", i+1, width, s.key, s.display(cfg))
	}
	if shadows := envShadows(); len(shadows) > 0 {
		fmt.Fprintln(out, "Overridden by the environment for this session:")
		for _, shadow := range shadows {
			fmt.Fprintf(out, "    %s\n", shadow)
		}
	}
}

// editSetting asks for one value and keeps asking while the answer is refused,
// which beats a wizard that starts over on a typo.
func editSetting(cfg *config.Config, s setting, reader *bufio.Reader, out io.Writer) bool {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s: %s\n", s.key, s.help)
	fmt.Fprintf(out, "Current: %s\n", s.display(cfg))
	if s.choices != nil {
		fmt.Fprintf(out, "Options: %s\n", describeChoices(s.choices(cfg)))
	}
	if s.env != "" {
		fmt.Fprintf(out, "Environment: %s\n", s.env)
	}

	for {
		answer, err := prompt(reader, out, "New value (blank keeps it): ")
		if err != nil || answer == "" {
			return false
		}
		if err := s.parse(cfg, answer); err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		fmt.Fprintf(out, "  %s = %s\n", s.key, s.display(cfg))
		return true
	}
}

func describeChoices(choices []string) string {
	if len(choices) <= maxListedChoices {
		return strings.Join(choices, ", ")
	}
	return fmt.Sprintf("%s ... (%d in total)", strings.Join(choices[:maxListedChoices], ", "), len(choices))
}

func menuIndex(choice string, count int) (int, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil || number < 1 || number > count {
		return 0, false
	}
	return number - 1, true
}

// prompt reads one line. A final line without a newline still counts, because
// that is how piped input usually ends.
func prompt(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprint(out, label)
	line, err := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
	if err != nil && answer == "" {
		return "", err
	}
	return answer, nil
}
