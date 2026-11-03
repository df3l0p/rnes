package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ParsePolicy reads a Policy in its text form: one "host:port" per line,
// "no-inspect host:port" for an entry that bypasses TLS interception,
// "private host:port" for an entry that may resolve to a private address
// (RFC 1918 / IPv6 ULA) as well as a public one, or a lone "*" to also allow
// any public host (Policy.AnyPublic); the two prefixes can be combined in
// either order (e.g. "no-inspect private host:port"). Blank lines and
// everything after a '#' are ignored. A malformed line is an error — callers
// must never fall back to allow-all, or to a policy that differs from what was
// written.
func ParsePolicy(r io.Reader) (Policy, error) {
	return parsePolicy(r, true)
}

// ParseUserPolicy reads a user-managed policy file (.rnes/policy.txt): one
// explicit "host:port", "private host:port" or "no-inspect host:port" per
// line, with '#' comments and blank lines ignored; the two prefixes can be
// combined in either order (e.g. "no-inspect private host:port"). Unlike
// ParsePolicy, it rejects "*" (wildcard public egress is gated by the
// -allow-public CLI flag). A "private" entry opts that one endpoint into
// RFC 1918 / IPv6 ULA addresses; -allow-private grants unlisted private
// destinations separately.
func ParseUserPolicy(r io.Reader) (Policy, error) {
	return parsePolicy(r, false)
}

func parsePolicy(r io.Reader, allowStar bool) (Policy, error) {
	var hostports, privates, noInspects []string
	anyPublic := false
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "*" {
			if !allowStar {
				return Policy{}, fmt.Errorf("line %d: \"*\" is not allowed in policy.txt; pass -allow-public on the command line instead", n)
			}
			anyPublic = true
			continue
		}
		privateMark, noInspectMark := false, false
		for {
			if rest, ok := strings.CutPrefix(line, "private "); ok {
				privateMark = true
				line = strings.TrimSpace(rest)
				continue
			}
			if rest, ok := strings.CutPrefix(line, "no-inspect "); ok {
				noInspectMark = true
				line = strings.TrimSpace(rest)
				continue
			}
			break
		}
		if err := validateHostPort(line); err != nil {
			return Policy{}, fmt.Errorf("line %d: %w", n, err)
		}
		hostports = append(hostports, line)
		if privateMark {
			privates = append(privates, line)
		}
		if noInspectMark {
			noInspects = append(noInspects, line)
		}
	}
	if err := sc.Err(); err != nil {
		return Policy{}, err
	}
	pol := NewPolicy(hostports)
	for _, hp := range privates {
		pol.MarkPrivate(hp)
	}
	for _, hp := range noInspects {
		pol.MarkNoInspect(hp)
	}
	pol.AnyPublic = anyPublic
	return pol, nil
}

func validateHostPort(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" || strings.ContainsAny(s, " \t") {
		return fmt.Errorf("%q is not host:port", s)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%q has an invalid port", s)
	}
	return nil
}

// SortedAllow lists the policy's explicit "host:port" entries in sorted order.
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

// LoadPolicy reads a Policy from a file in ParsePolicy's format. A missing
// or malformed file is an error.
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
