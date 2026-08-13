package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data through a temporary file in the same directory
// and renames it into place, so a call that dies part way through leaves the
// previous file untouched instead of a half written one. It returns the size
// of the file as the filesystem reports it after the rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (int64, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create a temporary file next to %s: %w", filepath.Base(path), err)
	}
	tempName := temp.Name()

	written, err := writeAndSync(temp, data)
	if err != nil {
		temp.Close()
		os.Remove(tempName)
		return 0, err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return 0, fmt.Errorf("failed to close the temporary file: %w", err)
	}
	if written != int64(len(data)) {
		os.Remove(tempName)
		return 0, fmt.Errorf("short write: %d of %d bytes reached disk", written, len(data))
	}

	if err := os.Chmod(tempName, perm); err != nil {
		os.Remove(tempName)
		return 0, fmt.Errorf("failed to set the file mode: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return 0, fmt.Errorf("failed to replace %s: %w", filepath.Base(path), err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("wrote %s but could not verify it: %w", filepath.Base(path), err)
	}
	if info.Size() != int64(len(data)) {
		return info.Size(), fmt.Errorf(
			"verification failed: %s holds %d bytes but %d were written",
			filepath.Base(path), info.Size(), len(data))
	}
	return info.Size(), nil
}

// writeAndSync writes every byte and flushes them to disk, so the byte count
// that gets reported is one the filesystem has actually accepted.
func writeAndSync(file *os.File, data []byte) (int64, error) {
	written, err := file.Write(data)
	if err != nil {
		return int64(written), fmt.Errorf("failed to write the file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return int64(written), fmt.Errorf("failed to flush the file to disk: %w", err)
	}
	return int64(written), nil
}

// existingFileMode reports the mode of an existing file, falling back to the
// default for a new one, so rewriting a file does not change its permissions.
func existingFileMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return 0644
}
