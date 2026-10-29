package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/harness"
)

func TestValidateRejectsSandboxOnlyFlagsWithoutSandbox(t *testing.T) {
	for _, cfg := range []cliConfig{{image: "x"}, {build: true}, {allNet: true}, {noInspect: true}} {
		if err := cfg.validate(); err == nil {
			t.Errorf("validate(%+v) = nil, want an error", cfg)
		}
	}
	for _, cfg := range []cliConfig{{}, {sandbox: true}, {sandboxUpdate: true}, {sandbox: true, image: "x", build: true, allNet: true, noInspect: true}} {
		if err := cfg.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", cfg, err)
		}
	}
}

// TestSandboxUpdateImpliesSandboxAndBuild checks that -sandbox-update turns
// into a from-scratch sandbox rebuild, so prepare runs and validate passes
// for the sandbox-only flags given alongside it.
func TestSandboxUpdateImpliesSandboxAndBuild(t *testing.T) {
	c := cliConfig{sandboxUpdate: true, image: "x"}
	c.applyImplications()
	if !c.sandbox || !c.build {
		t.Fatalf("applyImplications = sandbox=%v build=%v, want both true", c.sandbox, c.build)
	}
	if err := c.validate(); err != nil {
		t.Errorf("validate(%+v) = %v, want nil", c, err)
	}
	c2 := cliConfig{}
	c2.applyImplications()
	if c2.sandbox || c2.build {
		t.Errorf("applyImplications without -sandbox-update = sandbox=%v build=%v, want both false", c2.sandbox, c2.build)
	}
}

// fakeBinder is a harness that picks its own listen address.
type fakeBinder struct {
	harness.Harness
	host string
	err  error
}

func (f fakeBinder) BindHost() (string, error) { return f.host, f.err }

func TestListenHost(t *testing.T) {
	var plain harness.Harness // doesn't implement BindHoster
	tests := []struct {
		name    string
		h       harness.Harness
		host    string
		hostSet bool
		want    string
		wantErr bool
	}{
		{"explicit default host is not narrowed", fakeBinder{host: "172.17.0.1"}, defaultHost, true, defaultHost, false},
		{"host harness keeps the default", plain, defaultHost, false, defaultHost, false},
		{"container harness narrows the default", fakeBinder{host: "172.17.0.1"}, defaultHost, false, "172.17.0.1", false},
		{"container harness error is returned", fakeBinder{err: errors.New("boom")}, defaultHost, false, "", true},
		{"explicit host wins over the harness", fakeBinder{host: "172.17.0.1"}, "192.168.1.5", true, "192.168.1.5", false},
		{"explicit wildcard is honoured", fakeBinder{host: "172.17.0.1"}, "0.0.0.0", true, "0.0.0.0", false},
	}
	for _, tt := range tests {
		got, err := listenHost(tt.h, tt.host, tt.hostSet)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s: listenHost = (%q, %v), want (%q, err=%v)", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestGitHubTokenFromEnvReadsAndUnsets(t *testing.T) {
	t.Setenv("RNES_GITHUB_TOKEN", "github_pat_x")
	if got := githubTokenFromEnv(); got != "github_pat_x" {
		t.Fatalf("token = %q", got)
	}
	if v, ok := os.LookupEnv("RNES_GITHUB_TOKEN"); ok {
		t.Errorf("RNES_GITHUB_TOKEN is still set (%q): child processes would inherit it", v)
	}
}

func TestValidateRejectsAGitHubTokenWithNoInspect(t *testing.T) {
	c := cliConfig{sandbox: true, noInspect: true, githubToken: "github_pat_x"}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "-no-inspect") {
		t.Errorf("err = %v, want one naming -no-inspect", err)
	}
}

func TestValidateRejectsAnUnusableGitHubToken(t *testing.T) {
	c := cliConfig{sandbox: true, githubToken: "github_pat_x\n"}
	err := c.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_GITHUB_TOKEN") || strings.Contains(err.Error(), "github_pat_x") {
		t.Errorf("err = %v, want one naming RNES_GITHUB_TOKEN and not echoing it", err)
	}
}

