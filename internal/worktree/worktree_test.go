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
	all := append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", all...)
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

func TestGitDirsReadsARealWorktree(t *testing.T) {
	repo := gitRepo(t)
	path, err := Create(repo, "real")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, ok := GitDirs(path)
	if !ok {
		t.Fatal("GitDirs rejected a real linked worktree")
	}
	if !isWithin(got.CommonDir, got.Dir) {
		t.Errorf("git dir %q is not below common dir %q", got.Dir, got.CommonDir)
	}
	if got.CommonDir == string(filepath.Separator) {
		t.Errorf("common dir is the filesystem root")
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

func TestCreateRejectsANonRepository(t *testing.T) {
	if _, err := Create(t.TempDir(), "x"); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Errorf("Create(outside a repository) = %v, want a git repository error", err)
	}
}

func TestCreateRejectsAnOrdinaryDirectoryInTheWorktreeSlot(t *testing.T) {
	repo := gitRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, Dir, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(repo, "plain"); err == nil {
		t.Error("Create reused an ordinary directory")
	}
}

func TestCreateRejectsAForeignWorktree(t *testing.T) {
	repo := gitRepo(t)
	other := gitRepo(t)
	foreign, err := Create(other, "foreign")
	if err != nil {
		t.Fatal(err)
	}

	// Put a real directory with a foreign worktree's .git file in this
	// repository's .worktree slot. It must not be mistaken for a worktree of
	// repo.
	target := filepath.Join(repo, Dir, "foreign")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(foreign, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".git"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(repo, "foreign"); err == nil {
		t.Error("Create reused another repository's worktree")
	}
}

func TestCreateForcesAbsoluteWorktreePaths(t *testing.T) {
	repo := gitRepo(t)
	runGit(t, repo, "config", "worktree.useRelativePaths", "true")
	path, err := Create(repo, "feature-rel")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if !filepath.IsAbs(value) {
		t.Errorf("worktree .git uses a non-absolute gitdir %q", value)
	}
}

// fakeLinkedWorktree lays out the directories and files GitDirs needs for a
// linked worktree. rel controls whether the .git and gitdir files use relative
// paths.
func fakeLinkedWorktree(t *testing.T, repo, name string, rel bool) (wt, gitDir, common string) {
	t.Helper()
	common = filepath.Join(repo, ".git")
	gitDir = filepath.Join(common, "worktrees", name)
	wt = filepath.Join(repo, Dir, name)
	for _, d := range []string{gitDir, wt, common, filepath.Join(common, "objects")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{gitDir, common} {
		for _, sub := range []string{"HEAD", "refs"} {
			if sub == "HEAD" {
				if err := os.WriteFile(filepath.Join(d, sub), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	commondir := "../.."
	gitdir := filepath.Join(wt, ".git")
	gitfile := "gitdir: " + gitDir + "\n"
	if rel {
		commondir = "../.."
		gitdir = "../../../.worktree/" + name + "/.git"
		gitfile = "gitdir: ../../.git/worktrees/" + name + "\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(commondir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "gitdir"), []byte(gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}
	return wt, gitDir, common
}

func TestGitDirsReadsALinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	wt, gitDir, common := fakeLinkedWorktree(t, repo, "wt", false)

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

func TestGitDirsReadsASubmodule(t *testing.T) {
	super := t.TempDir()
	superGit := filepath.Join(super, ".git")
	sub := filepath.Join(super, "sub")
	subGit := filepath.Join(superGit, "modules", "sub")
	for _, d := range []string{
		superGit, filepath.Join(superGit, "objects"), filepath.Join(superGit, "refs"),
		subGit, filepath.Join(subGit, "objects"), filepath.Join(subGit, "refs"),
		sub,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{superGit, subGit} {
		if err := os.WriteFile(filepath.Join(d, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := GitDirs(sub)
	if !ok {
		t.Fatal("GitDirs rejected a submodule")
	}
	if got.Dir != subGit || got.CommonDir != subGit {
		t.Errorf("GitDirs = %+v, want git/common dir %s", got, subGit)
	}
}

func TestGitDirsRejectsAForeignSubmodule(t *testing.T) {
	other := t.TempDir()
	otherGit := filepath.Join(other, ".git")
	subGit := filepath.Join(otherGit, "modules", "sub")
	for _, d := range []string{otherGit, filepath.Join(otherGit, "objects"), filepath.Join(otherGit, "refs"), subGit, filepath.Join(subGit, "objects"), filepath.Join(subGit, "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{otherGit, subGit} {
		if err := os.WriteFile(filepath.Join(d, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	repo := t.TempDir()
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+subGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := GitDirs(sub); ok {
		t.Error("GitDirs accepted another repository's submodule")
	}
}

func TestGitDirsKeepsRelativePathsRelative(t *testing.T) {
	repo := t.TempDir()
	wt, gitDir, common := fakeLinkedWorktree(t, repo, "wt", true)

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
	if got.CommonDir != common {
		t.Errorf("CommonDir = %q", got.CommonDir)
	}
}

func TestGitDirsRejectsUnsafeOrUnrelatedDirectories(t *testing.T) {
	// A .git file pointing at an ordinary directory must not be accepted.
	plain := t.TempDir()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+plain+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := GitDirs(wt); ok {
		t.Error("GitDirs accepted a plain directory")
	}

	// A valid-looking git directory that isn't this worktree's layout is
	// refused too.
	other := t.TempDir()
	for _, d := range []string{other, filepath.Join(other, "objects"), filepath.Join(other, "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+other+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := GitDirs(wt); ok {
		t.Error("GitDirs accepted an unrelated git directory")
	}

	// A commondir that resolves to / must be rejected rather than mounted.
	repo := t.TempDir()
	linked, gitDir, _ := fakeLinkedWorktree(t, repo, "wt", false)
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := GitDirs(linked); ok {
		t.Error("GitDirs accepted a common dir of /")
	}
}
