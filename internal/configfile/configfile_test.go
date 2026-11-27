package configfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestUpdateCreatesTheFileAndItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	err := Update(path, func(raw []byte) ([]byte, error) {
		if raw != nil {
			t.Errorf("transform got %q for a missing file, want nil", raw)
		}
		return []byte("a = 1\n"), nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config: %v", err)
	}
	if string(raw) != "a = 1\n" {
		t.Errorf("config = %q, want %q", raw, "a = 1\n")
	}
}

func TestUpdatePreservesTheExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o600); err != nil {
		t.Fatalf("seeding the config: %v", err)
	}
	if err := Update(path, func([]byte) ([]byte, error) { return []byte("a = 2\n"), nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

// A config.toml managed by a dotfiles repo is a symlink; rewriting the link
// itself would leave the real file stale and the link gone.
func TestUpdateWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "config.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatalf("creating the dotfiles dir: %v", err)
	}
	if err := os.WriteFile(real, []byte("a = 1\n"), 0o644); err != nil {
		t.Fatalf("seeding the real config: %v", err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlinking: %v", err)
	}

	if err := Update(link, func([]byte) ([]byte, error) { return []byte("a = 2\n"), nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		t.Fatalf("reading the real config: %v", err)
	}
	if string(raw) != "a = 2\n" {
		t.Errorf("the real config = %q, want the rewritten contents", raw)
	}
}

func TestUpdateLeavesTheFileAloneWhenTransformFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o644); err != nil {
		t.Fatalf("seeding the config: %v", err)
	}
	boom := errors.New("boom")
	err := Update(path, func([]byte) ([]byte, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("Update error = %v, want it to wrap %v", err, boom)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config: %v", err)
	}
	if string(raw) != "a = 1\n" {
		t.Errorf("config = %q, want it untouched", raw)
	}
}

func TestUpdateLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Update(path, func([]byte) ([]byte, error) { return []byte("a = 1\n"), nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
}

// The flock is the whole reason for the lock file: concurrent writers must not
// drop one another's changes.
func TestUpdateSerialisesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Update(path, func(raw []byte) ([]byte, error) {
				return []byte(fmt.Sprintf("%sline-%d\n", raw, i)), nil
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config: %v", err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != writers {
		t.Fatalf("config has %d lines, want %d (a writer's change was dropped):\n%s", len(lines), writers, raw)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l] {
			t.Errorf("duplicate line %q", l)
		}
		seen[l] = true
	}
}
