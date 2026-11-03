package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.txt")
	const policy = `# comments and blank lines are ignored

host.docker.internal:8080
  example.com:443   # so is a trailing comment, and surrounding space
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
	pol, err := ParsePolicy(strings.NewReader("# nothing here\n\n"))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if len(pol.Allow) != 0 {
		t.Errorf("Allow = %v, want empty", pol.Allow)
	}
}

func TestLoadPolicyMissingFile(t *testing.T) {
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.txt")); err == nil {
		t.Fatal("expected an error for a missing policy file")
	}
}

// A malformed line is an error rather than silently skipped: a typo in an
// entry must not quietly leave the policy different from what was written.
func TestParsePolicyRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{
		"example.com",           // no port
		"example.com:https",     // non-numeric port
		"example.com:0",         // port out of range
		"example.com:70000",     // port out of range
		":443",                  // no host
		"example.com:443 extra", // two fields
	} {
		_, err := ParsePolicy(strings.NewReader("github.com:443\n" + line + "\n"))
		if err == nil {
			t.Errorf("ParsePolicy accepted %q", line)
			continue
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("error for %q = %v, want it to name line 2", line, err)
		}
	}
}

func TestParsePolicyStarAllowsAnyPublicHost(t *testing.T) {
	pol, err := ParsePolicy(strings.NewReader("host.docker.internal:8080\n*  # any public host\n"))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if !pol.AnyPublic {
		t.Error("a * line must set AnyPublic")
	}
	if !pol.Allows("example.com:443") || !pol.Allows("example.com:22") {
		t.Error("with *, any host:port passes the policy (the public-address check happens at dial time)")
	}
	if !pol.Explicit("host.docker.internal:8080") || pol.Explicit("example.com:443") {
		t.Error("Explicit must report only listed entries, not ones allowed by *")
	}
}

func TestParsePolicyPrivateEntry(t *testing.T) {
	pol, err := ParsePolicy(strings.NewReader("private host.docker.internal:8080 # llama-server\ngithub.com:443\n"))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
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
	for _, line := range []string{
		"no-inspect private host.docker.internal:443",
		"private no-inspect host.docker.internal:443",
	} {
		pol, err := ParseUserPolicy(strings.NewReader(line + "\n"))
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		const hp = "host.docker.internal:443"
		if !pol.Explicit(hp) || !pol.AllowsPrivate(hp) || !pol.SkipsInspect(hp) {
			t.Errorf("%q: policy = %+v, want explicit + private + no-inspect", line, pol)
		}
		if got := strings.Join(pol.DNSHosts(), ","); got != "" {
			t.Errorf("%q: DNSHosts = %q, want none (no-inspect excludes it)", line, got)
		}
	}
	if _, err := ParseUserPolicy(strings.NewReader("no-inspect private bad_port\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("combined malformed = %v, want line 1 error", err)
	}
}

func TestParseUserPolicyAcceptsPrivateAndRejectsStar(t *testing.T) {
	pol, err := ParseUserPolicy(strings.NewReader("# comment\npypi.org:443\nprivate host.docker.internal:8080\nno-inspect grpc.example.com:443\n"))
	if err != nil || !pol.Explicit("pypi.org:443") || !pol.Explicit("grpc.example.com:443") || pol.AnyPublic {
		t.Fatalf("ParseUserPolicy valid = (%+v, %v)", pol, err)
	}
	if !pol.Explicit("host.docker.internal:8080") || !pol.AllowsPrivate("host.docker.internal:8080") {
		t.Errorf("private entry must be Explicit and AllowsPrivate: %+v", pol)
	}
	if pol.AllowsAnyAddress("host.docker.internal:8080") {
		t.Errorf("a policy file must never produce an any-address entry: %+v", pol)
	}
	if pol.AllowsPrivate("pypi.org:443") {
		t.Errorf("ordinary entry must not be private: %+v", pol)
	}
	if !pol.SkipsInspect("grpc.example.com:443") || pol.SkipsInspect("pypi.org:443") {
		t.Errorf("SkipsInspect mismatch: %+v", pol)
	}
	if got := strings.Join(pol.DNSHosts(), ","); got != "host.docker.internal,pypi.org" {
		t.Errorf("DNSHosts = %q, want pypi.org and host.docker.internal (no-inspect excluded)", got)
	}
	if got := (&Server{Policy: pol}).dialerFor("host.docker.internal:8080"); got != publicPrivateDialer {
		t.Errorf("private entry dialer = %p, want publicPrivateDialer", got)
	}
	if _, err := ParseUserPolicy(strings.NewReader("pypi.org:443\n*\n")); err == nil || !strings.Contains(err.Error(), "-allow-public") {
		t.Errorf("ParseUserPolicy(*) = %v, want error naming -allow-public", err)
	}
	if _, err := ParseUserPolicy(strings.NewReader("private bad_port\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("ParseUserPolicy(private malformed) = %v, want line 1 error", err)
	}
	if _, err := ParseUserPolicy(strings.NewReader("no-inspect bad_port\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("ParseUserPolicy(no-inspect malformed) = %v, want line 1 error", err)
	}
}

func TestParsePolicyNoInspectExcludesHostFromDNSHosts(t *testing.T) {
	pol, err := ParsePolicy(strings.NewReader("no-inspect accounts.google.com:443\ngithub.com:443\n"))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
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
