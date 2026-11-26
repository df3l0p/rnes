package vibeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// mustRender is Render for tests that only care about the output.
func mustRender(t *testing.T, baseURL string, modelIDs []string) []byte {
	t.Helper()
	raw, err := Render(baseURL, modelIDs)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return raw
}

func TestRenderPointsAtTheLocalServerAndDisablesBackgroundEgress(t *testing.T) {
	raw := string(mustRender(t, "http://host.docker.internal:8080", []string{"m-one", "m-two"}))

	for _, want := range []string{
		`active_model = "m-one"`,
		"[[providers]]\nname = \"rnes-llamacpp\"\napi_base = \"http://host.docker.internal:8080/v1\"\napi_key_env_var = \"\"",
		"[[models]]\nname = \"m-one\"\nprovider = \"rnes-llamacpp\"\nalias = \"m-one\"",
		"[[models]]\nname = \"m-two\"\nprovider = \"rnes-llamacpp\"\nalias = \"m-two\"",
		"enable_telemetry = false",
		"enable_update_checks = false",
		"enable_auto_update = false",
		// With RNES_MISTRAL_API_KEY set, connectors would hand the agent the
		// operator's own Drive/GitHub/Linear/Slack.
		"enable_connectors = false",
		"[experiments]\nenable = false",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("Render output missing %q:\n%s", want, raw)
		}
	}
	// The sandbox config is generated, never copied: it must not carry the
	// managed block markers, which belong to the user's own file.
	if strings.Contains(raw, blockStart) {
		t.Errorf("Render output carries the host config's block markers:\n%s", raw)
	}
	if err := checkTOML([]byte(raw)); err != nil {
		t.Errorf("Render output is not valid TOML: %v\n%s", err, raw)
	}
}

func TestRenderWithoutModelsHasNoActiveModel(t *testing.T) {
	raw := string(mustRender(t, "http://127.0.0.1:8080", nil))
	if strings.Contains(raw, "active_model") {
		t.Errorf("Render(nil models) set an active model:\n%s", raw)
	}
	if !strings.Contains(raw, "[[providers]]") {
		t.Errorf("Render(nil models) dropped the provider:\n%s", raw)
	}
}

func TestRenderTrimsATrailingSlashFromTheBaseURL(t *testing.T) {
	raw := string(mustRender(t, "http://127.0.0.1:8080/", []string{"m"}))
	if !strings.Contains(raw, `api_base = "http://127.0.0.1:8080/v1"`) {
		t.Errorf("api_base not normalized:\n%s", raw)
	}
}

func TestRenderRejectsAModelIDThatIsNotUTF8(t *testing.T) {
	if _, err := Render("http://127.0.0.1:8080", []string{"m\xff"}); err == nil {
		t.Error("Render accepted a model id that is not valid UTF-8")
	}
}

func TestMergeReplacesTheBlockAndPreservesEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `# my own settings
theme = "auto"
active_model = "deepseek"

[tools.bash]
allowlist = [
    "git status",
    "ls",
]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, "http://127.0.0.1:8080", []string{"m-one"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// A second run with a different model list must replace, not append.
	if err := Merge(path, "http://127.0.0.1:8080", []string{"m-two"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.HasPrefix(got, original) {
		t.Errorf("the user's own settings were not preserved:\n%s", got)
	}
	if n := strings.Count(got, blockStart); n != 1 {
		t.Errorf("managed block appears %d times, want 1:\n%s", n, got)
	}
	if strings.Contains(got, `alias = "m-one"`) {
		t.Errorf("stale model entry survived the second Merge:\n%s", got)
	}
	if !strings.Contains(got, `alias = "m-two"`) {
		t.Errorf("new model entry missing:\n%s", got)
	}
	// The host session must not have its default model rewritten.
	if !strings.Contains(got, `active_model = "deepseek"`) {
		t.Errorf("Merge changed the user's active_model:\n%s", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("Merge did not preserve the file mode: %v, %v", fi.Mode(), err)
	}
}

func TestMergeCreatesTheFileAndItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "vibe", "config.toml")
	if err := Merge(path, "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), blockStart) {
		t.Errorf("a fresh config should start with the managed block:\n%s", raw)
	}
	if strings.Contains(string(raw), "active_model") {
		t.Errorf("a fresh config must not pin an active model:\n%s", raw)
	}
}

func TestMergeReplacesAnUnterminatedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// A hand edit that deleted the end marker must not leave the old block
	// behind, or the config would carry two providers.
	broken := "theme = \"auto\"\n" + blockStart + "\n[[providers]]\nname = \"rnes-llamacpp\"\n"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "[[providers]]"); n != 1 {
		t.Errorf("providers = %d, want 1:\n%s", n, raw)
	}
	if !strings.HasPrefix(string(raw), "theme = \"auto\"\n") {
		t.Errorf("the user's settings were not preserved:\n%s", raw)
	}
}

// A config that ended up with the block twice (a copy-paste, a merge tool) must
// be repaired: Vibe takes the first provider with a given name, so a stale
// block would silently win over the fresh one.
func TestMergeCollapsesADuplicatedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	block := managedBlock("http://127.0.0.1:9999", []string{"stale"})
	if err := os.WriteFile(path, []byte("theme = \"auto\"\n\n"+block+"\n"+block), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, "http://127.0.0.1:8080", []string{"fresh"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if n := strings.Count(got, blockStart); n != 1 {
		t.Errorf("managed block appears %d times, want 1:\n%s", n, got)
	}
	if strings.Contains(got, "stale") || strings.Contains(got, "9999") {
		t.Errorf("the stale block survived:\n%s", got)
	}
	if !strings.Contains(got, `alias = "fresh"`) {
		t.Errorf("the fresh block is missing:\n%s", got)
	}
}

// A model id that happens to contain a marker must not be read as one, or the
// second Merge would truncate the user's config at that line.
func TestMergeIgnoresAMarkerInsideAModelID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Merge(path, "http://127.0.0.1:8080", []string{"weird " + blockEnd + " id"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := Merge(path, "http://127.0.0.1:8080", []string{"m-two"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if n := strings.Count(got, blockStart); n != 1 {
		t.Errorf("managed block appears %d times, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, `alias = "m-two"`) {
		t.Errorf("the fresh block is missing:\n%s", got)
	}
	if err := checkTOML(raw); err != nil {
		t.Errorf("the merged config is not valid TOML: %v\n%s", err, raw)
	}
}

// A config rnes can't parse is left alone: appending to it would either be
// swallowed by the broken construct or make the error harder to find.
func TestMergeRefusesAConfigThatIsNotValidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	broken := "theme = \"auto\n"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Merge(path, "http://127.0.0.1:8080", []string{"m"})
	if err == nil {
		t.Fatal("Merge accepted a config that is not valid TOML")
	}
	if !strings.Contains(err.Error(), "parsing") {
		t.Errorf("error = %v, want it to name the parse failure", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != broken {
		t.Errorf("the broken config was rewritten:\n%s", raw)
	}
}

// ~/.vibe/config.toml is a prime candidate for a dotfiles symlink; rewriting
// the link itself would leave the real file stale.
func TestMergeWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "config.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("theme = \"auto\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Merge(link, "http://127.0.0.1:8080", []string{"m"}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("Merge replaced the symlink with a regular file")
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), blockStart) {
		t.Errorf("the real config did not get the block:\n%s", raw)
	}
}

func TestSetModelReplacesOnlyTheActiveModelLine(t *testing.T) {
	raw := mustRender(t, "http://127.0.0.1:8080", []string{"m-one", "m-two"})
	out, err := SetModel(raw, "m-two")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `active_model = "m-two"`) {
		t.Errorf("active_model not replaced:\n%s", got)
	}
	if strings.Contains(got, `active_model = "m-one"`) {
		t.Errorf("old active_model survived:\n%s", got)
	}
	// The model entries themselves are untouched.
	if !strings.Contains(got, `alias = "m-one"`) || !strings.Contains(got, `alias = "m-two"`) {
		t.Errorf("SetModel disturbed the model entries:\n%s", got)
	}
}

func TestSetModelAddsTheLineWhenMissing(t *testing.T) {
	out, err := SetModel([]byte("theme = \"auto\"\n"), "m")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if !strings.HasPrefix(string(out), `active_model = "m"`+"\n") {
		t.Errorf("SetModel did not prepend active_model:\n%s", out)
	}
}

// Render with no models leaves active_model out, so SetModel prepends it above
// the [experiments] table: still top-level, still valid TOML.
func TestSetModelPrependsAboveTheTables(t *testing.T) {
	raw := mustRender(t, "http://127.0.0.1:8080", nil)
	out, err := SetModel(raw, "m")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if !strings.HasPrefix(string(out), `active_model = "m"`+"\n") {
		t.Errorf("SetModel did not prepend active_model:\n%s", out)
	}
	if err := checkTOML(out); err != nil {
		t.Errorf("the config is not valid TOML after SetModel: %v\n%s", err, out)
	}
	var parsed struct {
		ActiveModel string `toml:"active_model"`
	}
	if _, err := toml.Decode(string(out), &parsed); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if parsed.ActiveModel != "m" {
		t.Errorf("active_model = %q, want m", parsed.ActiveModel)
	}
}

func TestSetModelIgnoresKeysThatMerelyStartWithTheSameWord(t *testing.T) {
	raw := []byte("active_model_extra = \"keep\"\nactive_model = \"old\"\n")
	out, err := SetModel(raw, "new")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `active_model_extra = "keep"`) {
		t.Errorf("SetModel rewrote an unrelated key:\n%s", got)
	}
	if !strings.Contains(got, `active_model = "new"`) {
		t.Errorf("SetModel did not rewrite active_model:\n%s", got)
	}
}

// active_model inside a table is a different key; only the top-level one is
// the session's model.
func TestSetModelIgnoresActiveModelInsideATable(t *testing.T) {
	raw := []byte("[tools.bash]\nactive_model = \"nested\"\n")
	out, err := SetModel(raw, "m")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `active_model = "nested"`) {
		t.Errorf("SetModel rewrote a nested key:\n%s", got)
	}
	if !strings.HasPrefix(got, `active_model = "m"`+"\n") {
		t.Errorf("SetModel did not prepend the top-level active_model:\n%s", got)
	}
}

func TestQuoteEscapesTOMLBasicStrings(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`with "quotes"`, `"with \"quotes\""`},
		{`back\slash`, `"back\\slash"`},
		{"tab\there", `"tab\there"`},
		{"bell\a", `"bell\u0007"`},
		{"unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M", `"unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M"`},
	} {
		if got := quote(tc.in); got != tc.want {
			t.Errorf("quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// The block rnes writes must parse as TOML on its own, whatever the model ids.
func TestManagedBlockIsValidTOML(t *testing.T) {
	block := managedBlock("http://127.0.0.1:8080", []string{`odd "id"`, "plain"})
	if err := checkTOML([]byte(block)); err != nil {
		t.Errorf("managed block is not valid TOML: %v\n%s", err, block)
	}
	var parsed struct {
		Providers []struct {
			Name string `toml:"name"`
		} `toml:"providers"`
		Models []struct {
			Alias string `toml:"alias"`
		} `toml:"models"`
	}
	if _, err := toml.Decode(block, &parsed); err != nil {
		t.Fatalf("decoding the block: %v", err)
	}
	if len(parsed.Providers) != 1 || parsed.Providers[0].Name != ProviderName {
		t.Errorf("providers = %+v, want one %q", parsed.Providers, ProviderName)
	}
	if len(parsed.Models) != 2 || parsed.Models[0].Alias != `odd "id"` {
		t.Errorf("models = %+v, want both ids with the quote intact", parsed.Models)
	}
}
