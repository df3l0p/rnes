// Package proxy implements the sandbox's egress forward proxy: a host
// allow/blocklist first, with TLS interception (see CA and handleIntercept)
// for the hosts the policy lists explicitly, and credential injection to be
// layered on top in a later phase.
package proxy

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// forwardTransport is a copy of http.DefaultTransport with proxying
// disabled. handleForward's sandbox already reaches this server via its own
// HTTP_PROXY/HTTPS_PROXY, but the Docker CLI can also inject an HTTP_PROXY
// into this proxy container's own environment from the operator's
// ~/.docker/config.json — this server must always dial the target directly,
// never chase that.
var forwardTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}()

// defaultHalfCloseIdle is Server.HalfCloseIdle's default.
const defaultHalfCloseIdle = 30 * time.Second

// Policy decides which upstream destinations may be reached. Entries are
// "host:port" strings, matching what a CONNECT target or a normalized
// absolute-URI request host already looks like, compared case-insensitively
// since host names are. The port may be "*", in which case every port on that
// host is covered. A wildcard host is deliberately not supported.
type Policy struct {
	// Allow is the set of listed "host:port" entries. An entry is public-only
	// unless it also carries the Private or AnyAddress mark; NoInspect only
	// affects TLS interception.
	Allow map[string]bool
	// Private names the subset of Allow entries that may connect to a private
	// (RFC 1918 or IPv6 ULA) address as well as a public one. This is the
	// per-entry "private" mark in policy files, including .rnes/policy.yaml.
	// Loopback, link-local metadata and CGNAT stay blocked for these entries.
	Private map[string]bool
	// AnyAddress names the subset of Allow entries whose address check is
	// skipped entirely, loopback included. It is for internal/test callers
	// (httptest backends); a policy file never produces it.
	AnyAddress map[string]bool
	// NoInspect names the subset of Allow entries whose HTTPS connections are
	// blind-tunnelled rather than TLS-terminated, even when a session CA is
	// configured (for endpoints using HTTP/2, gRPC, WebSockets, or certificate
	// pinning). They still go through the normal address check unless also in
	// Private (or both AnyPublic and AnyPrivate are set). Entries with
	// credentials (Auth) can't be no-inspect: injection needs decryption.
	NoInspect map[string]bool
	// Auth holds the optional credential-injection metadata for Allow entries:
	// the HTTP header to fill in and the environment variable that supplies
	// its value. rnes-proxy does not inject these yet; the schema is the
	// foundation for generic authentication providers.
	Auth map[string]AuthConfig
	// AnyPublic also lets through any destination not in Allow, as long as
	// the address actually dialed is public (see isPublic): the internet,
	// but not the host machine or the LAN.
	AnyPublic bool
	// AnyPrivate also lets through any destination not in Allow, as long as
	// the address actually dialed is private (RFC 1918 or IPv6 ULA, see
	// isPrivate): the host machine and the LAN, but not the internet. Explicit
	// Allow entries keep their public-only check unless marked Private; combine
	// with AnyPublic to allow either address type everywhere.
	AnyPrivate bool
}

// AuthConfig is one host's optional credential-injection metadata. Key is the
// HTTP header to set (for example "Authorization"), and Value names the
// environment variable holding the secret to put in it. The proxy only ever
// reads Value's name from the policy; the secret itself stays out of the file.
type AuthConfig struct {
	Key   string
	Value string
}

// entryKey returns the Allow key that covers hostport: an exact entry when one
// exists, otherwise a "host:*" wildcard entry. ok is false when neither is
// listed.
func (p Policy) entryKey(hostport string) (string, bool) {
	hp := strings.ToLower(hostport)
	if p.Allow[hp] {
		return hp, true
	}
	host, _, err := net.SplitHostPort(hp)
	if err != nil {
		return "", false
	}
	pattern := net.JoinHostPort(host, "*")
	if p.Allow[pattern] {
		return pattern, true
	}
	return "", false
}

