package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/proxy"
)

const yamlHeader = "policy: v1\nmode: allowlist\n"

func TestParsePolicyYAML(t *testing.T) {
	pol, err := parsePolicyYAML(strings.NewReader(`# comment
policy: v1
mode: allowlist

hosts:
  "github.com:443": {}
  Example.com:*:   # unquoted keys are fine when the colon isn't followed by a space
    private: true
    key: Authorization
    value: EXAMPLE_TOKEN
    format: Bearer {value}
  "grpc.example.com:443":
    no-inspect: true
`))
	if err != nil {
		t.Fatalf("parsePolicyYAML: %v", err)
	}
	if !pol.Explicit("github.com:443") {
		t.Error("github.com:443 missing")
	}
	if !pol.Allows("example.com:8443") || !pol.AllowsPrivate("example.com:8443") {
		t.Errorf("wildcard/private entry = %+v", pol)
	}
	if a, ok := pol.AuthFor("example.com:8443"); !ok || a.Key != "Authorization" || a.Value != "EXAMPLE_TOKEN" || a.Format != "Bearer {value}" {
		t.Errorf("auth = (%+v, %v)", a, ok)
	}
	if !pol.SkipsInspect("grpc.example.com:443") {
		t.Error("no-inspect mark missing")
	}
}

func TestParsePolicyYAMLAllowsTrailingEmptyDocuments(t *testing.T) {
	for _, doc := range []string{
		yamlHeader + "hosts: {}\n---\n",
		yamlHeader + "hosts: {}\n---\n---\n",
	} {
		if _, err := parsePolicyYAML(strings.NewReader(doc)); err != nil {
			t.Errorf("parsePolicyYAML(%q) = %v, want a trailing empty document to be accepted", doc, err)
		}
	}
}

func TestParsePolicyYAMLRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{"wrong version", "policy: v2\nmode: allowlist\nhosts: {}\n", `policy: must be "v1"`},
		{"missing version", "mode: allowlist\nhosts: {\"example.com:443\": {}}\n", `policy: must be "v1"`},
		{"unsupported mode", "policy: v1\nmode: blocklist\nhosts: {}\n", `mode: "blocklist" is not supported`},
		{"wildcard host", yamlHeader + "hosts: {\"*:*\": {}}\n", "wildcard host"},
		{"unknown top-level field", yamlHeader + "hosts: {}\nextra: true\n", "extra"},
		{"unknown entry field", yamlHeader + "hosts: {\"example.com:443\": {privatee: true}}\n", "privatee"},
		{"key without value", yamlHeader + "hosts: {\"example.com:443\": {key: Authorization}}\n", "key and value must be set together"},
		{"auth with no-inspect", yamlHeader + "hosts: {\"example.com:443\": {no-inspect: true, key: Authorization, value: TOKEN}}\n", "can't be combined with no-inspect"},
		{"format without key/value", yamlHeader + "hosts: {\"example.com:443\": {format: \"Bearer {value}\"}}\n", "format needs key and value"},
		{"format with key only", yamlHeader + "hosts: {\"example.com:443\": {key: Authorization, format: \"Bearer {value}\"}}\n", "format needs key and value"},
		{"format with value only", yamlHeader + "hosts: {\"example.com:443\": {value: TOKEN, format: \"Bearer {value}\"}}\n", "format needs key and value"},
		{"format without placeholder", yamlHeader + "hosts: {\"example.com:443\": {key: Authorization, value: TOKEN, format: Bearer}}\n", `format must contain "{value}" exactly once`},
		{"two documents", yamlHeader + "hosts: {}\n---\n" + yamlHeader + "hosts: {}\n", "exactly one YAML document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePolicyYAML(strings.NewReader(tc.doc))
			if err == nil {
				t.Fatalf("parsePolicyYAML accepted %q", tc.doc)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The documentation example must stay a valid policy: a broken example is
// worse than none.
func TestPolicyExampleParses(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "policy.example.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := parsePolicyYAML(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !pol.Explicit("github.com:443") {
		t.Error("example policy missing github.com:443")
	}
	// Optional settings are documentation examples, commented out. Copying
	// the file must not silently allow example hosts.
	for _, not := range []string{"api.github.com:443", "example.com:*", "host.docker.internal:5432", "grpc.example.com:443", "api.example.com:443"} {
		if pol.Explicit(not) {
			t.Errorf("example policy allows %s without the user uncommenting it", not)
		}
	}
}

// The user-facing YAML becomes the proxy's internal JSON; marks and auth
// metadata must survive that hand-off.
func TestPolicyYAMLRoundTripsThroughProxyJSON(t *testing.T) {
	pol, err := parsePolicyYAML(strings.NewReader(`policy: v1
mode: allowlist
hosts:
  "example.com:*":
    private: true
    key: Authorization
    value: EXAMPLE_TOKEN
    format: Bearer {value}
  "grpc.example.com:443":
    no-inspect: true
`))
	if err != nil {
		t.Fatalf("parsePolicyYAML: %v", err)
	}
	raw, err := proxy.MarshalPolicy(pol)
	if err != nil {
		t.Fatalf("MarshalPolicy: %v", err)
	}
	back, err := proxy.ParsePolicy(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePolicy(%s): %v", raw, err)
	}
	if !back.Explicit("example.com:8443") || !back.AllowsPrivate("example.com:8443") {
		t.Errorf("wildcard/private mark lost in the JSON hand-off:\n%s", raw)
	}
	if a, ok := back.AuthFor("example.com:8443"); !ok || a.Key != "Authorization" || a.Value != "EXAMPLE_TOKEN" || a.Format != "Bearer {value}" {
		t.Errorf("auth metadata = (%+v, %v), want it preserved", a, ok)
	}
	if !back.SkipsInspect("grpc.example.com:443") {
		t.Errorf("no-inspect mark lost in the JSON hand-off:\n%s", raw)
	}
}
