package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteKeepsAnExistingFilesCRLFEndings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0644); err != nil {
		t.Fatalf("seeding the file failed: %v", err)
	}

	out := runWrite(t, root, WriteInput{Path: "notes.txt", Content: "three\nfour\n"})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back failed: %v", err)
	}
	if string(got) != "three\r\nfour\r\n" {
		t.Errorf("file holds %q, want CRLF endings", string(got))
	}
	if !strings.Contains(out.Result, "CRLF") {
		t.Errorf("the reply did not mention the conversion: %q", out.Result)
	}
}

func TestWriteCountsTheBytesThatCRLFAdds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("one\r\n"), 0644); err != nil {
		t.Fatalf("seeding the file failed: %v", err)
	}

	out := runWrite(t, root, WriteInput{Path: "notes.txt", Content: "a\nb\n"})

	if !strings.Contains(out.Result, "6 bytes") {
		t.Errorf("reply reports the wrong size: %q", out.Result)
	}
}

func TestWriteLeavesANewFileExactlyAsSent(t *testing.T) {
	root := t.TempDir()

	runWrite(t, root, WriteInput{Path: "fresh.txt", Content: "one\ntwo\n"})

	got, err := os.ReadFile(filepath.Join(root, "fresh.txt"))
	if err != nil {
		t.Fatalf("reading the file back failed: %v", err)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("new file holds %q, want it byte for byte", string(got))
	}
}

func TestWriteDoesNotGiveAnLFFileCRLFEndings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unix.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatalf("seeding the file failed: %v", err)
	}

	runWrite(t, root, WriteInput{Path: "unix.txt", Content: "three\nfour\n"})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back failed: %v", err)
	}
	if strings.Contains(string(got), "\r\n") {
		t.Errorf("an LF file gained CRLF endings: %q", string(got))
	}
}

func TestWriteLeavesAMixedFileAlone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mixed.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\nthree\r\n"), 0644); err != nil {
		t.Fatalf("seeding the file failed: %v", err)
	}

	runWrite(t, root, WriteInput{Path: "mixed.txt", Content: "four\nfive\n"})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back failed: %v", err)
	}
	if string(got) != "four\nfive\n" {
		t.Errorf("mixed file was rewritten as %q, want it byte for byte", string(got))
	}
}