// NewPolicy builds a Policy allowing the given "host:port" entries at public
// addresses.
func NewPolicy(hostports []string) Policy {
	m := make(map[string]bool, len(hostports))
	for _, h := range hostports {
		m[strings.ToLower(h)] = true
	}
	return Policy{Allow: m}
}

// NewAnyAddressPolicy builds a Policy allowing the given "host:port" entries
// at any address, loopback and link-local included. Tests use it for httptest
// backends; a policy file can only grant private (RFC 1918 / ULA) addresses.
func NewAnyAddressPolicy(hostports []string) Policy {
	pol := NewPolicy(hostports)
	pol.MarkAnyAddress(hostports...)
	return pol
}

// Add adds each hostport to Allow as an ordinary (public-only, inspected)
// entry. A hostport may use "*" as its port.
func (p *Policy) Add(hostports ...string) {
	if p.Allow == nil {
		p.Allow = make(map[string]bool, len(hostports))
	}
	for _, h := range hostports {
		p.Allow[strings.ToLower(h)] = true
	}
}

// MarkPrivate marks each hostport (and adds it to Allow) as permitted to dial
// a private (RFC 1918 or IPv6 ULA) address as well as a public one.
func (p *Policy) MarkPrivate(hostports ...string) {
	p.Add(hostports...)
	if p.Private == nil {
		p.Private = make(map[string]bool, len(hostports))
	}
	for _, h := range hostports {
		p.Private[strings.ToLower(h)] = true
	}
}

// AllowsPrivate reports whether hostport has the per-entry "private" mark: it
// may resolve to a private address as well as a public one. It is not the
// -allow-private mode, which is Policy.AnyPrivate and applies to unlisted hosts.
func (p Policy) AllowsPrivate(hostport string) bool {
	key, ok := p.entryKey(hostport)
	return ok && p.Private[key]
}

// MarkAnyAddress marks each hostport (and adds it to Allow) as permitted to
// dial any address, loopback and link-local included. Internal/test use only.
func (p *Policy) MarkAnyAddress(hostports ...string) {
	p.Add(hostports...)
	if p.AnyAddress == nil {
		p.AnyAddress = make(map[string]bool, len(hostports))
	}
	for _, h := range hostports {
		p.AnyAddress[strings.ToLower(h)] = true
	}
}

// AllowsAnyAddress reports whether hostport bypasses the address check
// entirely (the internal/test mark).
func (p Policy) AllowsAnyAddress(hostport string) bool {
	key, ok := p.entryKey(hostport)
	return ok && p.AnyAddress[key]
}

// MarkNoInspect marks each hostport (and adds it to Allow) as skipping TLS
// interception so CONNECT tunnels it untouched.
func (p *Policy) MarkNoInspect(hostports ...string) {
	p.Add(hostports...)
	if p.NoInspect == nil {
		p.NoInspect = make(map[string]bool, len(hostports))
	}
	for _, h := range hostports {
		p.NoInspect[strings.ToLower(h)] = true
	}
}

// SkipsInspect reports whether hostport is configured to bypass TLS
// interception and be blind-tunnelled instead.
func (p Policy) SkipsInspect(hostport string) bool {
	key, ok := p.entryKey(hostport)
	return ok && p.NoInspect[key]
}

// Explicit reports whether hostport is listed, as opposed to allowed only by
// AnyPublic.
func (p Policy) Explicit(hostport string) bool {
	_, ok := p.entryKey(hostport)
	return ok
}

// AuthFor returns the credential-injection metadata for hostport, if any.
// Because it follows the same exact-then-wildcard lookup as Explicit, a
// "host:*" entry's Auth applies to every port on that host.
func (p Policy) AuthFor(hostport string) (AuthConfig, bool) {
	key, ok := p.entryKey(hostport)
	if !ok {
		return AuthConfig{}, false
	}
	a, ok := p.Auth[key]
	return a, ok
}

