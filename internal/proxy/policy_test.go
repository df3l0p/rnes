package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policyDoc is a small helper for tests: one ordinary entry plus extras.
func mustPolicy(t *testing.T, raw string) Policy {
	t.Helper()
	pol, err := ParsePolicy(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return pol
}

func TestLoadPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	const policy = `{
  "policy": "v1",
  "mode": "allowlist",
  "hosts": {
    "host.docker.internal:8080": {},
    "example.com:443": {}
  }
}
`
	if err := os.WriteFile(path, []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !pol.Allows("host.docker.internal:8080") || !pol.Allows("example.com:443") {
		t.Errorf("Allow = %v, missing an expected entry", pol.Allow)
	}
	if len(pol.Allow) != 2 {
		t.Errorf("Allow = %v, want exactly the two listed hosts", pol.Allow)
	}
	if pol.Allows("evil.example:443") {
		t.Error("an unlisted host must not be allowed")
	}
}

func TestParsePolicyEmptyAllowsNothing(t *testing.T) {
	for _, in := range []string{
		"",
		"null",
		`{"policy":"v1","mode":"allowlist","hosts":{}}`,
	} {
		pol := mustPolicy(t, in)
		if len(pol.Allow) != 0 {
			t.Errorf("ParsePolicy(%q) Allow = %v, want empty", in, pol.Allow)
		}
	}
}

func TestLoadPolicyMissingFile(t *testing.T) {
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("expected an error for a missing policy file")
	}
}

// Malformed JSON or an invalid entry is an error rather than silently
// skipped: a typo must not quietly leave the policy different from what was
// written.
func TestBuildPolicyRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		mode    string
		hosts   map[string]Entry
		want    string
	}{
		{"no port", "v1", "allowlist", map[string]Entry{"example.com": {}}, "not host:port"},
		{"non-numeric port", "v1", "allowlist", map[string]Entry{"example.com:https": {}}, "invalid port"},
		{"port zero", "v1", "allowlist", map[string]Entry{"example.com:0": {}}, "invalid port"},
		{"port too high", "v1", "allowlist", map[string]Entry{"example.com:70000": {}}, "invalid port"},
		{"no host", "v1", "allowlist", map[string]Entry{":443": {}}, "not host:port"},
		{"wildcard host", "v1", "allowlist", map[string]Entry{"*:443": {}}, "wildcard host"},
		{"host contains wildcard", "v1", "allowlist", map[string]Entry{"*.example.com:443": {}}, "wildcard host"},
		{"space in key", "v1", "allowlist", map[string]Entry{"example.com:443 extra": {}}, "not host:port"},
		{"wrong policy version", "v2", "allowlist", nil, `policy: must be "v1"`},
		{"missing policy version", "", "allowlist", map[string]Entry{"example.com:443": {}}, `policy: must be "v1"`},
		{"unsupported mode", "v1", "blocklist", nil, `mode: "blocklist" is not supported`},
		{"key without value", "v1", "allowlist", map[string]Entry{"example.com:443": {Key: "Authorization"}}, "key and value must be set together"},
		{"value without key", "v1", "allowlist", map[string]Entry{"example.com:443": {Value: "TOKEN"}}, "key and value must be set together"},
		{"bad header name", "v1", "allowlist", map[string]Entry{"example.com:443": {Key: "Bad Header", Value: "TOKEN"}}, "valid HTTP header name"},
		{"bad env var", "v1", "allowlist", map[string]Entry{"example.com:443": {Key: "Authorization", Value: "1TOKEN"}}, "valid environment variable name"},
		{"auth with no-inspect", "v1", "allowlist", map[string]Entry{"example.com:443": {NoInspect: true, Key: "Authorization", Value: "TOKEN"}}, "can't be combined with no-inspect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildPolicy(tc.version, tc.mode, tc.hosts)
			if err == nil {
				t.Fatalf("BuildPolicy(%s, %s, %v) succeeded", tc.version, tc.mode, tc.hosts)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParsePolicyRejectsUnknownAndMultipleDocuments(t *testing.T) {
	if _, err := ParsePolicy(strings.NewReader(`{"policy":"v1","mode":"allowlist","hosts":{},"extra":true}`)); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Errorf("unknown field = %v, want an error naming it", err)
	}
	if _, err := ParsePolicy(strings.NewReader(`{"policy":"v1","mode":"allowlist","hosts":{}} {"policy":"v1"}`)); err == nil || !strings.Contains(err.Error(), "exactly one JSON document") {
		t.Errorf("two documents = %v, want an error", err)
	}
}

func TestParsePolicyDuplicateAfterCaseFolding(t *testing.T) {
	if _, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"Example.com:443": {},
		"example.com:443": {},
	}); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Errorf("case-folded duplicate = %v, want a duplicate error", err)
	}
}

