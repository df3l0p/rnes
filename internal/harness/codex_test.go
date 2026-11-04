package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCodexRequiresSandbox(t *testing.T) {
	if _, err := New("codex", Options{}); err == nil || !strings.Contains(err.Error(), "-sandbox") {
		t.Fatalf("New(codex, Sandbox=false) = %v, want a -sandbox error", err)
	}
	h, err := New("codex", Options{Sandbox: true})
	if err != nil {
		t.Fatalf("New(codex, Sandbox=true): %v", err)
	}
	if _, ok := h.(*Sandbox); !ok {
		t.Errorf("expected a *Sandbox harness, got %T", h)
	}
	if NeedsModelServer(h) {
		t.Error("codex must not require a local llama-server")
	}
}

func TestNewCodexRejectsAPolicyThatInspectsItsNoInspectEndpoints(t *testing.T) {
	// codex needs its OpenAI endpoints tunnelled unread; a project policy
	// that lists one of them for TLS inspection can't be honored, so
	// constructing the harness must fail rather than silently downgrade the
	// user's entry.
	_, err := New("codex", Options{Sandbox: true, Policy: []byte("chatgpt.com:443\n")})
	if err == nil || !strings.Contains(err.Error(), "chatgpt.com:443") {
		t.Fatalf("New(codex, inspecting policy) = %v, want an error naming chatgpt.com:443", err)
	}

	// Marking the entry no-inspect resolves the conflict.
	if _, err := New("codex", Options{Sandbox: true, Policy: []byte("no-inspect chatgpt.com:443\n")}); err != nil {
		t.Fatalf("New(codex, no-inspect policy): %v", err)
	}
}

func TestCodexAuthVolumeIsPerProjectAndCanonical(t *testing.T) {
	projA := filepath.Join(t.TempDir(), "proj-a")
	projB := filepath.Join(t.TempDir(), "proj-b")
	linkA := filepath.Join(t.TempDir(), "link-a")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(projA, linkA); err != nil {
		t.Fatal(err)
	}

	if volA, volLinkA := codexAuthVolume(projA), codexAuthVolume(linkA); volA != volLinkA {
		t.Errorf("codexAuthVolume(%q) = %q, codexAuthVolume(%q) = %q, want equal (canonical path)", projA, volA, linkA, volLinkA)
	}
	if !strings.HasPrefix(codexAuthVolume(projA), "rnes-auth-codex-") {
		t.Errorf("codexAuthVolume(%q) = %q, want an rnes-auth-codex- prefix", projA, codexAuthVolume(projA))
	}
	if volB := codexAuthVolume(projB); volB == codexAuthVolume(projA) {
		t.Errorf("codexAuthVolume(%q) = codexAuthVolume(%q) = %q, want distinct volumes per project", projB, projA, volB)
	}
}
