package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// fileState is the identity of a file at one moment. Every tool that reads or
// writes a file returns one, so a caller can hand it straight back as a
// precondition on its next write without a second round trip.
type fileState struct {
	Sha256     string
	ModifiedAt string
	Size       int64
	Exists     bool
}

// fingerprintTime formats a modification time. Nanosecond precision keeps two
// writes in the same second distinguishable, which is the only thing that
// makes an mtime usable as a precondition at all.
func fingerprintTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// statFile reports the identity of a file on disk. A missing file is not an
// error: "absent" is a state a caller may legitimately be asserting.
func statFile(absPath string) (fileState, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fileState{}, nil
		}
		return fileState{}, err
	}
	if info.IsDir() {
		return fileState{}, errors.New("path is a directory, not a file")
	}

	sum, err := hashFile(absPath)
	if err != nil {
		return fileState{}, err
	}
	return fileState{
		Sha256:     sum,
		ModifiedAt: fingerprintTime(info.ModTime()),
		Size:       info.Size(),
		Exists:     true,
	}, nil
}

// hashFile hashes the bytes on disk, streaming so a large file is never held
// in memory twice.
//
// The hash covers the bytes as stored, not the text a read returns. Those
// differ for a CRLF file, and hashing what is actually on disk is the only
// version that can detect a change made by something other than this server.
func hashFile(absPath string) (string, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// hashBytes hashes content that is about to be written, so a write can report
// the new fingerprint without reading the file back.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// precondition is the optional "I expect the file to still look like this"
// assertion carried by write and edit. An empty one asserts nothing.
type precondition struct {
	Sha256     string
	ModifiedAt string
}

func (p precondition) empty() bool {
	return strings.TrimSpace(p.Sha256) == "" && strings.TrimSpace(p.ModifiedAt) == ""
}

// check compares the caller's expectation against the file on disk.
//
// It runs before any text matching, so a stale copy of a file is reported as a
// stale copy rather than as missing text. Those two failures have different
// fixes, and conflating them is what sends a caller hunting for a phantom
// second writer.
func (p precondition) check(absPath, displayPath string) error {
	if p.empty() {
		return nil
	}

	got, err := statFile(absPath)
	if err != nil {
		return fmt.Errorf("cannot verify the precondition for %s: %w", displayPath, err)
	}

	wantSha := strings.ToLower(strings.TrimSpace(p.Sha256))
	wantTime := strings.TrimSpace(p.ModifiedAt)

	switch {
	case !got.Exists:
		return staleError(displayPath, "the file does not exist any more", wantSha, wantTime, got)
	case wantSha != "" && wantSha != got.Sha256:
		return staleError(displayPath, "its contents changed", wantSha, wantTime, got)
	case wantTime != "" && !sameInstant(wantTime, got.ModifiedAt):
		return staleError(displayPath, "its modification time changed", wantSha, wantTime, got)
	}
	return nil
}

// sameInstant compares two timestamps as instants, so a caller that reformats
// or re-zones the value it was given is not punished for it.
func sameInstant(want, got string) bool {
	wantAt, wantErr := time.Parse(time.RFC3339, want)
	gotAt, gotErr := time.Parse(time.RFC3339, got)
	if wantErr != nil || gotErr != nil {
		return want == got
	}
	return wantAt.Equal(gotAt)
}

// staleError renders the "you are working from an old copy" failure.
//
// It says outright that nothing was written, because the first question after
// a failed write is always whether the file was left half changed.
func staleError(displayPath, because, wantSha, wantTime string, got fileState) error {
	var message strings.Builder
	fmt.Fprintf(&message, "precondition failed for %s: the file changed since you last read it (%s)", displayPath, because)
	message.WriteString("\n  nothing was written; the file on disk is untouched")

	if wantSha != "" {
		fmt.Fprintf(&message, "\n  expected sha256: %s", shortSha(wantSha))
		fmt.Fprintf(&message, "\n  actual sha256:   %s", shortSha(got.Sha256))
	}
	if wantTime != "" {
		fmt.Fprintf(&message, "\n  expected modifiedAt: %s", wantTime)
		fmt.Fprintf(&message, "\n  actual modifiedAt:   %s", orNone(got.ModifiedAt))
	}
	if got.Exists {
		fmt.Fprintf(&message, "\n  size now: %d bytes", got.Size)
	}

	message.WriteString("\n  this is not a text matching failure: re-read the file, rebuild the change from what it says now, and pass the fresh sha256")
	message.WriteString("\n  tip: recent_changes reports whether this server wrote the file, which tells your own edits apart from someone else's")
	return errors.New(message.String())
}

func shortSha(sum string) string {
	if sum == "" {
		return "(none)"
	}
	if len(sum) > 12 {
		return sum[:12] + "..."
	}
	return sum
}

func orNone(value string) string {
	if value == "" {
		return "(file absent)"
	}
	return value
}
