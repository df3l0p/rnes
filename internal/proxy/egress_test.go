package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":         true,
		"140.82.112.3":    true, // github.com
		"2606:4700::1111": true,
		"10.1.2.3":        false, // RFC 1918
		"172.17.0.1":      false, // docker bridge gateway
		"192.168.65.254":  false, // Docker Desktop's host
		"127.0.0.1":       false,
		"169.254.169.254": false, // link-local (cloud metadata)
		"100.101.102.103": false, // CGNAT / Tailscale
		"0.0.0.0":         false,
		"224.0.0.1":       false, // multicast
		"::1":             false,
		"fd7a:115c::1":    false, // unique local
		"fe80::1":         false,
		"::ffff:10.0.0.1": false, // IPv4-mapped private
	} {
		if got := isPublic(net.ParseIP(addr)); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", addr, got, want)
		}
	}
}

// With *, a destination that isn't listed may only be a public address: the
// host machine, the LAN and loopback stay out of reach even by IP.
func TestAnyPublicStillRefusesUnlistedNonPublicAddresses(t *testing.T) {
	reached := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer backend.Close()
	backendHost := mustParseURL(t, backend.URL).Host // 127.0.0.1:<port>

	px := httptest.NewServer(&Server{Policy: Policy{AnyPublic: true}})
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("forward to unlisted loopback: status = %d, want 403", resp.StatusCode)
	}

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", backendHost, backendHost)
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Errorf("CONNECT to unlisted loopback: status line = %q, want 403", status)
	}
	if reached {
		t.Error("an unlisted non-public address must never be reached")
	}
}

// Private entries (llama-server on the host) keep working alongside *.
func TestAnyPublicKeepsPrivateEntries(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer backend.Close()

	pol := NewAnyAddressPolicy([]string{mustParseURL(t, backend.URL).Host})
	pol.AnyPublic = true
	px := httptest.NewServer(&Server{Policy: pol})
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("private entry under *: status = %d, want 200", resp.StatusCode)
	}
}

// Ordinary explicit entries (everything in a user's policy.txt) must also
// resolve to public addresses, so a listed domain that resolves or rebinds to
// loopback, LAN, or 169.254.169.254 is still refused with 403.
func TestExplicitPolicyEntryRefusesNonPublicAddresses(t *testing.T) {
	reached := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer backend.Close()
	backendHost := mustParseURL(t, backend.URL).Host // 127.0.0.1:<port>

	audit := &auditLog{}
	px := httptest.NewServer(&Server{Policy: NewPolicy([]string{backendHost}), Logf: audit.Logf})
	defer px.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, px.URL))}}
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("forward to listed non-public address: status = %d, want 403", resp.StatusCode)
	}

	conn, err := net.Dial("tcp", mustParseURL(t, px.URL).Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", backendHost, backendHost)
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Errorf("CONNECT to listed non-public address: status line = %q, want 403", status)
	}

	// Also check the TLS-intercepted path: an intercepted DNS name ("localhost")
	// that resolves to 127.0.0.1 without a private mark must be refused with 403.
	tlsHostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	ca, err := NewCA([]string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	tlsPx := httptest.NewServer(&Server{
		Policy:      NewPolicy([]string{tlsHostport}),
		CA:          ca,
		Logf:        audit.Logf,
		UpstreamTLS: newTLSConfig(upstreamTrust),
	})
	defer tlsPx.Close()
	tlsResp, err := clientVia(mustParseURL(t, tlsPx.URL), poolOf(ca)).Get("https://" + tlsHostport + "/")
	if err != nil {
		t.Fatalf("intercepted Get: %v", err)
	}
	tlsResp.Body.Close()
	if tlsResp.StatusCode != http.StatusForbidden {
		t.Errorf("intercepted request to non-public address: status = %d, want 403", tlsResp.StatusCode)
	}

	if reached {
		t.Error("a listed non-private policy entry must never reach a non-public address")
	}
	if !strings.Contains(audit.String(), "not a public address") {
		t.Errorf("audit log = %q, want 'not a public address'", audit.String())
	}
}

