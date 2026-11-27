package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/df3l0p/rnes/internal/vibeconfig"
)

const (
	// containerVibeHome is Vibe's home inside the sandbox ($HOME/.vibe): the
	// staged directory holding the session's generated config.toml and the
	// attached skills (<containerVibeHome>/skills/<name>). Vibe reads
	// $VIBE_HOME, which defaults to $HOME/.vibe, so no variable is needed.
	containerVibeHome = containerHome + "/.vibe"
)

// Vibe drives the Mistral Vibe CLI (github.com/mistralai/mistral-vibe) as a
// Harness. Its config path follows VIBE_HOME, so rnes merges into the same
// config.toml Vibe itself would read.
type Vibe struct {
	configPath string
	args       []string
}

func newVibe(opts Options) (Harness, error) {
	if opts.Sandbox {
		return newSandboxWithProfile(opts, vibeProfile()), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	vibeHome := os.Getenv("VIBE_HOME")
	if vibeHome == "" {
		vibeHome = filepath.Join(home, ".vibe")
	}
	return Vibe{configPath: filepath.Join(vibeHome, "config.toml"), args: opts.Args}, nil
}

// vibeProfile is the sandbox side of the harness. Unlike opencode's, its
// generated config is built from scratch (see vibeconfig.Render) rather than
// copied from the host: Vibe's config.toml can hold literal secrets (an MCP
// server's static header, a provider's extra_headers), and none of them may
// reach the container. The cost is that the user's own providers, models and
// MCP servers are not available in the sandbox; the local llama-server models
// and, with RNES_MISTRAL_API_KEY set, Mistral's are.
func vibeProfile() sandboxProfile {
	return sandboxProfile{
		harness:             "vibe",
		needsModelServer:    true,
		usesGeneratedConfig: true,
		mistralEnv:          "MISTRAL_API_KEY",
		configInHome:        "config.toml",
		skillsConfigHome:    containerVibeHome,
		skillsRelDir:        "skills",
		renderConfig: func(_ *Sandbox, _, _ string, baseURL string, modelIDs []string) ([]byte, error) {
			return vibeconfig.Render(baseURL, modelIDs)
		},
		setModel: func(raw []byte, _, modelID string) ([]byte, error) {
			return vibeconfig.SetModel(raw, modelID)
		},
		containerArgs: func(_ string, _ bool, extraArgs []string) []string {
			// --trust: the workspace is the directory the user chose, and the
			// staged home carries no trust database, so without it every
			// session would stop at Vibe's trust prompt. The project's own
			// .vibe config and hooks then load, inside the sandbox.
			return append([]string{"--trust"}, extraArgs...)
		},
	}
}

func (v Vibe) Available() error {
	if _, err := exec.LookPath("vibe"); err != nil {
		return fmt.Errorf("vibe not found on PATH: %w", err)
	}
	out, err := exec.Command("vibe", "--version").Output()
	if err != nil {
		return fmt.Errorf("running vibe --version: %w", err)
	}
	return vibeIsSupported(string(out))
}

// vibeIsSupported reports an error unless out (the output of vibe --version)
// names Vibe 2 or newer. The generated sandbox config sets settings that older
// releases don't have (enable_auto_update arrived in 2.0.0), which they would
// report as unknown on every session, and the [[providers]]/[[models]] shape
// rnes merges into the host config is the same one.
func vibeIsSupported(out string) error {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("couldn't read a version from %q", strings.TrimSpace(out))
	}
	if major, err := strconv.Atoi(m[1]); err != nil || major < 2 {
		return fmt.Errorf("vibe %s is too old: rnes needs Vibe 2 or newer (upgrade with `uv tool upgrade mistral-vibe`)", strings.TrimSpace(out))
	}
	return nil
}

func (v Vibe) Configure(providerKey, baseURL string, modelIDs []string) error {
	return vibeconfig.Merge(v.configPath, baseURL, modelIDs)
}

func (v Vibe) Run(dir, providerKey, modelID string) error {
	// The interactive TUI takes no model flag (--model is headless-only), and
	// rewriting the user's active_model would outlive the session. Vibe's
	// environment layer outranks config.toml, so VIBE_ACTIVE_MODEL pins the
	// session's model without touching the file. Without it the session would
	// start on Vibe's own default, a cloud model, while rnes serves a local
	// one. providerKey is only used by the sandbox, which writes it into its
	// session-local config.
	cmd := exec.Command("vibe", v.args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), "VIBE_ACTIVE_MODEL", modelID)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
