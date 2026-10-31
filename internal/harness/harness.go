// Package harness defines the interface rnes uses to point a coding agent at
// a model provider (a local llama-server, or the agent's own cloud/subscription
// backend) and run it. New agents can be added by implementing Harness (or a
// sandboxProfile) and registering in registry below.
package harness

import (
	"fmt"
	"sort"
	"strings"

	"github.com/df3l0p/rnes/images"
	"github.com/df3l0p/rnes/internal/skills"
)

// Harness configures a coding agent and runs it.
type Harness interface {
	// Available reports an error if the harness's executable (or container
	// runtime) can't be found.
	Available() error
	// Configure points the harness at baseURL under providerKey, offering
	// the given model ids. Not called when NeedsModelServer(h) is false.
	Configure(providerKey, baseURL string, modelIDs []string) error
	// Run launches the harness in dir, wiring stdio to the current process
	// and blocking until it exits. Where the harness runs against its own
	// session-local configuration (the opencode sandbox), it starts on
	// modelID (one of the ids given to Configure, served under providerKey);
	// where it runs against the user's own configuration, the harness must
	// not rewrite the user's configured default, so modelID is advisory (or
	// forwarded only when Options.ModelSet is true).
	Run(dir, providerKey, modelID string) error
}

// Preparer is implemented by harnesses that need setup before the session
// starts (e.g. making sure a container image exists), so a failure there
// doesn't leave a server running for nothing.
type Preparer interface {
	Prepare() error
}

// AllPreparer is a Preparer that can also set up everything it offers, not
// just the selected variant (e.g. every bundled sandbox image).
type AllPreparer interface {
	PrepareAll() error
}

// BindHoster is implemented by harnesses that run the agent somewhere the
// host's loopback isn't reachable (a container), so the model server must
// listen on a different address. It returns the narrowest host address the
// agent can still reach; it must never widen to a wildcard.
type BindHoster interface {
	BindHost() (string, error)
}

// ModelServerUser is implemented by harnesses whose need for a local
// llama-server isn't always true (e.g. cloud-native subscription agents).
// Harnesses that don't implement it require a model server.
type ModelServerUser interface {
	NeedsModelServer() bool
}

// NeedsModelServer reports whether h uses a local llama-server (calling
// Configure before Run). It defaults to true when h doesn't implement
// ModelServerUser.
func NeedsModelServer(h Harness) bool {
	if m, ok := h.(ModelServerUser); ok {
		return m.NeedsModelServer()
	}
	return true
}

// AuthVolumer is implemented by sandbox harnesses that persist OAuth/session
// state for dir in a Docker named volume across runs.
type AuthVolumer interface {
	AuthVolume(dir string) string
}

// AuthVolume returns the Docker named volume h uses to persist auth state for
// dir, or "" when h doesn't persist auth in a volume.
func AuthVolume(h Harness, dir string) string {
	if a, ok := h.(AuthVolumer); ok {
		return a.AuthVolume(dir)
	}
	return ""
}

// Options selects how a harness is run.
type Options struct {
	// Sandbox runs the agent in a Docker container instead of on the host.
	Sandbox bool
	// Image names the bundled sandbox image (see package images); empty means
	// images.Default.
	Image string
	// Build rebuilds the sandbox image (and the base image it layers on)
	// even if it already exists locally. A missing image is always built, never
	// pulled.
	Build bool
	// AllNet lets the sandbox reach any public host through its egress
	// proxy, not just the default allow-list. The host machine and the LAN
	// stay out of reach apart from llama-server.
	AllNet bool
	// NoInspect turns off TLS interception in the sandbox's egress proxy. By
	// default the proxy terminates TLS (with a CA made for the session, which
	// the sandbox is set up to trust) for the hosts on its allow-list, and
	// logs each request; with NoInspect every HTTPS connection is tunnelled
	// unread, as before.
	NoInspect bool
	// GitHubToken, when set (with Sandbox), is injected by the sandbox's proxy
	// into requests to GitHub; the sandbox itself only gets a placeholder.
	GitHubToken string
	// OpenRouterKey, when set (with Sandbox), is injected by the sandbox's proxy
	// into requests to openrouter.ai; the sandbox itself only gets a placeholder.
	OpenRouterKey string
	// Skills are host skills to mount read-only into the sandbox, where the
	// active harness looks for global skills (with Sandbox; see package skills).
	Skills []skills.Skill
	// WebExpose serves opencode's web UI for the session (in the sandbox,
	// through the proxy's loopback forward). The TUI keeps running against the
	// same server, so both share one session. On the host the server already
	// serves the UI; exposing it there needs nothing extra.
	WebExpose bool
	// WebOpen opens the web UI in the browser at startup. Implies WebExpose.
	WebOpen bool
	// Policy, when non-nil (with Sandbox), replaces the built-in default
	// egress allow-list (e.g. from .rnes/policy.txt). An empty non-nil slice
	// allows nothing besides this session's model provider (or any public host
	// with AllNet).
	Policy []byte
	// ModelSet reports whether -model was explicitly passed on the command
	// line (used by harnesses whose own settings pick the model by default).
	ModelSet bool
	// Args are trailing command-line arguments after "--" forwarded to the
	// agent executable.
	Args []string
}

// registration is one harness's constructor plus its static need for a local
// llama-server, so pre-flight checks can ask without constructing it (and
// before the session's full Options, e.g. credentials, are known).
type registration struct {
	new              func(Options) (Harness, error)
	needsModelServer bool
}

// registry maps a harness name, as accepted by rnes's -harness flag, to its
// registration. Each harness owns its own config path (e.g. opencode's
// default global config location) rather than taking one from rnes's flags.
var registry = map[string]registration{
	"agy":      {new: newAgy, needsModelServer: false},
	"opencode": {new: newOpencode, needsModelServer: true},
}

// Names returns the registered harness names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// NeedsModelServerName reports whether the harness registered under name uses
// a local llama-server, without constructing it. It is for pre-flight checks
// (e.g. rejecting -host/-port or dropping credentials) that must happen before
// the session's Options are resolved.
func NeedsModelServerName(name string) (bool, error) {
	r, ok := registry[name]
	if !ok {
		return false, fmt.Errorf("unknown harness %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	return r.needsModelServer, nil
}

// New looks up name in the registry and constructs it, or returns an error
// listing the available names.
func New(name string, opts Options) (Harness, error) {
	r, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	return r.new(opts)
}

// PrepareAllHarnesses builds or ensures the sandbox images for every
// registered harness (all selectable images when opts.Image is empty, or just
// opts.Image when set), plus the shared proxy image once.
func PrepareAllHarnesses(opts Options) error {
	opts.Sandbox = true
	imgNames := images.Names()
	if opts.Image != "" {
		imgNames = []string{opts.Image}
	}
	var checked bool
	for _, name := range Names() {
		h, err := New(name, opts)
		if err != nil {
			return err
		}
		s, ok := h.(*Sandbox)
		if !ok {
			continue
		}
		if !checked {
			// Validate unknown -image before touching docker, then check docker once.
			for _, img := range imgNames {
				if _, err := images.Read(img); err != nil {
					return err
				}
			}
			if err := s.Available(); err != nil {
				return err
			}
			checked = true
		}
		if err := s.prepareImages(imgNames); err != nil {
			return err
		}
	}
	if _, err := prepareProxyImage(opts.Build); err != nil {
		return fmt.Errorf("preparing proxy image: %w", err)
	}
	return nil
}
