package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Policy file schema version and mode. v1 supports allow-lists only; the mode
// field exists so a future blocklist (or another policy style) can be added
// without ambiguity.
const (
	PolicyVersion       = "v1"
	PolicyModeAllowlist = "allowlist"
)

// Entry is one policy host's optional settings. The zero value is an ordinary
// inspected, public-only entry. It is the interchange type between the YAML
// parser on the host and the proxy's own JSON policy file: the proxy package
// is built standalone from its embedded source, so it may not import a YAML
// library.
type Entry struct {
	// Private lets this one endpoint resolve to a private (RFC 1918 / IPv6
	// ULA) address as well as a public one.
	Private bool `json:"private,omitempty" yaml:"private,omitempty"`
	// NoInspect blind-tunnels this endpoint's TLS instead of decrypting it.
	NoInspect bool `json:"no-inspect,omitempty" yaml:"no-inspect,omitempty"`
	// Key is the HTTP header to fill in for a generic authentication provider.
	// Value names the environment variable holding the secret to put in it.
	// Both must be set together. Injection is reserved for the auth-provider
	// phase; parsing them now is the schema foundation.
	Key   string `json:"key,omitempty" yaml:"key,omitempty"`
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
}

// policyDocument is the internal JSON shape rnes writes for rnes-proxy. The
// user-facing file is YAML; the host parses and validates it, then renders
// this schema so the proxy stays dependency-free.
type policyDocument struct {
	Policy string           `json:"policy"`
	Mode   string           `json:"mode"`
	Hosts  map[string]Entry `json:"hosts"`
}

// ParsePolicy reads a Policy in its internal JSON form. An empty document is a
// valid empty allow-list; anything malformed is an error. Callers must never
// fall back to allow-all, or to a policy that differs from what was written.
func ParsePolicy(r io.Reader) (Policy, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var doc policyDocument
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return NewPolicy(nil), nil
		}
		return Policy{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Policy{}, err
		}
		return Policy{}, errors.New("policy file must contain exactly one JSON document")
	}
	return BuildPolicy(doc.Policy, doc.Mode, doc.Hosts)
}

// BuildPolicy validates and normalizes a policy document. Host keys are
// "host:port", with "*" allowed as the port. It is shared with the host-side
// YAML parser so both formats enforce exactly the same rules.
func BuildPolicy(version, mode string, hosts map[string]Entry) (Policy, error) {
	if version == "" && mode == "" && len(hosts) == 0 {
		return NewPolicy(nil), nil
	}
	if version != PolicyVersion {
		return Policy{}, fmt.Errorf("policy: must be %q", PolicyVersion)
	}
	switch mode {
	case "", PolicyModeAllowlist:
	default:
		return Policy{}, fmt.Errorf("mode: %q is not supported (only %q)", mode, PolicyModeAllowlist)
	}

	pol := NewPolicy(nil)
	keys := make([]string, 0, len(hosts))
	for hp := range hosts {
		keys = append(keys, hp)
	}
	sort.Strings(keys)

	seen := make(map[string]string, len(keys))
	for _, hp := range keys {
		entry := hosts[hp]
		normalized, err := validateHostPortPattern(hp)
		if err != nil {
			return Policy{}, fmt.Errorf("hosts[%q]: %w", hp, err)
		}
		if prev, ok := seen[normalized]; ok {
			return Policy{}, fmt.Errorf("hosts[%q]: duplicates %q after case normalization", hp, prev)
		}
		seen[normalized] = hp

		pol.Add(normalized)
		if entry.Private {
			pol.MarkPrivate(normalized)
		}
		if entry.NoInspect {
			pol.MarkNoInspect(normalized)
		}
		if (entry.Key == "") != (entry.Value == "") {
			return Policy{}, fmt.Errorf("hosts[%q]: key and value must be set together", hp)
		}
		if entry.Key != "" {
			if entry.NoInspect {
				return Policy{}, fmt.Errorf("hosts[%q]: key/value need TLS inspection, so they can't be combined with no-inspect", hp)
			}
			if err := validateHeaderName(entry.Key); err != nil {
				return Policy{}, fmt.Errorf("hosts[%q]: key: %w", hp, err)
			}
			if err := validateEnvVar(entry.Value); err != nil {
				return Policy{}, fmt.Errorf("hosts[%q]: value: %w", hp, err)
			}
			pol.SetAuth(normalized, AuthConfig{Key: entry.Key, Value: entry.Value})
		}
	}
	return pol, nil
}

