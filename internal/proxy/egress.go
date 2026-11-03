package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errNotPublic is returned when a destination allowed only by the policy's *
// resolves to an address that isn't public.
var errNotPublic = errors.New("not a public address")

// errNotPrivate is returned when -allow-private lets a destination through but
// it resolves somewhere outside the private ranges (a public address, loopback,
// link-local metadata, CGNAT, or multicast).
var errNotPrivate = errors.New("not a private address")

// errNotPublicOrPrivate is returned when both -allow-public and -allow-private
// are on but an address is neither.
var errNotPublicOrPrivate = errors.New("not a public or private address")

// cgnat is 100.64.0.0/10 (RFC 6598): carrier-grade NAT, and the range
// Tailscale and some VPNs hand out, so never "the internet".
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPublic reports whether ip is a public unicast address: not loopback,
// link-local, multicast, unspecified, private (RFC 1918 / unique local), or
// CGNAT. It's what a policy's * lets through, keeping the host machine and
// the LAN out of reach even by IP.
func isPublic(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && cgnat.Contains(ip4) {
		return false
	}
	return true
}

// isPrivate reports whether ip is a private address as -allow-private defines
// it: RFC 1918 (10/8, 172.16/12, 192.168/16) or IPv6 unique-local (fc00::/7).
// Loopback, link-local (including cloud metadata 169.254.169.254), CGNAT and
// multicast deliberately stay out: they're not LAN addresses, and link-local
// is the SSRF target the public-only default exists to keep closed.
func isPrivate(ip net.IP) bool {
	return ip != nil && ip.IsPrivate()
}

// addressControl builds a dialer Control that refuses an address allow rejects,
// after DNS resolution so a listed name can't rebind its way through.
func addressControl(allow func(net.IP) bool, err error) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return splitErr
		}
		if allow(net.ParseIP(host)) {
			return nil
		}
		return fmt.Errorf("%s: %w", host, err)
	}
}

// anyAddressDialer reaches entries marked any-address (internal/test use): it
// skips the address-type check entirely, loopback and link-local included. It
// only dials; TLS interception is decided separately (see interceptable).
var anyAddressDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

// publicDialer reaches all other destinations — both explicit policy entries
// and hosts allowed by *. It checks the address it's actually connecting to,
// after DNS resolution, so a name in policy.txt that resolves (or rebinds) to
// a private or link-local metadata address can't slip through.
var publicDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	Control:   addressControl(isPublic, errNotPublic),
}

// privateDialer reaches destinations opened only by -allow-private, after the
// address resolves into a private range.
var privateDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	Control:   addressControl(isPrivate, errNotPrivate),
}

// publicPrivateDialer reaches per-entry private marks and any host when both
// -allow-public and -allow-private are on: either address type is fine.
var publicPrivateDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: addressControl(func(ip net.IP) bool {
		return isPublic(ip) || isPrivate(ip)
	}, errNotPublicOrPrivate),
}

// transportWith is forwardTransport dialing through d.
func transportWith(d *net.Dialer) *http.Transport {
	t := forwardTransport.Clone()
	t.DialContext = d.DialContext
	return t
}

// publicTransport is forwardTransport dialing through publicDialer.
var publicTransport = transportWith(publicDialer)

// privateTransport and publicPrivateTransport are the -allow-private forward
// paths.
var (
	privateTransport       = transportWith(privateDialer)
	publicPrivateTransport = transportWith(publicPrivateDialer)
)

// dialerFor picks how to reach hostport: through the any-address dialer when
// the entry carries the internal/test mark; through the public+private dialer
// when it carries the per-entry private mark; under -allow-private, through a
// dialer that accepts private addresses for unlisted hosts (listed hosts keep
// their public-only check, unless -allow-public is also on); otherwise public
// addresses only.
func (s *Server) dialerFor(hostport string) *net.Dialer {
	if s.Policy.AllowsAnyAddress(hostport) {
		return anyAddressDialer
	}
	if s.Policy.AllowsPrivate(hostport) {
		return publicPrivateDialer
	}
	if s.Policy.AnyPrivate {
		if s.Policy.AnyPublic {
			return publicPrivateDialer
		}
		if s.Policy.Explicit(hostport) {
			return publicDialer
		}
		return privateDialer
	}
	return publicDialer
}

// transportFor is dialerFor for the forward path.
func (s *Server) transportFor(hostport string) *http.Transport {
	if s.Policy.AllowsAnyAddress(hostport) {
		return forwardTransport
	}
	if s.Policy.AllowsPrivate(hostport) {
		return publicPrivateTransport
	}
	if s.Policy.AnyPrivate {
		if s.Policy.AnyPublic {
			return publicPrivateTransport
		}
		if s.Policy.Explicit(hostport) {
			return publicTransport
		}
		return privateTransport
	}
	return publicTransport
}

// isAddressError reports whether err is an address-type refusal, which
// dialFailed has already logged as a denial.
func isAddressError(err error) bool {
	return errors.Is(err, errNotPublic) ||
		errors.Is(err, errNotPrivate) ||
		errors.Is(err, errNotPublicOrPrivate)
}

// dialFailed answers a request whose upstream couldn't be reached: 403 when
// the address-type rule refused it, 502 otherwise.
func (s *Server) dialFailed(w http.ResponseWriter, target string, err error) {
	switch {
	case errors.Is(err, errNotPublic):
		s.denied(w, target+" (not a public address)")
	case errors.Is(err, errNotPrivate):
		s.denied(w, target+" (not a private address)")
	case errors.Is(err, errNotPublicOrPrivate):
		s.denied(w, target+" (not a public or private address)")
	default:
		http.Error(w, "rnes-proxy: dial upstream: "+err.Error(), http.StatusBadGateway)
	}
}