func TestBuildPolicyNormalizesHostCaseAndPort(t *testing.T) {
	pol, err := BuildPolicy("v1", "", map[string]Entry{"EXAMPLE.com:0443": {}})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if !pol.Explicit("example.com:443") {
		t.Errorf("entry wasn't normalized to example.com:443: %+v", pol.Allow)
	}
	if pol.Allows("example.com:0443") {
		t.Error("target entries should be compared after port normalization, not left as written")
	}
}

func TestBuildPolicyWildcardPort(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"example.com:*":  {Private: true, NoInspect: true},
		"github.com:443": {},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	for _, hp := range []string{"example.com:443", "example.com:8443", "example.com:22"} {
		if !pol.Allows(hp) || !pol.Explicit(hp) {
			t.Errorf("%s must be allowed and explicit through the wildcard port", hp)
		}
		if !pol.AllowsPrivate(hp) || !pol.SkipsInspect(hp) {
			t.Errorf("%s must inherit the wildcard entry's marks", hp)
		}
	}
	if pol.Allows("other.com:443") {
		t.Error("a wildcard port must not widen the host")
	}
	if !pol.Allows("github.com:443") || pol.Allows("github.com:8443") {
		t.Error("an exact entry must keep its own port")
	}
	if got := strings.Join(pol.SortedAllow(), ","); got != "example.com:*,github.com:443" {
		t.Errorf("SortedAllow = %q", got)
	}
	if got := strings.Join(pol.DNSHosts(), ","); got != "github.com" {
		t.Errorf("DNSHosts = %q, want only the inspected host", got)
	}
}

// An exact entry takes precedence over a wildcard one, so a host can allow an
// arbitrary port privately while keeping its TLS port public and inspected.
func TestBuildPolicyExactEntryOverridesWildcardPort(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"example.com:*":   {Private: true, NoInspect: true},
		"example.com:443": {},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if pol.AllowsPrivate("example.com:443") || pol.SkipsInspect("example.com:443") {
		t.Error("the exact :443 entry must not inherit the wildcard marks")
	}
	if !pol.AllowsPrivate("example.com:8443") || !pol.SkipsInspect("example.com:8443") {
		t.Error("other ports must still use the wildcard entry")
	}
}

func TestParsePolicyPrivateEntry(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"host.docker.internal:8080": {Private: true},
		"github.com:443":            {},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if !pol.Explicit("host.docker.internal:8080") || !pol.AllowsPrivate("host.docker.internal:8080") {
		t.Errorf("private entry must be both Explicit and AllowsPrivate: %+v", pol)
	}
	if pol.AllowsAnyAddress("host.docker.internal:8080") {
		t.Errorf("a policy-file private entry must not be unrestricted: %+v", pol)
	}
	if !pol.Explicit("github.com:443") || pol.AllowsPrivate("github.com:443") {
		t.Errorf("ordinary entry must be Explicit and not AllowsPrivate: %+v", pol)
	}
	if got := strings.Join(pol.DNSHosts(), ","); got != "github.com,host.docker.internal" {
		t.Errorf("DNSHosts = %q", got)
	}
	if got := strings.Join(pol.SortedAllow(), ","); got != "github.com:443,host.docker.internal:8080" {
		t.Errorf("SortedAllow = %q", got)
	}
}

func TestParsePolicyCombinesPrivateAndNoInspect(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"host.docker.internal:443": {Private: true, NoInspect: true},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	const hp = "host.docker.internal:443"
	if !pol.Explicit(hp) || !pol.AllowsPrivate(hp) || !pol.SkipsInspect(hp) {
		t.Errorf("policy = %+v, want explicit + private + no-inspect", pol)
	}
	if got := strings.Join(pol.DNSHosts(), ","); got != "" {
		t.Errorf("DNSHosts = %q, want none (no-inspect excludes it)", got)
	}
}

