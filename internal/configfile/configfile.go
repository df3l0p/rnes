// Package configfile rewrites a config file in the user's home without losing
// a concurrent edit or a crash: an flock around the read-modify-write, and a
// temp file + rename for the write itself.
package configfile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Update replaces the contents of the file at path with what transform returns
// for its current contents (nil when the file doesn't exist), creating the
// file and its directory when missing. An error from transform leaves the file
// untouched.
//
// A symlinked path is resolved first, so the link survives and the file it
// points at (a dotfiles-managed config, say) is the one rewritten. The existing
// file's permissions are preserved.
func Update(path string, transform func([]byte) ([]byte, error)) error {
	// Before anything else: os.Rename would otherwise replace the symlink
	// itself, leaving the real config stale and the link gone.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", path, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	out, err := transform(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".rnes-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	// Sync before the rename: without it the rename can be durable while the
	// data is not, which is the truncation the temp file exists to prevent.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