// SetAuth stores credential-injection metadata for hostport (and adds it to
// Allow). The secret is never stored: Value is the environment variable's
// name.
func (p *Policy) SetAuth(hostport string, a AuthConfig) {
	p.Add(hostport)
	if p.Auth == nil {
		p.Auth = make(map[string]AuthConfig)
	}
	p.Auth[strings.ToLower(hostport)] = a
}

// Merge adds every explicit entry and mark from other to p. The AnyPublic and
// AnyPrivate transport modes are OR-ed too; MarshalPolicy does not render
// them (rnes-proxy receives them as command-line flags).
func (p *Policy) Merge(other Policy) {
	for hp := range other.Allow {
		p.Add(hp)
	}
	for hp := range other.Private {
		p.MarkPrivate(hp)
	}
	for hp := range other.AnyAddress {
		p.MarkAnyAddress(hp)
	}
	for hp := range other.NoInspect {
		p.MarkNoInspect(hp)
	}
	for hp, a := range other.Auth {
		p.SetAuth(hp, a)
	}
	p.AnyPublic = p.AnyPublic || other.AnyPublic
	p.AnyPrivate = p.AnyPrivate || other.AnyPrivate
}

// Allows reports whether hostport passes the policy. With AnyPublic or
// AnyPrivate that's every destination; the address-type check then happens
// when dialing.
func (p Policy) Allows(hostport string) bool {
	return p.AnyPublic || p.AnyPrivate || p.Explicit(hostport)
}

// Server is the sandbox's egress proxy. It implements http.Handler and is
// meant to be run behind http.ListenAndServe.
type Server struct {
	Policy Policy
	// CA, when set, makes the proxy terminate TLS for CONNECT targets the
	// policy lists explicitly and the CA covers, instead of tunnelling them
	// blind. Everything else (hosts allowed only by AnyPublic included) is
	// still tunnelled. See handleIntercept.
	CA *CA
	// OnRequest, when set, is called for every intercepted request before it is
	// forwarded, with the CONNECT target ("host:port"). It may change the
	// request; a non-nil error refuses it with a 403. It is where credential
	// injection or request rules would plug in.
	OnRequest func(r *http.Request, target string) error
	// Logf receives an audit line per proxied or denied request. Nil means the
	// standard logger.
	Logf func(format string, args ...any)
	// UpstreamTLS configures how intercepted requests verify the real server;
	// nil means the system roots. Tests use it to trust a test server.
	UpstreamTLS *tls.Config
	// HalfCloseIdle is how long a CONNECT tunnel stays open with no data
	// moving once one side has finished, so a peer that never closes its
	// end can't hold the tunnel forever. Zero means defaultHalfCloseIdle.
	HalfCloseIdle time.Duration

	// upstreams caches the transport per intercepted target (see upstream).
	upstreams sync.Map
}

func (s *Server) halfCloseIdle() time.Duration {
	if s.HalfCloseIdle > 0 {
		return s.HalfCloseIdle
	}
	return defaultHalfCloseIdle
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handleForward(w, r)
}