func TestParsePolicyAuthMetadata(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"api.example.com:443":   {Key: "Authorization", Value: "EXAMPLE_TOKEN"},
		"wild.example.com:*":    {Key: "X-Api-Key", Value: "WILD_TOKEN"},
		"plain.example.com:443": {},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if got, ok := pol.AuthFor("api.example.com:443"); !ok || got.Key != "Authorization" || got.Value != "EXAMPLE_TOKEN" {
		t.Errorf("AuthFor(api.example.com:443) = (%+v, %v)", got, ok)
	}
	if got, ok := pol.AuthFor("wild.example.com:8443"); !ok || got.Key != "X-Api-Key" || got.Value != "WILD_TOKEN" {
		t.Errorf("AuthFor(wild.example.com:8443) = (%+v, %v), want the wildcard entry's auth", got, ok)
	}
	if _, ok := pol.AuthFor("plain.example.com:443"); ok {
		t.Error("an entry without key/value must have no auth metadata")
	}
	if _, ok := pol.AuthFor("unknown.example.com:443"); ok {
		t.Error("an unlisted host must have no auth metadata")
	}
	if len(pol.AnyAddress) != 0 {
		t.Errorf("a policy file must never produce an any-address entry: %+v", pol.AnyAddress)
	}
}

func TestParsePolicyNoInspectExcludesHostFromDNSHosts(t *testing.T) {
	pol, err := BuildPolicy("v1", "allowlist", map[string]Entry{
		"accounts.google.com:443": {NoInspect: true},
		"github.com:443":          {},
	})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if !pol.Allows("accounts.google.com:443") || !pol.Explicit("accounts.google.com:443") || !pol.SkipsInspect("ACCOUNTS.GOOGLE.COM:443") {
		t.Errorf("no-inspect entry should be allowed, explicit, and SkipsInspect: %+v", pol)
	}
	if got := strings.Join(pol.DNSHosts(), ","); got != "github.com" {
		t.Errorf("DNSHosts = %q, want github.com only", got)
	}
	if got := strings.Join(pol.SortedAllow(), ","); got != "accounts.google.com:443,github.com:443" {
		t.Errorf("SortedAllow = %q", got)
	}
}

func TestMarshalPolicyRoundTrip(t *testing.T) {
	pol := NewPolicy([]string{"github.com:443", "example.com:*"})
	pol.MarkPrivate("host.docker.internal:8080")
	pol.MarkNoInspect("grpc.example.com:443")
	pol.SetAuth("api.example.com:443", AuthConfig{Key: "Authorization", Value: "EXAMPLE_TOKEN"})
	pol.SetAuth("api.example.com:*", AuthConfig{Key: "X-Api-Key", Value: "WILD_TOKEN"})

	raw, err := MarshalPolicy(pol)
	if err != nil {
		t.Fatalf("MarshalPolicy: %v", err)
	}
	if bytes.Contains(raw, []byte("any-public")) {
		t.Errorf("MarshalPolicy wrote a transport mode into the file:\n%s", raw)
	}
	back := mustPolicy(t, string(raw))
	for _, hp := range pol.SortedAllow() {
		if !back.Explicit(hp) {
			t.Errorf("round trip lost %s:\n%s", hp, raw)
		}
		if pol.AllowsPrivate(hp) != back.AllowsPrivate(hp) {
			t.Errorf("round trip changed private for %s", hp)
		}
		if pol.SkipsInspect(hp) != back.SkipsInspect(hp) {
			t.Errorf("round trip changed no-inspect for %s", hp)
		}
	}
	if a, ok := back.AuthFor("api.example.com:443"); !ok || a != (AuthConfig{Key: "Authorization", Value: "EXAMPLE_TOKEN"}) {
		t.Errorf("round trip lost auth metadata: %+v, %v", a, ok)
	}
	if a, ok := back.AuthFor("api.example.com:8443"); !ok || a != (AuthConfig{Key: "X-Api-Key", Value: "WILD_TOKEN"}) {
		t.Errorf("round trip lost wildcard auth metadata: %+v, %v", a, ok)
	}

	// Transport modes can't be represented in the file, so serializing one
	// must fail rather than silently drop it.
	pol.AnyPublic = true
	if _, err := MarshalPolicy(pol); err == nil || !strings.Contains(err.Error(), "transport modes") {
		t.Errorf("MarshalPolicy(AnyPublic) = %v, want a transport-mode error", err)
	}
	pol.AnyPublic = false
	pol.AnyPrivate = true
	if _, err := MarshalPolicy(pol); err == nil || !strings.Contains(err.Error(), "transport modes") {
		t.Errorf("MarshalPolicy(AnyPrivate) = %v, want a transport-mode error", err)
	}
}
