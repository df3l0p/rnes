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
	// Sandbox sessions ensure it exists on the host (owned by the host user)
	// and bind-mount it read-only at /workspace/.rnes so an agent inside the
	// container can never create, edit, or remove .rnes/policy.txt.
	ProjectStateDir = ".rnes"
	// ProjectPolicyRel is the display path of a project's egress policy file.
	ProjectPolicyRel = ".rnes/policy.txt"
)

const starterPolicyHeader = `# Sandbox network allow-list for this project (.rnes/policy.txt).
#
# One host:port per line; '#' starts a comment. Everything else is blocked
# (403 from rnes-proxy). This session's llama-server (and openrouter.ai:443
# when RNES_OPENROUTER_API_KEY is set) are added automatically.
# Wildcard '*' is not allowed here; pass -all-net on the command line instead.
# Remove all entries below to restrict the sandbox to llama-server only.

`

const starterPolicyFooter = `
# Common package registries (uncomment as needed):
# proxy.golang.org:443
# sum.golang.org:443
# pypi.org:443
# files.pythonhosted.org:443
# crates.io:443
# static.crates.io:443
`

// starterPolicy builds the template written by GeneratePolicy, keeping the
// active host entries derived from defaultPolicy so the two never drift.
func starterPolicy() []byte {
	// defaultPolicy starts with its embedded-file header followed by a blank
	// line, then the commented host entries. Keep everything after that blank
	// line.
	body := defaultPolicy
	if _, rest, ok := bytes.Cut(defaultPolicy, []byte("\n\n")); ok {
		body = rest
	}
	s := string(body)
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(starterPolicyHeader + s + starterPolicyFooter)
}

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

// GeneratePolicy creates <cwd>/.rnes/policy.txt with the default allow-list
// template. It refuses to follow symlinks or overwrite an existing policy file.
func GeneratePolicy(cwd string) (string, error) {
	dir, err := EnsureProjectDir(cwd)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "policy.txt")
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
	if _, err := f.Write(starterPolicy()); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing %s: %w", ProjectPolicyRel, err)
	}
	return path, nil
}

// HasProjectPolicy reports whether <cwd>/.rnes/policy.txt exists on disk
// without creating .rnes or following symlinks on the file itself.
func HasProjectPolicy(cwd string) bool {
	dir := filepath.Join(cwd, ProjectStateDir)
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false
	}
	_, err = os.Lstat(filepath.Join(dir, "policy.txt"))
	return err == nil
}

// LoadProjectPolicy reads and validates <cwd>/.rnes/policy.txt when present.
// When absent, found is false and raw is nil. Both .rnes and .rnes/policy.txt
// are checked with Lstat so symlinks are rejected before anything starts.
func LoadProjectPolicy(cwd string) (raw []byte, pol proxy.Policy, found bool, err error) {
	dir := filepath.Join(cwd, ProjectStateDir)
	fi, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, proxy.Policy{}, false, nil
		}
		return nil, proxy.Policy{}, false, fmt.Errorf("checking %s: %w", ProjectStateDir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, proxy.Policy{}, false, fmt.Errorf("%s is a symlink; refusing to use it", ProjectStateDir)
	}
	if !fi.IsDir() {
		return nil, proxy.Policy{}, false, fmt.Errorf("%s exists and is not a directory", ProjectStateDir)
	}

	path := filepath.Join(dir, "policy.txt")
	pfi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, proxy.Policy{}, false, nil
		}
		return nil, proxy.Policy{}, false, fmt.Errorf("checking %s: %w", ProjectPolicyRel, err)
	}
	if pfi.Mode()&os.ModeSymlink != 0 || !pfi.Mode().IsRegular() {
		return nil, proxy.Policy{}, false, fmt.Errorf("%s must be a regular file, not a symlink", ProjectPolicyRel)
	}

	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, proxy.Policy{}, false, fmt.Errorf("reading %s: %w", ProjectPolicyRel, err)
	}
	if raw == nil {
		raw = []byte{}
	}
	pol, err = proxy.ParseUserPolicy(bytes.NewReader(raw))
	if err != nil {
		return nil, proxy.Policy{}, false, fmt.Errorf("parsing %s: %w", ProjectPolicyRel, err)
	}
	return raw, pol, true, nil
}

// GitHubHostsAllowed reports whether pol explicitly allows every host the
// GitHub credential provider is bound to.
func GitHubHostsAllowed(pol proxy.Policy) bool {
	for _, h := range proxy.GitHubHosts {
		if !pol.Explicit(h) {
			return false
		}
	}
	return true
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
