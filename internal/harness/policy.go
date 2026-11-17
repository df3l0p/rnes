package harness

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/df3l0p/rnes/internal/proxy"
)

const (
	// ProjectStateDir is the per-project folder inside the working directory.
	// Sandbox sessions shadow it with a read-only tmpfs at /workspace/.rnes
	// (and mount policy.yaml inside it read-only when present) so an agent
	// inside the container can never create, edit, or remove
	// .rnes/policy.yaml. A tmpfs rather than a nested bind mount keeps Docker
	// Desktop for Mac from putting a "deny delete" ACL on the host directory,
	// which used to make `git worktree remove` fail after a session.
	ProjectStateDir = ".rnes"
	// ProjectPolicyRel is the display path of a project's egress policy file.
	ProjectPolicyRel = ".rnes/policy.yaml"
	// LegacyProjectPolicyRel is the pre-YAML text format. It is rejected with
	// a conversion hint rather than silently ignored: a user who keeps an old
	// restrictive policy must not fall back to unrestricted defaults.
	LegacyProjectPolicyRel = ".rnes/policy.txt"
)

// EnsureProjectDir ensures <cwd>/.rnes exists as a real directory (never a
// symlink) owned by the current host user, returning its path.
func EnsureProjectDir(cwd string) (string, error) {
	dir := filepath.Join(cwd, ProjectStateDir)
	fi, err := os.Lstat(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("checking %s: %w", dir, err)
		}
		if err := os.Mkdir(dir, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", dir, err)
		}
		return dir, nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink; refusing to use it", ProjectStateDir)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s exists and is not a directory", ProjectStateDir)
	}
	return dir, nil
}

// GeneratePolicy creates <cwd>/.rnes/policy.yaml with the default allow-list
// template. It refuses to follow symlinks or overwrite an existing policy file.
func GeneratePolicy(cwd string) (string, error) {
	dir, err := EnsureProjectDir(cwd)
	if err != nil {
		return "", err
	}
	if err := rejectLegacyPolicy(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "policy.yaml")
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s already exists; edit it directly or remove it first", ProjectPolicyRel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking %s: %w", ProjectPolicyRel, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", ProjectPolicyRel, err)
	}
	defer f.Close()
	if _, err := f.Write(defaultPolicy); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing %s: %w", ProjectPolicyRel, err)
	}
	return path, nil
}

// HasProjectPolicy reports whether <cwd>/.rnes/policy.yaml exists on disk
// without creating .rnes or following symlinks on the file itself.
func HasProjectPolicy(cwd string) bool {
	return hasProjectFile(cwd, "policy.yaml")
}

// HasLegacyProjectPolicy reports whether <cwd>/.rnes/policy.txt exists, so a
// host run can say it is no longer read instead of silently ignoring it.
func HasLegacyProjectPolicy(cwd string) bool {
	return hasProjectFile(cwd, "policy.txt")
}

// hasProjectFile reports whether <cwd>/.rnes/<name> exists, without creating
// .rnes or following symlinks on the file itself.
func hasProjectFile(cwd, name string) bool {
	dir := filepath.Join(cwd, ProjectStateDir)
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false
	}
	_, err = os.Lstat(filepath.Join(dir, name))
	return err == nil
}

// rejectLegacyPolicy returns a conversion error when the pre-YAML
// .rnes/policy.txt is present. If both files exist the legacy one still has to
// go, so the active policy can never be ambiguous.
func rejectLegacyPolicy(dir string) error {
	legacy := filepath.Join(dir, "policy.txt")
	if _, err := os.Lstat(legacy); err == nil {
		return fmt.Errorf("%s is no longer read; convert it to %s (policy: v1, mode: allowlist, hosts: { ... }) and remove the old file", LegacyProjectPolicyRel, ProjectPolicyRel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", LegacyProjectPolicyRel, err)
	}
	return nil
}

// projectPolicyPath returns the host path of <cwd>/.rnes/policy.yaml when it
// exists. It applies the same symlink and regular-file checks as
// LoadProjectPolicy and never creates .rnes. found is false when either .rnes
// or policy.yaml is absent. A leftover policy.txt is an error (see
// rejectLegacyPolicy).
func projectPolicyPath(cwd string) (path string, found bool, err error) {
	dir := filepath.Join(cwd, ProjectStateDir)
	fi, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("checking %s: %w", ProjectStateDir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("%s is a symlink; refusing to use it", ProjectStateDir)
	}
	if !fi.IsDir() {
		return "", false, fmt.Errorf("%s exists and is not a directory", ProjectStateDir)
	}
	if err := rejectLegacyPolicy(dir); err != nil {
		return "", false, err
	}

	path = filepath.Join(dir, "policy.yaml")
	pfi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("checking %s: %w", ProjectPolicyRel, err)
	}
	if pfi.Mode()&os.ModeSymlink != 0 || !pfi.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s must be a regular file, not a symlink", ProjectPolicyRel)
	}
	return path, true, nil
}

// LoadProjectPolicy reads and validates <cwd>/.rnes/policy.yaml when present.
// When absent, found is false and raw is nil. Both .rnes and .rnes/policy.yaml
// are checked with Lstat so symlinks are rejected before anything starts.
func LoadProjectPolicy(cwd string) (raw []byte, pol proxy.Policy, found bool, err error) {
	path, found, err := projectPolicyPath(cwd)
	if err != nil || !found {
		return nil, proxy.Policy{}, false, err
	}

	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, proxy.Policy{}, false, fmt.Errorf("reading %s: %w", ProjectPolicyRel, err)
	}
	if raw == nil {
		raw = []byte{}
	}
	pol, err = parsePolicyYAML(bytes.NewReader(raw))
	if err != nil {
		return nil, proxy.Policy{}, false, fmt.Errorf("parsing %s: %w", ProjectPolicyRel, err)
	}
	return raw, pol, true, nil
}

// BlockedSummary reads a proxy log file and summarizes any "rnes-proxy: denied
// <target>" lines as "host1:port (Nx), host2:port (Mx)", sorted by target, or
// "" when nothing was denied.
func BlockedSummary(logPath string) string {
	if logPath == "" {
		return ""
	}
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	counts := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		_, target, ok := strings.Cut(sc.Text(), "rnes-proxy: denied ")
		if !ok {
			continue
		}
		if target = strings.TrimSpace(target); target != "" {
			counts[target]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	targets := make([]string, 0, len(counts))
	for t := range counts {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = fmt.Sprintf("%s (%dx)", t, counts[t])
	}
	return strings.Join(parts, ", ")
}
