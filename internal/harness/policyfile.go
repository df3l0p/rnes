package harness

import (
	"errors"
	"io"

	"github.com/df3l0p/rnes/internal/proxy"
	"gopkg.in/yaml.v3"
)

// policyYAML is the user-facing policy schema. The proxy package can't import
// a YAML library (its image is built standalone from embedded source), so the
// host parses and validates this shape and renders proxy.Entry values for the
// proxy's internal JSON file.
type policyYAML struct {
	Policy string                 `yaml:"policy"`
	Mode   string                 `yaml:"mode"`
	Hosts  map[string]proxy.Entry `yaml:"hosts"`
}

// parsePolicyYAML reads a policy in the user-facing YAML schema: a
// "policy: v1" header, "mode: allowlist", and a hosts map of "host:port" keys.
// Each host may carry optional private and no-inspect marks, plus key/value
// credential metadata for a future generic authentication provider. The port
// may be "*" to cover every port on that host.
//
// An empty document (or one with only comments) is a valid empty allow-list,
// so an empty .rnes/policy.yaml restricts a session to its automatic entries.
// Anything else is an error: callers must never fall back to allow-all, or to
// a policy that differs from what was written.
func parsePolicyYAML(r io.Reader) (proxy.Policy, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var doc policyYAML
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return proxy.NewPolicy(nil), nil
		}
		return proxy.Policy{}, err
	}
	// One non-empty document only: a second one is almost certainly a
	// mistake, and silently ignoring it could leave a host the user thinks is
	// listed. Empty documents are allowed, so a trailing "---" is harmless.
	for {
		var extra any
		if err := dec.Decode(&extra); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return proxy.Policy{}, err
		} else if extra != nil {
			return proxy.Policy{}, errors.New("policy file must contain exactly one YAML document")
		}
	}

	return proxy.BuildPolicy(doc.Policy, doc.Mode, doc.Hosts)
}
