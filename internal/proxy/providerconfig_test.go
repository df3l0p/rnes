package proxy

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestEncodeCredentialsIsOneNewlineTerminatedLine(t *testing.T) {
	b, err := EncodeCredentials(
		[]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "rnes_placeholder_1"}},
		map[string]string{"EXAMPLE_TOKEN": "secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("\n")) || bytes.Count(b, []byte("\n")) != 1 {
		t.Errorf("want exactly one trailing newline, got %q", b)
	}
	if !strings.Contains(string(b), "EXAMPLE_TOKEN") || !strings.Contains(string(b), "secret") {
		t.Errorf("credentials line = %q", b)
	}
}

func TestReadCredentialsRoundTripsAndBuildsTheGitHubProvider(t *testing.T) {
	b, _ := EncodeCredentials(
		[]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "rnes_placeholder_1"}},
		map[string]string{"EXAMPLE_TOKEN": "secret"},
	)
	// More input may follow the line (stdin isn't closed): only one line is read.
	ps, secrets, err := ReadCredentials(bufio.NewReader(bytes.NewReader(append(b, []byte("trailing")...))))
	if err != nil {
		t.Fatalf("ReadCredentials: %v", err)
	}
	if len(ps) != 1 || ps[0].Name() != "github" || ps[0].Placeholder() != "rnes_placeholder_1" {
		t.Fatalf("providers = %+v", ps)
	}
	if got := secrets["EXAMPLE_TOKEN"]; got != "secret" {
		t.Errorf("secrets = %v", secrets)
	}
}

func TestReadCredentialsRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":            "\n",
		"not json":         "nope\n",
		"unknown provider": `{"providers":[{"name":"gitlab","token":"t","placeholder":"p"}]}` + "\n",
		"bad token":        `{"providers":[{"name":"github","token":"a b","placeholder":"p"}]}` + "\n",
		"no credentials":   `{}` + "\n",
		"no newline":       `{}`,
		"bad env name":     `{"secrets":{"1TOKEN":"x"}}` + "\n",
		"empty secret":     `{"secrets":{"TOKEN":""}}` + "\n",
		"secret newline":   `{"secrets":{"TOKEN":"a\nb"}}` + "\n",
		"unknown field":    `{"secrets":{"TOKEN":"x"},"extra":1}` + "\n",
		"second document":  `{"secrets":{"TOKEN":"x"}} {}` + "\n",
		"duplicate name": `{"providers":[{"name":"github","token":"a","placeholder":"p1"},` +
			`{"name":"github","token":"b","placeholder":"p2"}]}` + "\n",
		"duplicate placeholder": `{"providers":[{"name":"github","token":"a","placeholder":"p"},` +
			`{"name":"other","token":"b","placeholder":"p"}]}` + "\n",
	} {
		if _, _, err := ReadCredentials(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReadCredentialsErrorsNeverEchoTheToken(t *testing.T) {
	in := `{"providers":[{"name":"github","token":"SECRET VALUE","placeholder":"p"}]}` + "\n"
	_, _, err := ReadCredentials(bufio.NewReader(strings.NewReader(in)))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v; must exist and not contain the token", err)
	}

	// The \n is a valid JSON escape that decodes to a newline; validateSecret
	// must reject the control character, and the error must not echo it.
	in = `{"secrets":{"TOKEN":"SECRET\nVALUE"}}` + "\n"
	_, _, err = ReadCredentials(bufio.NewReader(strings.NewReader(in)))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v; must exist and not contain the secret", err)
	}
}

// The duplicate checks must be what rejects these (the second entry names a
// provider that doesn't exist, so a later check would also fail them).
func TestReadCredentialsNamesTheDuplicate(t *testing.T) {
	for in, want := range map[string]string{
		`{"providers":[{"name":"github","token":"a","placeholder":"p1"},{"name":"github","token":"b","placeholder":"p2"}]}` + "\n": "listed twice",
		`{"providers":[{"name":"github","token":"a","placeholder":"p"},{"name":"other","token":"b","placeholder":"p"}]}` + "\n":    "share a placeholder",
	} {
		_, _, err := ReadCredentials(bufio.NewReader(strings.NewReader(in)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one containing %q", err, want)
		}
	}
}

func TestInjectorFromStdinBuildsTheInjectorForExplicitHosts(t *testing.T) {
	line, _ := EncodeCredentials(
		[]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "rnes_placeholder_1"}},
		map[string]string{"EXAMPLE_TOKEN": "secret"},
	)
	pol := NewPolicy([]string{"github.com:443", "api.github.com:443"})
	pol.SetAuth("api.example.com:443", AuthConfig{Key: "Authorization", Value: "EXAMPLE_TOKEN", Format: "Bearer {value}"})
	in, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), pol, nil)
	if err != nil {
		t.Fatalf("InjectorFromStdin: %v", err)
	}
	if len(in.Providers) != 1 || in.Providers[0].Name() != "github" {
		t.Errorf("providers = %+v", in.Providers)
	}
	if got := in.Secrets["EXAMPLE_TOKEN"]; got != "secret" {
		t.Errorf("secrets = %v", in.Secrets)
	}
}