func TestValidateAcceptsAGitHubTokenWithSandbox(t *testing.T) {
	if err := (cliConfig{sandbox: true, githubToken: "github_pat_x"}).validate(); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestUsageDocumentsTheGitHubTokenAndTheGhShortcut(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, want := range []string{"Environment:", "RNES_GITHUB_TOKEN", "fine-grained", "export RNES_GITHUB_TOKEN=$(gh auth token)", "-sandbox"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, buf.String())
		}
	}
}

func TestTokenWarningFlagsAnythingButAFineGrainedToken(t *testing.T) {
	for _, tok := range []string{"ghp_abc", "gho_abc", "ghu_abc", "ghs_abc", "v1.0123456789abcdef"} {
		if tokenWarning(tok) == "" {
			t.Errorf("tokenWarning(%q) is empty, want a warning", tok)
		}
	}
	if w := tokenWarning("github_pat_abc"); w != "" {
		t.Errorf("tokenWarning(fine-grained) = %q, want none", w)
	}
}

// TestSessionCredentialIsKeptOnlyForASession checks that a credential is
// dropped when no session will use it: without -sandbox, and with
// -sandbox-update, which exits before one starts.
func TestSessionCredentialIsKeptOnlyForASession(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  cliConfig
		want string
	}{
		{"no sandbox", cliConfig{}, ""},
		{"sandbox", cliConfig{sandbox: true}, "tok"},
		{"sandbox-update", cliConfig{sandbox: true, sandboxUpdate: true}, ""},
	} {
		if got := tc.cfg.sessionCredential("RNES_X", "tok"); got != tc.want {
			t.Errorf("%s: credential = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOpenRouterKeyFromEnvReadsAndUnsets(t *testing.T) {
	t.Setenv("RNES_OPENROUTER_API_KEY", "sk-or-x")
	if got := openRouterKeyFromEnv(); got != "sk-or-x" {
		t.Errorf("got %q", got)
	}
	if v, ok := os.LookupEnv("RNES_OPENROUTER_API_KEY"); ok {
		t.Errorf("RNES_OPENROUTER_API_KEY is still set (%q): child processes would inherit it", v)
	}
}

func TestValidateRejectsAnOpenRouterKeyWithNoInspect(t *testing.T) {
	err := cliConfig{sandbox: true, noInspect: true, openrouterKey: "sk-or-x"}.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_OPENROUTER_API_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateRejectsAnUnusableOpenRouterKey(t *testing.T) {
	err := cliConfig{sandbox: true, openrouterKey: "sk-or-x\n"}.validate()
	if err == nil || !strings.Contains(err.Error(), "RNES_OPENROUTER_API_KEY") || strings.Contains(err.Error(), "sk-or-x") {
		t.Errorf("err = %v, want one naming RNES_OPENROUTER_API_KEY and not echoing it", err)
	}
}

func TestValidateIgnoresTheOpenRouterKeyForACloudHarness(t *testing.T) {
	// agy never uses the OpenRouter provider, so even an unusable key or one
	// combined with -no-inspect must not block the session (it is dropped with
	// a note at startup).
	for _, cfg := range []cliConfig{
		{sandbox: true, harness: "agy", openrouterKey: "sk-or-x"},
		{sandbox: true, harness: "agy", noInspect: true, openrouterKey: "sk-or-x"},
		{sandbox: true, harness: "agy", openrouterKey: "sk-or-x\n"},
	} {
		if err := cfg.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", cfg, err)
		}
	}
	// A model-server harness still validates it.
	if err := (cliConfig{sandbox: true, harness: "opencode", noInspect: true, openrouterKey: "sk-or-x"}).validate(); err == nil {
		t.Error("opencode with -no-inspect and an OpenRouter key must still be rejected")
	}
}

func TestActiveProvidersListsOnlyTheSetOnes(t *testing.T) {
	if got := activeProviders(cliConfig{}); len(got) != 0 {
		t.Errorf("none set: %v", got)
	}
	got := activeProviders(cliConfig{githubToken: "a", openrouterKey: "b"})
	if strings.Join(got, ",") != "github,openrouter" {
		t.Errorf("both set: %v", got)
	}
}

func TestUsageDocumentsTheOpenRouterKey(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, want := range []string{"RNES_OPENROUTER_API_KEY", "credit limit", "-sandbox"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, buf.String())
		}
	}
}

