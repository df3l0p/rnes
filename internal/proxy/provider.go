package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// Provider supplies a credential the proxy injects into requests to the hosts
// it is bound to. The sandbox only ever holds Placeholder; the proxy swaps in
// Credential on the way out.
type Provider interface {
	// Name identifies the provider in logs ("github").
	Name() string
	// Hosts are the "host:port" CONNECT targets the credential may be sent to.
	Hosts() []string
	// Placeholder is the opaque value the sandbox is given in place of the
	// credential.
	Placeholder() string
	// Credential is the current secret. A refreshing provider renews it here.
	Credential(ctx context.Context) (string, error)
}

// StaticProvider is a Provider whose credential never changes.
type StaticProvider struct {
	name        string
	hosts       []string
	token       string
	placeholder string
}

// ValidateToken rejects tokens that can't be sent safely in a header: empty,
// or containing whitespace or control characters (a trailing newline from a
// copy-paste is the usual culprit).
func ValidateToken(token string) error {
	if token == "" {
		return errors.New("the token is empty")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("the token contains whitespace or a control character (a trailing newline?)")
		}
	}
	return nil
}

// NewStaticProvider builds a provider for a fixed token.
func NewStaticProvider(name string, hosts []string, token, placeholder string) (*StaticProvider, error) {
	if err := ValidateToken(token); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if placeholder == "" {
		return nil, fmt.Errorf("%s: the placeholder is empty", name)
	}
	return &StaticProvider{name: name, hosts: hosts, token: token, placeholder: placeholder}, nil
}

// NewGitHubProvider is the GitHub token provider: git over https
// (github.com) and the API that gh uses (api.github.com). It stays a
// placeholder-based provider rather than a policy key/value entry because git
// sends its credential as an HTTP Basic password, which a header template
// can't express. Providers such as OpenRouter and Mistral, whose clients send
// a plain Bearer header, use the policy's key/value/format instead.
func NewGitHubProvider(token, placeholder string) (*StaticProvider, error) {
	return NewStaticProvider("github", []string{"github.com:443", "api.github.com:443"}, token, placeholder)
}

func (p *StaticProvider) Name() string        { return p.name }
func (p *StaticProvider) Hosts() []string     { return p.hosts }
func (p *StaticProvider) Placeholder() string { return p.placeholder }
func (p *StaticProvider) Credential(context.Context) (string, error) {
	return p.token, nil
}

// CredentialError reports that a provider couldn't produce its credential.
// The proxy answers 502 for it. Error() deliberately leaves out Err: it may
// carry details of the secret's source.
type CredentialError struct {
	Provider string
	Err      error
}

func (e *CredentialError) Error() string { return e.Provider + ": credential unavailable" }
func (e *CredentialError) Unwrap() error { return e.Err }

// Injector is a Server.OnRequest hook that swaps providers' placeholders for
// their credentials, and fills in the headers described by the policy's
// key/value entries.
type Injector struct {
	Providers []Provider
	// Policy supplies the key/value auth entries. Secrets holds the values of
	// the environment variables those entries name; a missing entry leaves the
	// request untouched (its placeholder, if any, stays inert).
	Policy  Policy
	Secrets map[string]string
	// Logf receives one audit line per injection or refusal; nil discards them.
	// Lines name the provider or environment variable and host only: never the
	// secret, the placeholder or the query string.
	Logf func(format string, args ...any)
}

func (in *Injector) logf(format string, args ...any) {
	if in.Logf != nil {
		in.Logf(format, args...)
	}
}

// OnRequest implements Server.OnRequest. At a host a provider is bound to, it
// replaces the provider's placeholder in the request's Authorization header
// with the credential. That is the only place a provider credential is ever
// written: a placeholder anywhere else, or at any other host, is left as it is
// and so stays inert (the upstream just rejects it).
//
// A host with a policy key/value entry gets that entry's header set instead:
// the secret from the environment variable the entry names, rendered through
// the entry's format. It is written unconditionally, replacing whatever value
// the sandbox sent, so a client only needs to be configured, not to mimic a
// placeholder.
func (in *Injector) OnRequest(r *http.Request, target string) error {
	auth := r.Header.Get("Authorization")
	for _, p := range in.Providers {
		if !bound(p, target) || !authCarries(auth, p.Placeholder()) {
			continue
		}
		secret, err := p.Credential(r.Context())
		if err != nil {
			in.logf("rnes-proxy: provider %s: credential unavailable for %s", p.Name(), target)
			return &CredentialError{Provider: p.Name(), Err: err}
		}
		auth = substitute(auth, p.Placeholder(), secret)
		r.Header.Set("Authorization", auth)
		in.logf("rnes-proxy: provider %s: credential injected for %s", p.Name(), target)
	}
	if a, ok := in.Policy.AuthFor(target); ok {
		if secret := in.Secrets[a.Value]; secret != "" {
			r.Header.Set(a.Key, a.HeaderValue(secret))
			in.logf("rnes-proxy: auth %s: credential injected into %s for %s", a.Value, a.Key, target)
		}
	}
	return nil
}

func bound(p Provider, target string) bool {
	for _, h := range p.Hosts() {
		if strings.EqualFold(h, target) {
			return true
		}
	}
	return false
}

func authCarries(auth, ph string) bool {
	if auth == "" {
		return false
	}
	if raw, ok := basicPayload(auth); ok {
		return bytes.Contains(raw, []byte(ph))
	}
	return strings.Contains(auth, ph)
}

// basicPayload decodes a Basic Authorization value.
func basicPayload(auth string) ([]byte, bool) {
	scheme, rest, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	return raw, err == nil
}

// substitute replaces ph with secret in an Authorization value, re-encoding
// Basic credentials.
func substitute(auth, ph, secret string) string {
	if raw, ok := basicPayload(auth); ok {
		swapped := bytes.ReplaceAll(raw, []byte(ph), []byte(secret))
		return "Basic " + base64.StdEncoding.EncodeToString(swapped)
	}
	return strings.ReplaceAll(auth, ph, secret)
}
