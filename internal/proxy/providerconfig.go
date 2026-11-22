package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
)

// ProviderConfig is one placeholder-based provider as rnes hands it to
// rnes-proxy on stdin. Only GitHub needs this: its credential is a
// placeholder substituted into a Basic-auth password.
type ProviderConfig struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	Placeholder string `json:"placeholder"`
}

// credentialsMessage is the single line rnes writes to rnes-proxy's stdin. The
// policy file names the environment variables; this message carries their
// values, so secrets never appear in an argument, an environment variable or a
// file.
type credentialsMessage struct {
	Providers []ProviderConfig  `json:"providers,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
}

// EncodeCredentials renders providers and secrets as the single
// newline-terminated JSON line ReadCredentials expects.
func EncodeCredentials(providers []ProviderConfig, secrets map[string]string) ([]byte, error) {
	b, err := json.Marshal(credentialsMessage{Providers: providers, Secrets: secrets})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ReadCredentials reads one newline-terminated line holding a JSON
// credentialsMessage and builds the providers. It stops at the newline, so the
// sender need not close the stream. Provider names and placeholders must be
// unique: the placeholder is how the proxy tells whose credential to inject,
// so a shared one would be ambiguous. Secrets are keyed by the environment
// variable name a policy entry's value names. Errors never include the line's
// content: it holds secrets.
func ReadCredentials(r *bufio.Reader) ([]Provider, map[string]string, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, nil, errors.New("credential config isn't newline-terminated")
		}
		return nil, nil, fmt.Errorf("reading credential config: %w", err)
	}
	var msg credentialsMessage
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&msg); err != nil {
		return nil, nil, errors.New("credential config isn't valid JSON")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("credential config must contain exactly one JSON object")
	}
	if len(msg.Providers) == 0 && len(msg.Secrets) == 0 {
		return nil, nil, errors.New("credential config lists no credentials")
	}
	names, placeholders := map[string]bool{}, map[string]bool{}
	for _, c := range msg.Providers {
		if names[c.Name] {
			return nil, nil, fmt.Errorf("provider %q is listed twice", c.Name)
		}
		if placeholders[c.Placeholder] {
			return nil, nil, errors.New("two providers share a placeholder")
		}
		names[c.Name], placeholders[c.Placeholder] = true, true
	}
	ps := make([]Provider, 0, len(msg.Providers))
	for _, c := range msg.Providers {
		p, err := BuildProvider(c)
		if err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
	}
	for name, secret := range msg.Secrets {
		if err := validateEnvVar(name); err != nil {
			return nil, nil, fmt.Errorf("secret %q: %w", name, err)
		}
		if err := validateSecret(secret); err != nil {
			return nil, nil, fmt.Errorf("secret %q: %w", name, err)
		}
	}
	return ps, msg.Secrets, nil
}

// validateSecret rejects secrets that can't be written into an HTTP header
// safely. Spaces are allowed (some schemes embed them); CR, LF and other
// control characters are not, since they could inject another header.
func validateSecret(s string) error {
	if s == "" {
		return errors.New("the secret is empty")
	}
	for _, r := range s {
		if r == '\r' || r == '\n' || r == 0 || (unicode.IsControl(r) && r != '\t') {
			return errors.New("the secret contains a control character")
		}
	}
	return nil
}

// InjectorFromStdin reads the credential config from r (see ReadCredentials)
// and returns the Injector for it, logging to logf. A bound host not listed
// explicitly in pol is noted in the log rather than failing startup, so a
// custom per-project policy can omit a provider's hosts while the credential
// remains set in the operator's environment: omitted hosts are blocked by pol
// (or blind-tunnelled under -allow-public, without decrypting or injecting the
// secret). A policy auth entry whose environment variable has no secret is
// noted too; its requests are forwarded without authentication.
func InjectorFromStdin(r *bufio.Reader, pol Policy, logf func(string, ...any)) (*Injector, error) {
	providers, secrets, err := ReadCredentials(r)
	if err != nil {
		return nil, err
	}
	in := &Injector{Providers: providers, Policy: pol, Secrets: secrets, Logf: logf}
	for _, p := range providers {
		for _, h := range p.Hosts() {
			if !pol.Explicit(h) || pol.SkipsInspect(h) {
				in.logf("rnes-proxy: provider %s: %s is not listed as an intercepted host in policy; credential will not be injected there", p.Name(), h)
			}
		}
	}
	for _, h := range pol.SortedAuthHosts() {
		if secrets[pol.Auth[h].Value] == "" {
			in.logf("rnes-proxy: auth %s: no secret was provided for %s; requests to %s won't be authenticated", pol.Auth[h].Value, pol.Auth[h].Key, h)
		}
	}
	return in, nil
}

// BuildProvider turns a config entry into its Provider ("github").
func BuildProvider(c ProviderConfig) (Provider, error) {
	switch c.Name {
	case "github":
		return NewGitHubProvider(c.Token, c.Placeholder)
	default:
		return nil, fmt.Errorf("unknown provider %q", c.Name)
	}
}