func TestValidateRequiresSandboxForSkillFlags(t *testing.T) {
	for _, cfg := range []cliConfig{{skills: stringList{"foo"}}, {skillsDirs: stringList{"/x"}}} {
		err := cfg.validate()
		if err == nil || !strings.Contains(err.Error(), "-skill") || !strings.Contains(err.Error(), "require -sandbox") {
			t.Errorf("validate(%+v) = %v, want a requires -sandbox error naming -skill", cfg, err)
		}
	}
	if err := (cliConfig{sandbox: true, skills: stringList{"foo"}, skillsDirs: stringList{"/x"}}).validate(); err != nil {
		t.Errorf("with -sandbox: %v", err)
	}
}

func TestSandboxUpdateDropsTheSkillFlags(t *testing.T) {
	c := cliConfig{sandboxUpdate: true, skills: stringList{"foo"}, skillsDirs: stringList{"/x"}}
	c.applyImplications()
	c.dropSkillsForUpdate()
	if len(c.skills) != 0 || len(c.skillsDirs) != 0 {
		t.Errorf("skills = %v, dirs = %v; want both dropped", c.skills, c.skillsDirs)
	}
	if err := c.validate(); err != nil {
		t.Errorf("validate = %v", err)
	}
	kept := cliConfig{sandbox: true, skills: stringList{"foo"}}
	kept.dropSkillsForUpdate()
	if len(kept.skills) != 1 {
		t.Errorf("a session's -skill was dropped: %v", kept.skills)
	}
}

