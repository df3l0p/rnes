// Package images holds the sandbox images rnes ships, one <name>.Dockerfile per
// image.
//
// base-<harness> is each harness's internal foundation: it installs the agent
// and runs as a non-root user. None of the bases are offered to users. Every
// other image is a selectable layer on a base (`rnes -sandbox -image <name>`,
// default is Default): it declares `ARG RNES_BASE`, its final stage is
// `FROM ${RNES_BASE}` (rnes passes the built base image as that build arg), and
// it ends as `USER rnes`.
package images

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.Dockerfile
var files embed.FS

const suffix = ".Dockerfile"

const (
	// BasePrefix marks the internal per-harness base images
	// (base-<harness>.Dockerfile). None of them are selectable with -image.
	BasePrefix = "base-"
	// Default is the image used when none is chosen.
	Default = "default"
)

// BaseName returns the internal image name ("base-<harness>") for harness.
func BaseName(harness string) string {
	return BasePrefix + harness
}

// BaseNames lists all bundled internal base image names ("base-<harness>"),
// sorted.
func BaseNames() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err) // the embedded FS always has a root
	}
	var names []string
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), suffix); strings.HasPrefix(n, BasePrefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Names lists the selectable image names, sorted; internal base images are not
// among them.
func Names() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err) // the embedded FS always has a root
	}
	var names []string
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), suffix); !strings.HasPrefix(n, BasePrefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Read returns the Dockerfile of the named selectable image. Only exact names
// from Names resolve; anything else, including paths and internal bases, is an
// error listing what exists.
func Read(name string) ([]byte, error) {
	for _, n := range Names() {
		if n == name {
			return files.ReadFile(name + suffix)
		}
	}
	return nil, fmt.Errorf("unknown sandbox image %q (available: %s)", name, strings.Join(Names(), ", "))
}

// ReadBase returns the Dockerfile of the given harness's base image.
func ReadBase(harness string) ([]byte, error) {
	if harness == "" || strings.ContainsAny(harness, "/\\") {
		return nil, fmt.Errorf("unknown sandbox base for harness %q", harness)
	}
	return files.ReadFile(BaseName(harness) + suffix)
}
