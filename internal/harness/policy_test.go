package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/proxy"
)

func TestGeneratePolicyWritesTemplateAndRefusesOverwriteOrSymlink(t *testing.T) {
	cwd := t.TempDir()
	path, err := GeneratePolicy(cwd)
	if err != nil {
		t.Fatalf("GeneratePolicy: %v", err)
	}
	if path != filepath.Join(cwd, ".rnes", "policy.txt") {
		t.Errorf("path = %q", path)
	}

	raw, pol, found, err := LoadProjectPolicy(cwd)
	if err != nil || !found {
		t.Fatalf("LoadProjectPolicy: found=%v err=%v", found, err)
	}
	if !bytes.Equal(raw, defaultPolicy) {
		t.Errorf("generated policy doesn't match policy.txt:\ngot:\n%s\nwant:\n%s", raw, defaultPolicy)
	}
	defPol, err := proxy.ParsePolicy(bytes.NewReader(defaultPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pol.SortedAllow(), ",") != strings.Join(defPol.SortedAllow(), ",") {
		t.Errorf("generated policy hosts = %v, want %v\n%s", pol.SortedAllow(), defPol.SortedAllow(), raw)
	}

	// Must refuse to overwrite an existing policy.txt.
	if _, err := GeneratePolicy(cwd); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second GeneratePolicy = %v, want already-exists error", err)
	}

	// Must refuse when .rnes is a symlink.
	symCwd := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(symCwd, ".rnes")); err != nil {
		t.Fatal(err)
	}
	if _, err := GeneratePolicy(symCwd); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("GeneratePolicy with symlink .rnes = %v, want symlink error", err)
	}
}

