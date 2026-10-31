package harness

import (
	"testing"

	"github.com/df3l0p/rnes/images"
)

func TestNewReturnsRegisteredHarness(t *testing.T) {
	h, err := New("opencode", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := h.(Opencode); !ok {
		t.Errorf("expected an Opencode harness, got %T", h)
	}
}

func TestNewSandboxReturnsSandboxHarness(t *testing.T) {
	h, err := New("opencode", Options{Sandbox: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, ok := h.(*Sandbox)
	if !ok {
		t.Fatalf("expected a *Sandbox harness, got %T", h)
	}
	if s.name != images.Default {
		t.Errorf("image name = %q, want default %q", s.name, images.Default)
	}
}

func TestNewUnknownHarnessListsAvailableNames(t *testing.T) {
	for _, err := range []error{
		func() error { _, err := New("nope", Options{}); return err }(),
		func() error { _, err := NeedsModelServerName("nope"); return err }(),
	} {
		if err == nil {
			t.Fatal("expected an error for an unregistered harness name")
		}
		const want = `unknown harness "nope" (available: agy, opencode)`
		if err.Error() != want {
			t.Errorf("got %q, want %q", err.Error(), want)
		}
	}
}

// TestModelServerCapabilityMatchesTheConstructedHarness keeps the registry's
// pre-flight capability from drifting away from the harness's own answer.
func TestModelServerCapabilityMatchesTheConstructedHarness(t *testing.T) {
	for _, name := range Names() {
		want, err := NeedsModelServerName(name)
		if err != nil {
			t.Fatalf("NeedsModelServerName(%q): %v", name, err)
		}
		h, err := New(name, Options{Sandbox: true})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if got := NeedsModelServer(h); got != want {
			t.Errorf("%s: registry says needsModelServer=%v, harness says %v", name, want, got)
		}
	}
}
