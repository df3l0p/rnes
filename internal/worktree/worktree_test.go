package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a git repository with one commit and returns its root.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "rnes@example.com")
	runGit(t, dir, "config", "user.name", "rnes")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestCreateMakesAWorktreeAndReusesIt(t *testing.T) {
	repo := gitRepo(t)
	nested := filepath.Join(repo, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Create(nested, "feature-01")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := filepath.Join(repo, Dir, "feature-01")
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if filepath.Clean(path) != filepath.Clean(want) {
		t.Errorf("Create = %q, want %q", path, want)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree has no .git file: %v", err)
	}
	if branch := strings.TrimSpace(runGit(t, path, "branch", "--show-current")); branch != "feature-01" {
		t.Errorf("branch = %q, want feature-01", branch)
	}

	// A second rnes session with the same name reuses the worktree instead of
	// failing because the path and branch exist.
	again, err := Create(repo, "feature-01")
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if again != path {
		t.Errorf("Create again = %q, want %q", again, path)
	}
}

func TestCreateChecksOutAnExistingBranch(t *testing.T) {
	repo := gitRepo(t)
	runGit(t, repo, "branch", "already-there")
	path, err := Create(repo, "already-there")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if branch := strings.TrimSpace(runGit(t, path, "branch", "--show-current")); branch != "already-there" {
		t.Errorf("branch = %q, want already-there", branch)
	}
}

func TestCreateRejectsBadNames(t *testing.T) {
	repo := gitRepo(t)
	for _, name := range []string{"", ".", "..", "../escape", "a/b", ".hidden", "-dash", "a b", "a..b"} {
		if _, err := Create(repo, name); err == nil {
			t.Errorf("Create(%q) = nil, want an error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, Dir, "escape")); !os.IsNotExist(err) {
		t.Errorf("a bad name created something under %s", filepath.Join(repo, Dir))
	}
}

func TestCreateRejectsANonRepositoryAndAForeignDirectory(t *testing.T) {
	if _, err := Create(t.TempDir(), "x"); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Errorf("Create(outside a repository) = %v, want a git repository error", err)
	}

	repo := gitRepo(t)
	other := gitRepo(t)
	foreign, err := Create(other, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	// A directory already at <repo>/.worktree/x that belongs to another
	// repository must not be mistaken for this repo's worktree.
	base := filepath.Join(repo, Dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(base, "foreign")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(repo, "foreign"); err == nil {
		t.Error("Create reused a symlink or another repository's worktree")
	}
}

func TestGitDirsReadsALinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	common := filepath.Join(repo, ".git")
	gitDir := filepath.Join(common, "worktrees", "wt")
	for _, d := range []string{gitDir, filepath.Join(repo, Dir, "wt")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(repo, Dir, "wt")
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := GitDirs(wt)
	if !ok {
		t.Fatal("GitDirs says it isn't a linked worktree")
	}
	if got.Dir != gitDir || got.CommonDir != common || got.RelDir != gitDir || got.RelCommon != "../.." {
		t.Errorf("GitDirs = %+v", got)
	}

	// A normal checkout's .git directory is not a linked worktree.
	if _, ok := GitDirs(repo); ok {
		t.Error("GitDirs(repository) = true, want false for a .git directory")
	}
	// Neither is a directory with no .git at all.
	if _, ok := GitDirs(t.TempDir()); ok {
		t.Error("GitDirs(no checkout) = true, want false")
	}
}

func TestGitDirsKeepsRelativePathsRelative(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git", "worktrees", "wt")
	wt := filepath.Join(repo, Dir, "wt")
	for _, d := range []string{gitDir, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../../.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := GitDirs(wt)
	if !ok {
		t.Fatal("GitDirs says it isn't a linked worktree")
	}
	if got.RelDir != "../../.git/worktrees/wt" {
		t.Errorf("RelDir = %q", got.RelDir)
	}
	if got.Dir != gitDir {
		t.Errorf("Dir = %q, want %q", got.Dir, gitDir)
	}
	if got.CommonDir != filepath.Join(repo, ".git") {
		t.Errorf("CommonDir = %q", got.CommonDir)
	}
}