func TestLoadProjectPolicyHandlesAbsentEmptyCustomAndInvalid(t *testing.T) {
	// Absent .rnes directory -> found=false, nil raw, and does not create .rnes.
	absent := t.TempDir()
	raw, _, found, err := LoadProjectPolicy(absent)
	if err != nil || found || raw != nil {
		t.Fatalf("absent = (%v, found=%v, err=%v)", raw, found, err)
	}
	if _, err := os.Lstat(filepath.Join(absent, ".rnes")); !os.IsNotExist(err) {
		t.Errorf("LoadProjectPolicy must not create .rnes when absent")
	}

	// Empty 0-byte .rnes/policy.txt -> found=true, non-nil empty slice (so
	// Sandbox uses it instead of falling back to defaultPolicy).
	emptyCwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(emptyCwd, ".rnes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyCwd, ".rnes", "policy.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, pol, found, err := LoadProjectPolicy(emptyCwd)
	if err != nil || !found || raw == nil || len(pol.Allow) != 0 {
		t.Fatalf("empty = (rawNil=%v, allow=%v, found=%v, err=%v)", raw == nil, pol.Allow, found, err)
	}
	s := newSandbox(Options{Sandbox: true, Policy: raw}, "")
	s.modelServer = "host.docker.internal:8080"
	merged, err := proxy.ParsePolicy(bytes.NewReader(s.policy()))
	if err != nil {
		t.Fatalf("ParsePolicy(merged): %v", err)
	}
	if strings.Join(merged.SortedAllow(), ",") != "host.docker.internal:8080" || !merged.AllowsPrivate("host.docker.internal:8080") {
		t.Errorf("empty custom policy should allow only private llama-server, got %+v", merged)
	}

	// Rejects '*' in .rnes/policy.txt.
	starCwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(starCwd, ".rnes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(starCwd, ".rnes", "policy.txt"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadProjectPolicy(starCwd); err == nil || !strings.Contains(err.Error(), "-allow-public") {
		t.Errorf("star policy err = %v, want error naming -allow-public", err)
	}

	// A per-entry private mark is accepted, and stays scoped to private ranges
	// (it must not become an unrestricted entry).
	privCwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(privCwd, ".rnes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privCwd, ".rnes", "policy.txt"), []byte("private host.docker.internal:5432\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, privPol, found, err := LoadProjectPolicy(privCwd)
	if err != nil || !found {
		t.Fatalf("private policy = (found=%v, err=%v)", found, err)
	}
	if !privPol.AllowsPrivate("host.docker.internal:5432") || privPol.AllowsAnyAddress("host.docker.internal:5432") {
		t.Errorf("private policy = %+v, want a scoped private entry", privPol)
	}

	// Rejects symlinked policy.txt.
	symFileCwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(symFileCwd, ".rnes"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("github.com:443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(symFileCwd, ".rnes", "policy.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadProjectPolicy(symFileCwd); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink policy.txt err = %v, want symlink error", err)
	}
}

func TestSandboxRunShadowsProjectDirWithoutCreatingIt(t *testing.T) {
	logPath := fakeDockerProxy(t, nil, nil)
	t.Setenv("TMPDIR", t.TempDir())
	workdir := t.TempDir()

	s := newSandbox(Options{Sandbox: true}, filepath.Join(t.TempDir(), "absent.jsonc"))
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatal(err)
	}
	s.image, s.proxyImage = "rnes-sandbox-default:test", "rnes-sandbox-proxy:test"
	if err := s.Run(workdir, "llama-cpp", "m"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(workdir, ".rnes")); !os.IsNotExist(err) {
		t.Errorf("Run must not create .rnes, got err=%v", err)
	}

	run := sandboxRun(t, logPath)
	if !strings.Contains(run, "--tmpfs /workspace/.rnes:ro") {
		t.Errorf("sandbox run did not shadow .rnes with a read-only tmpfs:\n%s", run)
	}
	if strings.Contains(run, "source="+filepath.Join(workdir, ".rnes")+",target=/workspace/.rnes") {
		t.Errorf("sandbox run must not bind-mount the host .rnes directory:\n%s", run)
	}
}

func TestSandboxRunMountsPolicyFileReadOnly(t *testing.T) {
	logPath := fakeDockerProxy(t, nil, nil)
	t.Setenv("TMPDIR", t.TempDir())
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".rnes"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(workdir, ".rnes", "policy.txt")
	if err := os.WriteFile(policy, []byte("pypi.org:443\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newSandbox(Options{Sandbox: true, Policy: []byte("pypi.org:443\n")}, filepath.Join(t.TempDir(), "absent.jsonc"))
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatal(err)
	}
	s.image, s.proxyImage = "rnes-sandbox-default:test", "rnes-sandbox-proxy:test"
	if err := s.Run(workdir, "llama-cpp", "m"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run := sandboxRun(t, logPath)
	tmpfs := "--tmpfs /workspace/.rnes:ro"
	policyMount := "source=" + policy + ",target=/workspace/.rnes/policy.txt,readonly"
	for _, want := range []string{tmpfs, policyMount} {
		if !strings.Contains(run, want) {
			t.Errorf("sandbox run missing %q:\n%s", want, run)
		}
	}
	// The tmpfs has to be mounted first; the policy file lands inside it.
	if strings.Index(run, tmpfs) > strings.Index(run, policyMount) {
		t.Errorf("sandbox run mounts policy.txt before the tmpfs that holds it:\n%s", run)
	}
}

// sandboxRun returns the sandbox's docker run command from the fake docker log.
func sandboxRun(t *testing.T, logPath string) string {
	t.Helper()
	for _, c := range calls(t, logPath) {
		if strings.HasPrefix(c, "run --rm") {
			return c
		}
	}
	t.Fatalf("no sandbox run command in %v", calls(t, logPath))
	return ""
}

func TestSandboxRunRefusesSymlinkProjectDir(t *testing.T) {
	logPath := fakeDockerProxy(t, nil, nil)
	workdir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(workdir, ".rnes")); err != nil {
		t.Fatal(err)
	}

	s := newSandbox(Options{Sandbox: true}, filepath.Join(t.TempDir(), "absent.jsonc"))
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatal(err)
	}
	s.image, s.proxyImage = "rnes-sandbox-default:test", "rnes-sandbox-proxy:test"
	if err := s.Run(workdir, "llama-cpp", "m"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Run with symlink .rnes = %v, want symlink error", err)
	}
	if got := calls(t, logPath); len(got) != 0 {
		t.Errorf("docker must not be called when .rnes is a symlink, got %v", got)
	}
}

func TestBlockedSummary(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "proxy.log")
	const logText = `2026/10/28 21:00:00 rnes-proxy: listening on [::]:8888
2026/10/28 21:00:01 rnes-proxy: GET https://github.com/ -> 200 (40ms)
2026/10/28 21:00:02 rnes-proxy: denied pypi.org:443
2026/10/28 21:00:03 rnes-proxy: denied 169.254.169.254:80
2026/10/28 21:00:04 rnes-proxy: denied pypi.org:443
`
	if err := os.WriteFile(logFile, []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}
	got := BlockedSummary(logFile)
	want := "169.254.169.254:80 (1x), pypi.org:443 (2x)"
	if got != want {
		t.Errorf("BlockedSummary = %q, want %q", got, want)
	}

	cleanFile := filepath.Join(t.TempDir(), "clean.log")
	if err := os.WriteFile(cleanFile, []byte("2026/10/28 21:00:01 rnes-proxy: GET https://github.com/ -> 200 (10ms)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BlockedSummary(cleanFile); got != "" {
		t.Errorf("clean log summary = %q, want empty", got)
	}
}