// -allow-private opens RFC 1918/ULA, not loopback. With -allow-public too,
// explicit hosts use the public+private dialer; the intercepted path must
// report its refusal as a denial (403 + audit line), like CONNECT and forward
// do, rather than falling through to a 502.
func TestAnyPrivateInterceptRefusesLoopbackAsDenied(t *testing.T) {
	reached := false
	tlsHostport, upstreamTrust := newUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	ca, err := NewCA([]string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	audit := &auditLog{}
	px := httptest.NewServer(&Server{
		Policy:      Policy{AnyPrivate: true, AnyPublic: true, Allow: map[string]bool{tlsHostport: true}},
		CA:          ca,
		Logf:        audit.Logf,
		UpstreamTLS: newTLSConfig(upstreamTrust),
	})
	defer px.Close()

	resp, err := clientVia(mustParseURL(t, px.URL), poolOf(ca)).Get("https://" + tlsHostport + "/")
	if err != nil {
		t.Fatalf("intercepted Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if reached {
		t.Error("an intercepted loopback destination must never be reached under -allow-private")
	}
	if !strings.Contains(audit.String(), "denied "+tlsHostport+" (not a public or private address)") {
		t.Errorf("audit log = %q, want a denied line with the address reason", audit.String())
	}
}

func TestIsPrivate(t *testing.T) {
	for addr, want := range map[string]bool{
		"10.1.2.3":        true,
		"172.16.0.1":      true,
		"172.31.255.255":  true,
		"192.168.0.10":    true,
		"fd7a:115c::1":    true, // IPv6 unique-local
		"8.8.8.8":         false,
		"172.32.0.1":      false,
		"192.169.1.1":     false,
		"127.0.0.1":       false, // loopback stays blocked
		"169.254.169.254": false, // link-local / cloud metadata
		"100.101.102.103": false, // CGNAT / Tailscale
		"fe80::1":         false,
	} {
		if got := isPrivate(net.ParseIP(addr)); got != want {
			t.Errorf("isPrivate(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestPrivateDialersScopeAddresses(t *testing.T) {
	checks := []struct {
		name   string
		dialer *net.Dialer
		addr   string
		err    error
	}{
		{"private accepts RFC1918", privateDialer, "10.1.2.3:443", nil},
		{"private accepts ULA", privateDialer, "[fd7a::1]:443", nil},
		{"private refuses public", privateDialer, "8.8.8.8:443", errNotPrivate},
		{"private refuses loopback", privateDialer, "127.0.0.1:443", errNotPrivate},
		{"private refuses metadata", privateDialer, "169.254.169.254:80", errNotPrivate},
		{"public+private accepts public", publicPrivateDialer, "8.8.8.8:443", nil},
		{"public+private accepts private", publicPrivateDialer, "192.168.1.2:443", nil},
		{"public+private refuses loopback", publicPrivateDialer, "127.0.0.1:443", errNotPublicOrPrivate},
		{"public+private refuses metadata", publicPrivateDialer, "169.254.169.254:80", errNotPublicOrPrivate},
	}
	for _, c := range checks {
		err := c.dialer.Control("tcp", c.addr, nil)
		if c.err == nil {
			if err != nil {
				t.Errorf("%s: %v, want allowed", c.name, err)
			}
			continue
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", c.name, err, c.err)
		}
	}
}

// -allow-private lets an unlisted destination through the policy level; the
// dialer then limits it to private addresses (and never loopback/metadata).
func TestAnyPrivateAllowsAnyHostButScopesAddresses(t *testing.T) {
	pol := Policy{AnyPrivate: true}
	if !pol.Allows("unlisted.example:443") || !pol.Allows("10.0.0.1:80") {
		t.Fatalf("-allow-private must let any destination reach the dialer: %+v", pol)
	}
	srv := &Server{Policy: pol}
	if got := srv.dialerFor("unlisted.example:443"); got != privateDialer {
		t.Errorf("unlisted host dialer = %p, want privateDialer", got)
	}

	explicit := Policy{AnyPrivate: true, Allow: map[string]bool{"listed.example:443": true}}
	if got := (&Server{Policy: explicit}).dialerFor("listed.example:443"); got != publicDialer {
		t.Errorf("explicit host with -allow-private dialer = %p, want publicDialer (listed hosts stay public-only)", got)
	}

	listedPrivate := Policy{
		AnyPrivate: true,
		Allow:      map[string]bool{"internal.example:443": true},
		Private:    map[string]bool{"internal.example:443": true},
	}
	if got := (&Server{Policy: listedPrivate}).dialerFor("internal.example:443"); got != publicPrivateDialer {
		t.Errorf("per-entry private host dialer = %p, want publicPrivateDialer", got)
	}

	anyAddr := Policy{
		Allow:      map[string]bool{"test.example:443": true},
		AnyAddress: map[string]bool{"test.example:443": true},
	}
	if got := (&Server{Policy: anyAddr}).dialerFor("test.example:443"); got != anyAddressDialer {
		t.Errorf("any-address host dialer = %p, want anyAddressDialer", got)
	}

	both := Policy{AnyPrivate: true, AnyPublic: true}
	if got := (&Server{Policy: both}).dialerFor("unlisted.example:443"); got != publicPrivateDialer {
		t.Errorf("-allow-public -allow-private dialer = %p, want publicPrivateDialer", got)
	}
}
