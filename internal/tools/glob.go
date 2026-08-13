package tools

import (
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// searchWalkTimeout bounds a single filesystem walk so that a huge or slow
// tree cannot hold a tool call open indefinitely.
const searchWalkTimeout = 20 * time.Second

// maxCompiledGlobs bounds the pattern cache so a long-lived server cannot grow
// it without limit.
const maxCompiledGlobs = 512

// caseInsensitiveFS reports whether the host filesystem compares names without
// regard to case. Windows and macOS do by default, Linux does not.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

var (
	globCacheMu sync.RWMutex
	globCache   = make(map[string]*regexp.Regexp)
)

// normalizeGlobPattern accepts the pattern spellings clients actually send,
// including Windows separators and a leading "./" or "/", and returns the
// canonical slash-separated form used for matching.
func normalizeGlobPattern(pattern string) string {
	normalized := strings.TrimSpace(pattern)
	normalized = strings.ReplaceAll(normalized, `\`, "/")
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimLeft(normalized, "/")
	return normalized
}

// MatchGlob reports whether a slash-separated path, relative to the directory
// the search started from, matches pattern.
//
// Supported syntax:
//
//	"*"      any run of characters within one path segment
//	"?"      exactly one character within one path segment
//	"**"     any number of path segments, including none
//	"[a-z]"  character class, "[!abc]" negates
//	"{a,b}"  alternation
//
// A pattern that contains no separator is also matched against the base name,
// so "*.go" keeps finding files at every depth.
func MatchGlob(pattern, relPath string) bool {
	normalizedPattern := normalizeGlobPattern(pattern)
	if normalizedPattern == "" {
		return false
	}

	compiled, err := compileGlob(normalizedPattern)
	if err != nil {
		return false
	}

	normalizedPath := strings.TrimPrefix(filepath.ToSlash(relPath), "./")
	if compiled.MatchString(normalizedPath) {
		return true
	}
	if !strings.Contains(normalizedPattern, "/") {
		return compiled.MatchString(path.Base(normalizedPath))
	}
	return false
}

// compileGlob translates a glob pattern into an anchored regular expression and
// caches the result, because the same pattern is tested against every file in
// the walk.
func compileGlob(pattern string) (*regexp.Regexp, error) {
	globCacheMu.RLock()
	cached, ok := globCache[pattern]
	globCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	compiled, err := regexp.Compile(globRegexpSource(pattern))
	if err != nil {
		return nil, err
	}

	globCacheMu.Lock()
	if len(globCache) >= maxCompiledGlobs {
		globCache = make(map[string]*regexp.Regexp)
	}
	globCache[pattern] = compiled
	globCacheMu.Unlock()

	return compiled, nil
}

func globRegexpSource(pattern string) string {
	var source strings.Builder
	if caseInsensitiveFS {
		source.WriteString("(?i)")
	}
	source.WriteString(`\A`)

	braces := 0
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; c {
		case '*':
			stars := 0
			for i < len(pattern) && pattern[i] == '*' {
				stars++
				i++
			}
			switch {
			case stars == 1:
				source.WriteString(`[^/]*`)
			case i < len(pattern) && pattern[i] == '/':
				// "**/" spans zero or more complete segments, so
				// "internal/**/*.go" also matches "internal/main.go".
				source.WriteString(`(?:[^/]*/)*`)
				i++
			default:
				source.WriteString(`.*`)
			}
		case '?':
			source.WriteString(`[^/]`)
			i++
		case '[':
			class, next, ok := globCharClass(pattern, i)
			if !ok {
				// An unterminated class is a caller mistake. Emit a pattern
				// that cannot compile so the tool can report it instead of
				// silently returning nothing.
				return "("
			}
			source.WriteString(class)
			i = next
		case '{':
			braces++
			source.WriteString(`(?:`)
			i++
		case '}':
			if braces == 0 {
				source.WriteString(regexp.QuoteMeta("}"))
			} else {
				braces--
				source.WriteString(`)`)
			}
			i++
		case ',':
			if braces == 0 {
				source.WriteString(regexp.QuoteMeta(","))
			} else {
				source.WriteString(`|`)
			}
			i++
		default:
			// QuoteMeta only escapes ASCII metacharacters, so escaping byte by
			// byte still reproduces multi-byte UTF-8 sequences exactly.
			source.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	for ; braces > 0; braces-- {
		source.WriteString(`)`)
	}

	source.WriteString(`\z`)
	return source.String()
}

// globCharClass converts a bracket expression starting at start into its
// regular expression equivalent and returns the index just past it.
func globCharClass(pattern string, start int) (string, int, bool) {
	i := start + 1
	negated := false
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		negated = true
		i++
	}
	// A "]" immediately after the opening bracket is a literal.
	if i < len(pattern) && pattern[i] == ']' {
		i++
	}
	for i < len(pattern) && pattern[i] != ']' {
		i++
	}
	if i >= len(pattern) {
		return "", start, false
	}

	body := pattern[start+1 : i]
	if negated {
		body = body[1:]
	}
	if body == "" {
		return "", start, false
	}

	var class strings.Builder
	class.WriteString("[")
	if negated {
		class.WriteString("^")
	}
	class.WriteString(strings.ReplaceAll(body, `\`, `\\`))
	class.WriteString("]")
	return class.String(), i + 1, true
}