func TestSkillFlagsAreRepeatable(t *testing.T) {
	var l stringList
	for _, v := range []string{"a", "all", "~/b"} {
		if err := l.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(l, " ") != "a all ~/b" {
		t.Errorf("l = %v", l)
	}
}

func TestResolveSkillsAttachesNothingWithoutFlags(t *testing.T) {
	var buf bytes.Buffer
	// A home that doesn't exist would make any lookup fail: none must happen.
	got, err := resolveSkills(cliConfig{sandbox: true}, "/nonexistent-home", "/nonexistent-cwd", &buf)
	if err != nil || got != nil || buf.Len() != 0 {
		t.Errorf("got %v, %v, output %q", got, err, buf.String())
	}
}

func TestResolveSkillsPrintsWarningsAndResolves(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, d := range []string{
		filepath.Join(home, ".config", "opencode", "skills", "foo"),
		filepath.Join(home, ".claude", "skills", "foo"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: foo\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := resolveSkills(cliConfig{sandbox: true, skills: stringList{"foo"}}, home, cwd, &buf); err == nil {
		t.Errorf("an ambiguous -skill must fail")
	}
	got, err := resolveSkills(cliConfig{sandbox: true, skills: stringList{"all"}}, home, cwd, &buf)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if !strings.Contains(buf.String(), "rnes: warning:") || !strings.Contains(buf.String(), "shadowing") {
		t.Errorf("output = %q", buf.String())
	}

	buf.Reset()
	if _, err := resolveSkills(cliConfig{sandbox: true, skillsDirs: stringList{filepath.Join(home, "absent")}}, home, cwd, &buf); err == nil {
		t.Errorf("a missing -skills-dir must fail")
	}
	if _, err := resolveSkills(cliConfig{sandbox: true, skillsDirs: stringList{home}}, home, cwd, &buf); err != nil || !strings.Contains(buf.String(), "nothing is attached") {
		t.Errorf("-skills-dir alone: %v, output %q", err, buf.String())
	}
}

func TestValidateRejectsGeneratePolicyWithSandboxUpdate(t *testing.T) {
	if err := (cliConfig{generatePolicy: true}).validate(); err != nil {
		t.Errorf("generatePolicy alone: %v", err)
	}
	if err := (cliConfig{generatePolicy: true, sandboxUpdate: true, sandbox: true, build: true}).validate(); err == nil {
		t.Error("expected validate error when combining -generate-policy and -sandbox-update")
	}
	if got := (cliConfig{generatePolicy: true}).sessionCredential("RNES_GITHUB_TOKEN", "github_pat_x"); got != "" {
		t.Errorf("sessionCredential with -generate-policy = %q, want empty", got)
	}
}

func TestResolvePolicyHandlesHostAndSandbox(t *testing.T) {
	// Without .rnes/policy.txt: sandbox creates .rnes/ and returns nil policy.
	cwd := t.TempDir()
	var buf bytes.Buffer
	cfg := cliConfig{sandbox: true, githubToken: "github_pat_x"}
	raw, err := resolvePolicy(cfg, cwd, &buf)
	if err != nil || raw != nil || buf.Len() != 0 {
		t.Fatalf("no policy file = (raw=%v, err=%v, out=%q)", raw, err, buf.String())
	}
	if fi, err := os.Lstat(filepath.Join(cwd, ".rnes")); err != nil || !fi.IsDir() {
		t.Fatalf("expected .rnes dir created for sandbox, fi=%v err=%v", fi, err)
	}

	// Generated starter policy prints active hosts.
	if _, err := harness.GeneratePolicy(cwd); err != nil {
		t.Fatal(err)
	}
	raw, err = resolvePolicy(cfg, cwd, &buf)
	if err != nil || raw == nil {
		t.Fatalf("generated policy = (rawNil=%v, err=%v)", raw == nil, err)
	}
	if !strings.Contains(buf.String(), "rnes: sandbox policy (.rnes/policy.txt):") {
		t.Errorf("startup output missing policy line: %q", buf.String())
	}

	// Host mode (no -sandbox) prints a note and leaves .rnes untouched.
	buf.Reset()
	raw, err = resolvePolicy(cliConfig{}, cwd, &buf)
	if err != nil || raw != nil || !strings.Contains(buf.String(), "ignored without -sandbox") {
		t.Errorf("host mode with policy file = (raw=%v, err=%v, out=%q)", raw, err, buf.String())
	}

	// Custom policy prints only the configured hosts (the proxy blocks any
	// omitted provider hosts on its own, without per-provider checks in rnes).
	buf.Reset()
	if err := os.WriteFile(filepath.Join(cwd, ".rnes", "policy.txt"), []byte("pypi.org:443\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err = resolvePolicy(cfg, cwd, &buf)
	if err != nil || string(raw) != "pypi.org:443\n" {
		t.Fatalf("pypi policy = (%q, %v)", raw, err)
	}
	if !strings.Contains(buf.String(), "pypi.org:443") || strings.Contains(buf.String(), "warning:") {
		t.Errorf("output = %q", buf.String())
	}

	// Empty policy prints "none (model server only)".
	buf.Reset()
	if err := os.WriteFile(filepath.Join(cwd, ".rnes", "policy.txt"), []byte("# model server only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err = resolvePolicy(cliConfig{sandbox: true}, cwd, &buf)
	if err != nil || raw == nil || !strings.Contains(buf.String(), "none (model server only)") {
		t.Errorf("empty policy = (rawNil=%v, err=%v, out=%q)", raw == nil, err, buf.String())
	}

	// A cloud harness has no model server to add, so the empty-policy line
	// describes its own endpoints instead.
	buf.Reset()
	if _, err := resolvePolicy(cliConfig{sandbox: true, harness: "agy"}, cwd, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "none (harness endpoints only)") {
		t.Errorf("empty agy policy line = %q, want none (harness endpoints only)", buf.String())
	}

	// no-inspect entries are marked in the startup line.
	buf.Reset()
	if err := os.WriteFile(filepath.Join(cwd, ".rnes", "policy.txt"), []byte("no-inspect example.com:443\ngithub.com:443\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePolicy(cliConfig{sandbox: true}, cwd, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "example.com:443 (no-inspect)") || !strings.Contains(buf.String(), "github.com:443") {
		t.Errorf("startup line = %q, want example.com:443 (no-inspect) and plain github.com:443", buf.String())
	}
}

func TestCheckModelServerFlagsRejectsHostAndPortForCloudHarnesses(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cfg              cliConfig
		needsModelServer bool
		want             string
	}{
		{"model server accepts -host and -port", cliConfig{harness: "opencode", hostSet: true, portSet: true}, true, ""},
		{"cloud harness accepts neither flag being set", cliConfig{harness: "agy"}, false, ""},
		{"cloud harness rejects -host", cliConfig{harness: "agy", hostSet: true}, false, "-host cannot be used with -harness agy"},
		{"cloud harness rejects -port", cliConfig{harness: "agy", portSet: true}, false, "-port cannot be used with -harness agy"},
		{"cloud harness rejects both", cliConfig{harness: "agy", hostSet: true, portSet: true}, false, "-host and -port cannot be used with -harness agy"},
	} {
		err := checkModelServerFlags(tc.cfg, tc.needsModelServer)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

func TestHarnessOptionsCarryModelSetAndTrailingArgs(t *testing.T) {
	cfg := cliConfig{
		sandbox:  true,
		modelSet: true,
		args:     []string{"--continue", "--mode", "plan"},
	}
	opts := harnessOptions(cfg, nil, nil)
	if !opts.ModelSet || strings.Join(opts.Args, " ") != "--continue --mode plan" {
		t.Errorf("options = ModelSet=%v Args=%v, want true and the trailing args", opts.ModelSet, opts.Args)
	}
	if !opts.Sandbox {
		t.Error("Sandbox was not carried over")
	}
	if plain := harnessOptions(cliConfig{}, nil, nil); plain.ModelSet || len(plain.Args) != 0 {
		t.Errorf("default options = ModelSet=%v Args=%v, want false and none", plain.ModelSet, plain.Args)
	}
}

func TestRebuildAllHarnessesOnlyWithoutExplicitHarness(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  cliConfig
		want bool
	}{
		{"not updating", cliConfig{}, false},
		{"update without -harness rebuilds all", cliConfig{sandboxUpdate: true}, true},
		{"update with -harness is scoped", cliConfig{sandboxUpdate: true, harnessSet: true}, false},
	} {
		if got := rebuildAllHarnesses(tc.cfg); got != tc.want {
			t.Errorf("%s: rebuildAllHarnesses = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveSkillsPrefersTheActiveHarnessGlobalFolder(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	agyDir := filepath.Join(home, ".gemini", "config", "skills", "shared")
	ocDir := filepath.Join(home, ".config", "opencode", "skills", "shared")
	for _, d := range []string{agyDir, ocDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: shared\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		harness string
		want    string
	}{
		{"agy", filepath.Join(".gemini", "config", "skills", "shared")},
		{"opencode", filepath.Join(".config", "opencode", "skills", "shared")},
	} {
		var buf bytes.Buffer
		got, err := resolveSkills(cliConfig{sandbox: true, harness: tc.harness, skills: stringList{"all"}}, home, cwd, &buf)
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: got %v, %v", tc.harness, got, err)
		}
		if !strings.Contains(got[0].Dir, tc.want) {
			t.Errorf("%s: attached %q, want the %s copy", tc.harness, got[0].Dir, tc.want)
		}
		if !strings.Contains(buf.String(), "shadowing") {
			t.Errorf("%s: output = %q, want a shadowing warning", tc.harness, buf.String())
		}
	}
}