// A bound host that a custom policy omits (or allows only via *) is logged
// rather than failing proxy startup: the proxy itself blocks omitted hosts
// (and never decrypts *-only hosts, so the credential is never injected there).
func TestInjectorFromStdinLogsBoundHostsThePolicyDoesNotList(t *testing.T) {
	line, _ := EncodeCredentials(
		[]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "rnes_placeholder_1"}},
		nil,
	)
	pol := NewPolicy([]string{"github.com:443"}) // no api.github.com
	audit := &auditLog{}
	if _, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), pol, audit.Logf); err != nil {
		t.Fatalf("InjectorFromStdin: %v", err)
	}
	if got := audit.String(); !strings.Contains(got, "provider github: api.github.com:443") || strings.Contains(got, "provider github: github.com:443") {
		t.Errorf("log = %q, want note for api.github.com:443 only", got)
	}

	// "*" allows the host as a blind tunnel, not as an explicit decrypted entry.
	any := Policy{Allow: map[string]bool{"github.com:443": true}, AnyPublic: true}
	audit2 := &auditLog{}
	if _, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), any, audit2.Logf); err != nil {
		t.Fatalf("InjectorFromStdin with *: %v", err)
	}
	if !strings.Contains(audit2.String(), "api.github.com:443") {
		t.Errorf("a host allowed only by * must still be logged as not decrypted, got %q", audit2.String())
	}

	// A host marked no-inspect is also blind-tunnelled, so credentials won't be injected there.
	noInsp := NewPolicy([]string{"github.com:443", "api.github.com:443"})
	noInsp.MarkNoInspect("api.github.com:443")
	audit3 := &auditLog{}
	if _, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), noInsp, audit3.Logf); err != nil {
		t.Fatalf("InjectorFromStdin with no-inspect: %v", err)
	}
	if !strings.Contains(audit3.String(), "provider github: api.github.com:443") {
		t.Errorf("a no-inspect host must be logged as not decrypted, got %q", audit3.String())
	}
}

// A policy auth entry without a secret is inert; startup says so instead of
// failing, since the defaults list optional cloud providers whose key may not
// be set.
func TestInjectorFromStdinLogsAuthEntriesWithoutSecrets(t *testing.T) {
	line, _ := EncodeCredentials(nil, map[string]string{"OTHER": "x"})
	pol := NewPolicy([]string{"api.example.com:443"})
	pol.SetAuth("api.example.com:443", AuthConfig{Key: "Authorization", Value: "EXAMPLE_TOKEN"})
	audit := &auditLog{}
	if _, err := InjectorFromStdin(bufio.NewReader(bytes.NewReader(line)), pol, audit.Logf); err != nil {
		t.Fatalf("InjectorFromStdin: %v", err)
	}
	if got := audit.String(); !strings.Contains(got, "EXAMPLE_TOKEN") || !strings.Contains(got, "api.example.com:443") {
		t.Errorf("log = %q, want a note naming the missing variable and host", got)
	}
}

func TestInjectorFromStdinPassesTheReadErrors(t *testing.T) {
	_, err := InjectorFromStdin(bufio.NewReader(strings.NewReader("nope\n")), NewPolicy(nil), nil)
	if err == nil {
		t.Error("want an error for unreadable config")
	}
}

func TestReadCredentialsBuildsGitHubFromTheLine(t *testing.T) {
	b, _ := EncodeCredentials(
		[]ProviderConfig{{Name: "github", Token: "github_pat_x", Placeholder: "p1"}},
		map[string]string{"RNES_OPENROUTER_API_KEY": "sk-or-x"},
	)
	ps, secrets, err := ReadCredentials(bufio.NewReader(bytes.NewReader(b)))
	if err != nil || len(ps) != 1 {
		t.Fatalf("providers = %+v, %v", ps, err)
	}
	if ps[0].Name() != "github" || strings.Join(ps[0].Hosts(), ",") != "github.com:443,api.github.com:443" {
		t.Errorf("github provider = %s %v", ps[0].Name(), ps[0].Hosts())
	}
	if secrets["RNES_OPENROUTER_API_KEY"] != "sk-or-x" {
		t.Errorf("secrets = %v", secrets)
	}
}
