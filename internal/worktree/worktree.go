// Package worktree creates git worktrees for rnes sessions and reports the
// git metadata a sandbox needs to make git work inside one.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Dir is the directory (relative to a repository root) that holds rnes's
// worktrees.
const Dir = ".worktree"

// Dirs describes the git directories behind a linked worktree's .git file.
// The zero value is what GitDirs returns for a normal checkout (where .git is a
// directory) or for a directory that isn't a checkout at all.
type Dirs struct {
	// Dir is the absolute path of the worktree's own git directory (the one
	// the worktree's .git file points at).
	Dir string
	// CommonDir is the absolute path of the git directory shared with the
	// main checkout. It is Dir itself when the worktree has no commondir
	// file (for example, a submodule's checkout).
	CommonDir string
	// RelDir is the gitdir value exactly as written in the .git file. It is
	// absolute for worktrees made by git's default, and relative for those
	// made with --relative-paths.
	RelDir string
	// RelCommon is the content of Dir's commondir file, or "" when it has
	// none.
	RelCommon string
}

// repoRoot returns the top-level directory of the git working tree containing
// dir.
func repoRoot(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repository (required for -worktree): %w", err)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", fmt.Errorf("not a git repository (required for -worktree)")
	}
	return filepath.Clean(root), nil
}

// Create ensures the linked worktree <repo>/.worktree/name exists and returns
// its path. It creates the worktree (on a new branch named name, or on an
// existing branch of that name) from dir's repository root when missing, and
// reuses it when it is already a worktree of that repository. dir may be any
// directory inside the repository.
func Create(dir, name string) (string, error) {
	root, err := repoRoot(dir)
	if err != nil {
		return "", err
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	base, err := ensureBase(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, name)

	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("-worktree %s: %s is a symlink; refusing to use it", name, path)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("-worktree %s: %s exists and is not a directory", name, path)
		}
		if !isWorktreeOf(path, root) {
			return "", fmt.Errorf("-worktree %s: %s exists and is not a worktree of %s", name, path, root)
		}
		return path, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("checking %s: %w", path, err)
	}

	args := []string{"worktree", "add", "--quiet"}
	if branchExists(root, name) {
		args = append(args, path, name)
	} else {
		args = append(args, "-b", name, path, "HEAD")
	}
	if _, err := gitOutput(root, args...); err != nil {
		return "", fmt.Errorf("creating worktree %s: %w", path, err)
	}
	return path, nil
}

// GitDirs reads a linked worktree's .git file and resolves the git directories
// it points at. ok is false when dir has no .git file (a normal checkout or no
// checkout at all), or when the file doesn't point at a usable git directory.
func GitDirs(dir string) (dirs Dirs, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return Dirs{}, false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	value, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !found {
		return Dirs{}, false
	}
	relDir := strings.TrimSpace(value)
	if relDir == "" {
		return Dirs{}, false
	}
	gitDir := relDir
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return Dirs{}, false
	}

	commonDir, relCommon := gitDir, ""
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		relCommon = strings.TrimSpace(string(raw))
		if relCommon != "" {
			commonDir = relCommon
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(gitDir, commonDir)
			}
			commonDir = filepath.Clean(commonDir)
		}
	}
	return Dirs{Dir: gitDir, CommonDir: commonDir, RelDir: relDir, RelCommon: relCommon}, true
}

// ensureBase ensures <root>/.worktree is a real directory owned by the user,
// returning its path. A symlink is refused so a worktree can't be redirected
// outside the repository.
func ensureBase(root string) (string, error) {
	base := filepath.Join(root, Dir)
	fi, err := os.Lstat(base)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symlink; refusing to use it", base)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s exists and is not a directory", base)
		}
		return base, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("checking %s: %w", base, err)
	}
	if err := os.Mkdir(base, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", base, err)
	}
	return base, nil
}

// isWorktreeOf reports whether path is a linked worktree of the repository at
// root (not merely a checkout of some other repository).
func isWorktreeOf(path, root string) bool {
	pathCommon, err := commonDir(path)
	if err != nil {
		return false
	}
	rootCommon, err := commonDir(root)
	if err != nil {
		return false
	}
	return pathCommon == rootCommon
}

// commonDir resolves dir's common git directory to an absolute path, whether
// dir is the main checkout or a linked worktree.
func commonDir(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return "", fmt.Errorf("git reported no common directory")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Clean(common), nil
}

// validateName rejects names that can't be a single directory below .worktree
// and a valid git branch name. Keeping it to one path component also keeps
// -worktree from writing outside .worktree.
func validateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("-worktree: empty name")
	case name == "." || name == "..":
		return fmt.Errorf("-worktree %q: not a valid worktree name", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("-worktree %q: a worktree name can't start with a dot", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("-worktree %q: a worktree name can't start with a dash", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("-worktree %q: a worktree name can't contain a path separator", name)
	}
	// Let git decide the remaining branch-name rules (spaces, @{, .., a
	// trailing dot, control characters, and so on).
	if _, err := gitOutput("", "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("-worktree %q: not a valid branch name: %w", name, err)
	}
	return nil
}

// branchExists reports whether the repository at root has a local branch
// called name.
func branchExists(root, name string) bool {
	_, err := gitOutput(root, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// gitOutput runs git with args, in dir when dir is non-empty, and returns its
// stdout. Errors include git's stderr so a failed worktree command says why.
func gitOutput(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}
	return string(out), nil
}
