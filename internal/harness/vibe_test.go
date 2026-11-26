package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/skills"
)

func TestNewVibeRunsOnTheHostAndInTheSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBE_HOME", filepath.Join(home, "custom-vibe"))

	h, err := New("vibe", Options{})
	if err != nil {
		t.Fatalf("New(vibe): %v", err)
	}
	v, ok := h.(Vibe)
	if !ok {
		t.Fatalf("expected a Vibe harness, got %T", h)
	}
	if want := filepath.Join(home, "custom-vibe", "config.toml"); v.configPath != want {
		t.Errorf("configPath = %q, want %q (VIBE_HOME must be honored)", v.configPath, want)
	}
	if !NeedsModelServer(h) {
		t.Error("vibe must use the local llama-server")
	}

	// Without VIBE_HOME the config is Vibe's own default location.
	t.Setenv("VIBE_HOME", "")
	h, err = New("vibe", Options{})
	if err != nil {
		t.Fatalf("New(vibe): %v", err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(userHome, ".vibe", "config.toml"); h.(Vibe).configPath != want {
		t.Errorf("configPath = %q, want %q", h.(Vibe).configPath, want)
	}

	s, err := New("vibe", Options{Sandbox: true})
	if err != nil {
		t.Fatalf("New(vibe, sandbox): %v", err)
	}
	sb, ok := s.(*Sandbox)
	if !ok {
		t.Fatalf("expected a *Sandbox harness, got %T", s)
	}
	if sb.profile.harness != "vibe" {
		t.Errorf("profile harness = %q, want vibe", sb.profile.harness)
	}
	if !NeedsModelServer(s) {
		t.Error("the vibe sandbox must use the local llama-server")
	}
}

func TestVibeSandboxConfigureWritesALocalProviderConfig(t *testing.T) {
	s := newSandboxWithProfile(Options{Sandbox: true}, vibeProfile())
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m-one", "m-two"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { os.Remove(s.generated) })

	raw, err := os.ReadFile(s.generated)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		// The container reaches the host's llama-server through the proxy.
		`api_base = "http://host.docker.internal:8080/v1"`,
		`name = "rnes-llamacpp"`,
		`alias = "m-one"`,
		`alias = "m-two"`,
		`active_model = "m-one"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated config missing %q:\n%s", want, got)
		}
	}
	if s.modelServer != "host.docker.internal:8080" {
		t.Errorf("modelServer = %q, want host.docker.internal:8080", s.modelServer)
	}

	// Run's model choice lands in the config's default, not in a flag.
	if err := s.applyModel("llama-cpp", "m-two"); err != nil {
		t.Fatalf("applyModel: %v", err)
	}
	raw, err = os.ReadFile(s.generated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `active_model = "m-two"`) {
		t.Errorf("applyModel did not set the default model:\n%s", raw)
	}
}

func TestVibeSandboxRunArgsMountTheStagedHomeAndTrustTheWorkspace(t *testing.T) {
	workdir := t.TempDir()
	skillDir := t.TempDir()
	pn := &proxyNet{network: "rnes-net-1", container: "rnes-proxy-1", caVolume: "rnes-ca-1"}

	h, err := New("vibe", Options{
		Sandbox: true,
		Skills:  []skills.Skill{{Name: "my-skill", Dir: skillDir}},
		Args:    []string{"--continue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := h.(*Sandbox)
	s.image = "rnes-sandbox-default:vibe1"
	s.configHome = "/tmp/staged-vibe-home"
	s.mistralPlaceholder = "rnes_placeholder_mistral"

	joined := strings.Join(s.runArgsForModel("rnes-1", workdir, pn, false, "m-one"), " ")
	for _, want := range []string{
		// The staged home is Vibe's $HOME/.vibe: it carries the generated
		// config.toml and the attached skills.
		"--mount type=bind,source=/tmp/staged-vibe-home,target=/home/rnes/.vibe ",
		"--mount type=bind,source=" + skillDir + ",target=/home/rnes/.vibe/skills/my-skill,readonly ",
		"-e MISTRAL_API_KEY=rnes_placeholder_mistral",
		"rnes-sandbox-default:vibe1 --trust --continue",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("vibe runArgs missing %q:\n%s", want, joined)
		}
	}
	// Vibe reads its config from its own home, so opencode's config mount and
	// variable must not appear.
	for _, bad := range []string{"OPENCODE_CONFIG", "/etc/rnes/opencode.jsonc", "opencode.jsonc"} {
		if strings.Contains(joined, bad) {
			t.Errorf("vibe runArgs must not contain %q:\n%s", bad, joined)
		}
	}
}

func TestVibeSandboxRunStagesTheHomeEvenWithoutSkills(t *testing.T) {
	logPath := fakeDockerProxy(t, nil, nil)
	t.Setenv("TMPDIR", t.TempDir())
	workdir := t.TempDir()

	h, err := New("vibe", Options{Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	s := h.(*Sandbox)
	s.image, s.proxyImage = "rnes-sandbox-default:vibetest", "rnes-sandbox-proxy:test"
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m-one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(workdir, "llama-cpp", "m-one"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var staged string
	for _, c := range calls(t, logPath) {
		if !strings.HasPrefix(c, "run --rm") {
			continue
		}
		for _, f := range strings.Fields(c) {
			if strings.HasPrefix(f, "type=bind,source=") && strings.HasSuffix(f, ",target=/home/rnes/.vibe") {
				staged = strings.TrimSuffix(strings.TrimPrefix(f, "type=bind,source="), ",target=/home/rnes/.vibe")
			}
		}
		if !strings.HasSuffix(c, "rnes-sandbox-default:vibetest --trust") {
			t.Errorf("run call missing the image and --trust: %s", c)
		}
	}
	if staged == "" || s.configHome != staged {
		t.Fatalf("no ~/.vibe staging mount found (configHome %q, staged %q)", s.configHome, staged)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("the staging directory must be removed after Run, stat err = %v", err)
	}
}

func TestVibeWriteConfigInHomePutsTheGeneratedConfigWhereVibeReadsIt(t *testing.T) {
	s := newSandboxWithProfile(Options{Sandbox: true}, vibeProfile())
	if err := s.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m-one"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(s.generated) })

	home := t.TempDir()
	if err := s.writeConfigInHome(home); err != nil {
		t.Fatalf("writeConfigInHome: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml not written into the staged home: %v", err)
	}
	if !strings.Contains(string(raw), `name = "rnes-llamacpp"`) {
		t.Errorf("staged config.toml is not the generated one:\n%s", raw)
	}
}

func TestVibeHostConfigureMergesIntoTheVibeHomeConfig(t *testing.T) {
	vibeHome := t.TempDir()
	t.Setenv("VIBE_HOME", vibeHome)
	configPath := filepath.Join(vibeHome, "config.toml")
	original := "theme = \"auto\"\nactive_model = \"mine\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := New("vibe", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Configure("llama-cpp", "http://127.0.0.1:8080", []string{"m-one"}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.HasPrefix(got, original) {
		t.Errorf("the user's own settings were not preserved:\n%s", got)
	}
	// On the host the session keeps the user's model: the local one is one
	// /model selection away.
	if !strings.Contains(got, `active_model = "mine"`) {
		t.Errorf("Configure rewrote the user's active_model:\n%s", got)
	}
	if !strings.Contains(got, `api_base = "http://127.0.0.1:8080/v1"`) {
		t.Errorf("Configure did not add the local provider:\n%s", got)
	}
}

func TestVibeHostRunForwardsArgsAndRunsInTheProject(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vibe.log")
	script := "#!/bin/sh\necho \"$*\" >> " + logPath + "\npwd >> " + logPath + "\n"
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "vibe"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	project := t.TempDir()
	h, err := New("vibe", Options{Args: []string{"--continue"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Available(); err != nil {
		t.Fatalf("Available: %v", err)
	}
	if err := h.Run(project, "llama-cpp", "m-one"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 4 {
		t.Fatalf("vibe log = %q, want the argv and working directory of both calls", lines)
	}
	// Available's --version call comes first, then the session's.
	if lines[0] != "--version" {
		t.Errorf("first call = %q, want --version", lines[0])
	}
	if lines[2] != "--continue" {
		t.Errorf("run argv = %q, want --continue", lines[2])
	}
	if real, _ := filepath.EvalSymlinks(project); lines[3] != project && lines[3] != real {
		t.Errorf("vibe ran in %q, want the project %q", lines[3], project)
	}
}

func TestVibeHostAvailableReportsAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h, err := New("vibe", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Available(); err == nil || !strings.Contains(err.Error(), "vibe not found on PATH") {
		t.Fatalf("Available() = %v, want a not-found error", err)
	}
}