// handleConnect handles an allowed CONNECT target: terminated and inspected
// when the CA covers it (see handleIntercept), otherwise blind-tunnelled, with
// bytes relayed unmodified in both directions and never inspected.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !s.Policy.Allows(target) {
		s.denied(w, target)
		return
	}
	if s.interceptable(target) {
		s.handleIntercept(w, r, target)
		return
	}
	upstream, err := s.dialerFor(target).DialContext(r.Context(), "tcp", target)
	if err != nil {
		s.dialFailed(w, target, err)
		return
	}
	defer upstream.Close()
	s.logf("rnes-proxy: CONNECT %s -> tunnelled", target)

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "rnes-proxy: hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "rnes-proxy: hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	// A client that pipelines its first bytes (e.g. a TLS ClientHello) onto
	// the same write as the CONNECT request can have them land in the
	// Hijacked bufio.Reader's buffer rather than still on the wire: flush
	// those upstream before wiring up the copies, or they're silently lost.
	if rw != nil && rw.Reader.Buffered() > 0 {
		buffered := make([]byte, rw.Reader.Buffered())
		if _, err := io.ReadFull(rw.Reader, buffered); err != nil {
			return
		}
		if _, err := upstream.Write(buffered); err != nil {
			return
		}
	}

	// When one side stops sending, pass that on as a half-close and keep the
	// other direction open: a client that shuts down its write side after its
	// request still gets the whole response. From then on the remaining
	// direction must keep data moving, or it's closed after
	// s.halfCloseIdle().
	idle := s.halfCloseIdle()
	var finishing atomic.Bool
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, idleReader{client, idle, &finishing})
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, idleReader{upstream, idle, &finishing})
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	finishing.Store(true)
	// The remaining copy may already be blocked in a read with no deadline.
	deadline := time.Now().Add(idle)
	client.SetReadDeadline(deadline)
	upstream.SetReadDeadline(deadline)
	<-done
}

// idleReader reads from c, and once *finishing is set pushes c's read
// deadline idle ahead on every read: the tunnel closes when nothing moves
// for that long, not after a fixed total.
type idleReader struct {
	c         net.Conn
	idle      time.Duration
	finishing *atomic.Bool
}

func (r idleReader) Read(p []byte) (int, error) {
	if r.finishing.Load() {
		r.c.SetReadDeadline(time.Now().Add(r.idle))
	}
	return r.c.Read(p)
}

// closeWrite half-closes c if it supports that (TCP does), and fully closes
// it otherwise, so the peer always sees the end of the stream.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

// denied logs and refuses a destination the policy doesn't allow.
func (s *Server) denied(w http.ResponseWriter, target string) {
	s.logf("rnes-proxy: denied %s", target)
	http.Error(w, "rnes-proxy: host not allowed: "+target, http.StatusForbidden)
}

// hostport normalizes u's authority to an explicit "host:port", defaulting
// to port 80 the way a plain (non-TLS) HTTP request would.
func hostport(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "80")
}

// hopByHop are headers that describe a single connection, not the message,
// and must not be forwarded by a proxy (RFC 9110 §7.6.1).
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, f := range strings.Split(h.Get("Connection"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			h.Del(f)
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

// handleForward proxies a plain (non-CONNECT) absolute-URI request to an
// allowed host. The response is flushed to the client chunk by chunk, so
// streamed responses (llama-server's SSE completions) aren't held back.
// Only http:// is forwarded: https goes through CONNECT, and letting it in
// here would check the policy against hostport's port-80 default while the
// transport dials 443.
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" {
		http.Error(w, "rnes-proxy: only http:// URLs are forwarded; use CONNECT for https", http.StatusBadRequest)
		return
	}
	target := hostport(r.URL)
	if !s.Policy.Allows(target) {
		s.denied(w, target)
		return
	}
	start := time.Now()
	out := r.Clone(r.Context())
	out.RequestURI = "" // must be empty on a client request
	removeHopByHop(out.Header)

	resp, err := s.transportFor(target).RoundTrip(out)
	if err != nil {
		s.dialFailed(w, target, err)
		if !isAddressError(err) {
			s.logf("rnes-proxy: %s http://%s%s -> %d (%s): %s", r.Method, target, r.URL.EscapedPath(), http.StatusBadGateway, time.Since(start).Round(time.Millisecond), err.Error())
		}
		return
	}
	defer resp.Body.Close()
	relay(w, resp)
	s.logf("rnes-proxy: %s http://%s%s -> %d (%s)", r.Method, target, r.URL.EscapedPath(), resp.StatusCode, time.Since(start).Round(time.Millisecond))
}

// relay writes resp to w, flushing chunk by chunk so streamed responses
// (llama-server's SSE completions) aren't held back.
func relay(w http.ResponseWriter, resp *http.Response) {
	removeHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}
