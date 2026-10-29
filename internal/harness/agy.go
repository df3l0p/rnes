package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

const (
	// containerAgyAuthDir is where agy stores its OAuth credentials,
	// settings.json, and brain/ transcripts inside the sandbox.
	containerAgyAuthDir = containerHome + "/.gemini/antigravity-cli"
	// containerAgyConfigHome is where agy reads global skills
	// (<containerAgyConfigHome>/skills/<name>), kept as a sibling of
	// containerAgyAuthDir so skills mounts never touch the auth volume.
	containerAgyConfigHome = containerHome + "/.gemini/config"
)

// agyExtraPolicy lists the Google OAuth and Antigravity endpoints agy reaches
// directly over HTTP/2 and gRPC. They are marked no-inspect so TLS is
// tunnelled untouched while the rest of the policy (e.g. github.com) remains
// TLS-inspected and credential-injected.
const agyExtraPolicy = `# Google Antigravity OAuth & API endpoints (HTTP/2 & gRPC passthrough)
no-inspect accounts.google.com:443
no-inspect oauth2.googleapis.com:443
no-inspect www.googleapis.com:443
no-inspect antigravity.google:443
no-inspect antigravity.googleapis.com:443
no-inspect antigravity-pa.googleapis.com:443
no-inspect antigravity-unleash.goog:443
no-inspect cloudcode-pa.googleapis.com:443
no-inspect generativelanguage.googleapis.com:443
no-inspect businessaicode.googleapis.com:443
no-inspect aiplatform.googleapis.com:443
`

func newAgy(opts Options) (Harness, error) {
	if !opts.Sandbox {
		return nil, fmt.Errorf("-harness agy requires -sandbox")
	}
	return newSandboxWithProfile(opts, agyProfile()), nil
}

func agyProfile() sandboxProfile {
	return sandboxProfile{
		harness:             "agy",
		needsModelServer:    false,
		usesGeneratedConfig: false,
		extraPolicy:         agyExtraPolicy,
		authVolume:          agyAuthVolume,
		authLabel:           "rnes.auth=agy",
		authMountTarget:     containerAgyAuthDir,
		skillsConfigHome:    containerAgyConfigHome,
		skillsRelDir:        "skills",
		containerArgs: func(modelID string, modelSet bool, extraArgs []string) []string {
			var out []string
			if modelSet && modelID != "" {
				out = append(out, "--model", modelID)
			}
			return append(out, extraArgs...)
		},
	}
}

// agyAuthVolume returns the deterministic per-project Docker named volume
// holding agy's OAuth credentials and transcripts for dir. Scoping by the
// canonical project path prevents cross-project transcript/settings leakage.
func agyAuthVolume(dir string) string {
	clean := filepath.Clean(dir)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		clean = real
	}
	sum := sha256.Sum256([]byte(clean))
	return "rnes-auth-agy-" + hex.EncodeToString(sum[:])[:12]
}
