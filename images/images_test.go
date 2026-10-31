package images

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// validName is what an image name may look like: it becomes part of the Docker
// repository name rnes-sandbox-<name>, which must be lowercase alphanumerics with
// single separators, or `docker build` fails with an unhelpful tag error.
var validName = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

func TestImageNamesAreValidDockerRepositoryNames(t *testing.T) {
	bases := BaseNames()
	if !reflect.DeepEqual(bases, []string{"base-agy", "base-opencode"}) {
		t.Fatalf("BaseNames() = %v, want [base-agy base-opencode]", bases)
	}
	for _, name := range append(bases, Names()...) {
		if !validName.MatchString(name) {
			t.Errorf("image name %q must match %s (it becomes rnes-sandbox-%s)", name, validName, name)
		}
	}
	// Guard the guard: names it must reject.
	for _, bad := range []string{"RE", "my image", "-x", "x-", "a--b", ""} {
		if validName.MatchString(bad) {
			t.Errorf("validName accepts %q", bad)
		}
	}
}

type instruction struct {
	name string // upper-cased
	args []string
}

// parseInstructions returns a Dockerfile's instructions with comments dropped
// and backslash continuations joined. It only needs to find FROM, ARG and USER.
func parseInstructions(src string) []instruction {
	var out []instruction
	var cur strings.Builder
	flush := func() {
		if f := strings.Fields(cur.String()); len(f) > 0 {
			out = append(out, instruction{name: strings.ToUpper(f[0]), args: f[1:]})
		}
		cur.Reset()
	}
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || (t == "" && cur.Len() == 0) {
			continue
		}
		if strings.HasSuffix(t, `\`) {
			cur.WriteString(strings.TrimSuffix(t, `\`) + " ")
			continue
		}
		cur.WriteString(t)
		flush()
	}
	flush()
	return out
}

func TestNamesListsOnlySelectableImages(t *testing.T) {
	got := Names()
	if !reflect.DeepEqual(got, []string{"default"}) {
		t.Errorf("Names() = %v, want [default]: the base images are internal", got)
	}
}

func TestReadRejectsUnknownNamesPathsAndTheBases(t *testing.T) {
	bad := append([]string{"nope", "", "base", "../images/default", "default.Dockerfile", "sub/default"}, BaseNames()...)
	for _, name := range bad {
		if _, err := Read(name); err == nil {
			t.Errorf("Read(%q) = nil error, want one", name)
		}
	}
	if _, err := Read(Default); err != nil {
		t.Errorf("Read(%q): %v", Default, err)
	}
}

func TestReadBase(t *testing.T) {
	opencodeRaw, err := ReadBase("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(opencodeRaw), "FROM node:") {
		t.Error("opencode base must be a standalone node image")
	}

	agyRaw, err := ReadBase("agy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agyRaw), "FROM debian:bookworm-slim") {
		t.Error("agy base must be a standalone debian:bookworm-slim image")
	}

	for _, bad := range []string{"", "nope", "../default"} {
		if _, err := ReadBase(bad); err == nil {
			t.Errorf("ReadBase(%q) = nil error, want one", bad)
		}
	}
}

// TestBundledImagesFollowTheContract checks every shipped Dockerfile, since
// rnes builds them as-is with no runtime rewriting: each base is standalone;
// every selectable image is a layer that declares ARG RNES_BASE, starts its
// final stage FROM ${RNES_BASE} (which rnes passes as a build arg) and hands
// back to user rnes.
func TestBundledImagesFollowTheContract(t *testing.T) {
	wantEntrypoints := map[string]string{
		"base-opencode": `ENTRYPOINT ["opencode"]`,
		"base-agy":      `ENTRYPOINT ["agy"]`,
	}
	all := append(BaseNames(), Names()...)
	for _, name := range all {
		t.Run(name, func(t *testing.T) {
			var raw []byte
			var err error
			isBase := strings.HasPrefix(name, BasePrefix)
			if isBase {
				raw, err = ReadBase(strings.TrimPrefix(name, BasePrefix))
			} else {
				raw, err = Read(name)
			}
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			if isBase {
				if strings.Contains(src, "RNES_BASE") {
					t.Error("a base must not depend on RNES_BASE")
				}
				if want, ok := wantEntrypoints[name]; ok && !strings.Contains(src, want) {
					t.Errorf("%s must contain %s", name, want)
				}
			}

			var (
				sawFrom, argBeforeFrom bool
				finalFrom, lastUser    string
			)
			for _, in := range parseInstructions(src) {
				switch in.name {
				case "FROM":
					sawFrom = true
					finalFrom = ""
					for _, a := range in.args {
						if !strings.HasPrefix(a, "--") {
							finalFrom = a
							break
						}
					}
					lastUser = "" // each stage starts from its base's user
				case "ARG":
					for _, a := range in.args {
						if n, _, _ := strings.Cut(a, "="); n == "RNES_BASE" && !sawFrom {
							argBeforeFrom = true
						}
					}
				case "USER":
					if len(in.args) > 0 {
						lastUser = in.args[0]
					}
				}
			}
			if !sawFrom {
				t.Fatal("no FROM instruction")
			}
			if isBase {
				if lastUser != "rnes" {
					t.Errorf("a base must end as USER rnes, got %q", lastUser)
				}
				return
			}
			if !argBeforeFrom {
				t.Error("a layer must declare ARG RNES_BASE before its first FROM")
			}
			if finalFrom != "${RNES_BASE}" && finalFrom != "$RNES_BASE" {
				t.Errorf("a layer's final stage must be FROM ${RNES_BASE}, got FROM %s", finalFrom)
			}
			if lastUser != "rnes" {
				t.Errorf("a layer must end as USER rnes (after any USER root), got %q", lastUser)
			}
		})
	}
}
