// Package opencodeconfig merges a provider block into opencode's JSONC
// config file without disturbing any of its other keys.
package opencodeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/df3l0p/rnes/internal/configfile"
)

// Provider is the shape opencode expects for a custom OpenAI-compatible
// provider entry under the top-level "providers" key. Package is the
// provider's npm package, prefixed with "aisdk:" when it is an AI SDK
// package (as @ai-sdk/openai-compatible is).
type Provider struct {
	Package  string                 `json:"package"`
	Name     string                 `json:"name"`
	Settings map[string]interface{} `json:"settings"`
	Models   map[string]interface{} `json:"models"`
}

// Merge reads the JSONC config at path (treating a missing file as "{}"),
// sets provider[providerKey] to the given block, and writes the result back.
// All other keys in the file are left untouched. Comments in the original
// file are not preserved.
//
// The read-modify-write goes through configfile.Update, so a concurrent rnes
// process can't drop this one's change and a crash mid-write can't leave the
// config truncated.
func Merge(path, providerKey string, provider Provider) error {
	return configfile.Update(path, func(raw []byte) ([]byte, error) {
		return render(raw, providerKey, provider)
	})
}

// Render returns the config at path (a missing file counts as "{}") with
// provider[providerKey] set to the given block, as JSON, without writing
// anything. It's Merge's read-and-modify half, for callers that want a
// modified copy of the config rather than to change the original.
func Render(path, providerKey string, provider Provider) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	out, err := render(raw, providerKey, provider)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func render(raw []byte, providerKey string, provider Provider) ([]byte, error) {
	config := map[string]interface{}{}
	if stripped := stripJSONComments(raw); len(bytes.TrimSpace(stripped)) > 0 {
		if err := json.Unmarshal(stripped, &config); err != nil {
			return nil, fmt.Errorf("parsing: %w", err)
		}
	}

	providers, _ := config["providers"].(map[string]interface{})
	if providers == nil {
		providers = map[string]interface{}{}
	}

	providerBlock, err := toMap(provider)
	if err != nil {
		return nil, fmt.Errorf("encoding provider block: %w", err)
	}
	providers[providerKey] = providerBlock
	config["providers"] = providers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), nil
}

func toMap(provider Provider) (map[string]interface{}, error) {
	raw, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// stripJSONComments removes // line comments and /* */ block comments from
// JSONC input, leaving string literals untouched, so the result can be fed
// to encoding/json.
func stripJSONComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(src); i++ {
		c := src[i]
		var next byte
		if i+1 < len(src) {
			next = src[i+1]
		}

		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				out = append(out, c)
			}
		case inBlockComment:
			if c == '*' && next == '/' {
				inBlockComment = false
				i++
			}
		case inString:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				out = append(out, next)
				i++
				continue
			}
			if c == '"' {
				inString = false
			}
		default:
			switch {
			case c == '"':
				inString = true
				out = append(out, c)
			case c == '/' && next == '/':
				inLineComment = true
				i++
			case c == '/' && next == '*':
				inBlockComment = true
				i++
			default:
				out = append(out, c)
			}
		}
	}
	return out
}

// DropProviderAPIKeys removes every provider's apiKey from the JSON config in
// raw — providers[id].settings.apiKey in the v2 shape and the legacy
// provider[id].options.apiKey written before the v2 config migration — and
// returns the re-encoded config with the sorted ids whose key was dropped
// (nil when none was). Every other option in the provider blocks is
// untouched: the sandbox's copy of the config must carry no key at all.
func DropProviderAPIKeys(raw []byte) ([]byte, []string, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return raw, nil, fmt.Errorf("parsing: %w", err)
	}
	dropped := map[string]bool{}
	// Both locations are always tried: a config can carry a key in each.
	for _, loc := range [...][2]string{{"providers", "settings"}, {"provider", "options"}} {
		byID, _ := config[loc[0]].(map[string]interface{})
		for id, v := range byID {
			block, _ := v.(map[string]interface{})
			values, _ := block[loc[1]].(map[string]interface{})
			if _, ok := values["apiKey"]; ok {
				delete(values, "apiKey")
				dropped[id] = true
			}
		}
	}
	if len(dropped) == 0 {
		return raw, nil, nil
	}
	ids := make([]string, 0, len(dropped))
	for id := range dropped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return raw, nil, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), ids, nil
}

// SetModel sets the top-level "model" default in the JSON config in raw, in
// opencode's "provider/model" format. The full TUI takes no model flag, so
// this is how a session starts on a chosen model without touching any
// config that outlives it.
func SetModel(raw []byte, model string) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	config["model"] = model
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	return append(out, '\n'), nil
}