// validateHostPortPattern checks one hosts map key and returns it normalized
// (lowercase host, explicit brackets for IPv6). The port may be "*".
func validateHostPortPattern(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" || strings.ContainsAny(s, " \t") {
		return "", fmt.Errorf("%q is not host:port", s)
	}
	if strings.Contains(host, "*") {
		return "", fmt.Errorf("%q uses a wildcard host; only the port may be \"*\"", s)
	}
	if port != "*" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("%q has an invalid port", s)
		}
		// Canonicalize so "host:0443" matches the CONNECT target "host:443".
		port = strconv.Itoa(p)
	}
	return net.JoinHostPort(strings.ToLower(host), port), nil
}

// validateHeaderName accepts the RFC 9110 token characters, so a header name
// can't smuggle a colon, whitespace or control character into the request.
func validateHeaderName(s string) error {
	if s == "" {
		return errors.New("the header name is empty")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return fmt.Errorf("%q is not a valid HTTP header name", s)
		}
	}
	return nil
}

// validateEnvVar accepts a shell-style environment variable name, which is
// what value names; it never contains the secret itself.
func validateEnvVar(s string) error {
	if s == "" {
		return errors.New("the environment variable name is empty")
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return fmt.Errorf("%q is not a valid environment variable name", s)
			}
		default:
			return fmt.Errorf("%q is not a valid environment variable name", s)
		}
	}
	return nil
}

// SortedAllow lists the policy's explicit "host:port" entries in sorted order.
// Wildcard entries keep their "*" port.
func (p Policy) SortedAllow() []string {
	out := make([]string, 0, len(p.Allow))
	for hp := range p.Allow {
		out = append(out, hp)
	}
	sort.Strings(out)
	return out
}

// DNSHosts lists, sorted and without duplicates, the host names in the policy's
// explicit entries that are subject to TLS interception, ignoring ports, IP
// literals, and no-inspect entries. These are the hosts an interception CA can
// be constrained to.
func (p Policy) DNSHosts() []string {
	seen := map[string]bool{}
	var hosts []string
	for hp := range p.Allow {
		if p.SkipsInspect(hp) {
			continue
		}
		host, _, err := net.SplitHostPort(hp)
		if err != nil || net.ParseIP(host) != nil || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// Entries renders p's per-host settings for serialization.
func (p Policy) Entries() map[string]Entry {
	out := make(map[string]Entry, len(p.Allow))
	for hp := range p.Allow {
		out[hp] = Entry{
			Private:   p.Private[hp],
			NoInspect: p.NoInspect[hp],
			Key:       p.Auth[hp].Key,
			Value:     p.Auth[hp].Value,
		}
	}
	return out
}

// MarshalPolicy renders p's explicit entries in the internal JSON form, for
// rnes-proxy's policy file. The AnyPublic and AnyPrivate transport modes are
// intentionally not rendered: rnes-proxy receives those as command-line flags,
// so a project policy can never grant unrestricted egress by itself. Passing a
// policy with either mode set is an error rather than silently dropping it.
func MarshalPolicy(p Policy) ([]byte, error) {
	if p.AnyPublic || p.AnyPrivate {
		return nil, errors.New("transport modes (AnyPublic/AnyPrivate) must be passed as rnes-proxy flags, not serialized")
	}
	raw, err := json.MarshalIndent(policyDocument{
		Policy: PolicyVersion,
		Mode:   PolicyModeAllowlist,
		Hosts:  p.Entries(),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// LoadPolicy reads a Policy from a file in ParsePolicy's internal JSON format.
// A missing or malformed file is an error.
func LoadPolicy(path string) (Policy, error) {
	f, err := os.Open(path)
	if err != nil {
		return Policy{}, fmt.Errorf("reading policy file %s: %w", path, err)
	}
	defer f.Close()
	pol, err := ParsePolicy(f)
	if err != nil {
		return Policy{}, fmt.Errorf("parsing policy file %s: %w", path, err)
	}
	return pol, nil
}
