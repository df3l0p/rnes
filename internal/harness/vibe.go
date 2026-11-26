package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	vibeHome := os.Getenv("VIBE_HOME")
	if vibeHome == "" {
		vibeHome = filepath.Join(home, ".vibe")
	}
	configPath := filepath.Join(vibeHome, "config.toml")
	if opts.Sandbox {
		return newSandboxWithProfile(opts, vibeProfile()), nil
	}
	return Vibe{configPath: configPath, args: opts.Args}, nil
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
		stageConfigHome:     true,
		configInHome:        "config.toml",
		skillsConfigHome:    containerVibeHome,
		skillsRelDir:        "skills",
		renderConfig: func(_ *Sandbox, _, _ string, baseURL string, modelIDs []string) ([]byte, error) {
			return vibeconfig.Render(baseURL, modelIDs), nil
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
	if out, err := exec.Command("vibe", "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("running vibe --version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (v Vibe) Configure(providerKey, baseURL string, modelIDs []string) error {
	return vibeconfig.Merge(v.configPath, baseURL, modelIDs)
}

func (v Vibe) Run(dir, providerKey, modelID string) error {
	// The interactive TUI takes no model flag (--model is headless-only), and
	// rewriting the user's active_model would outlive the session. So on the
	// host the session starts on whatever model Vibe would pick anyway, and
	// the llama-server models are one /model selection away. providerKey and
	// modelID are only used by the sandbox, which writes them into its
	// session-local config.
	cmd := exec.Command("vibe", v.args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
