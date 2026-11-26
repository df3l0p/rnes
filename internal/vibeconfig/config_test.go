package vibeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPointsAtTheLocalServerAndDisablesBackgroundEgress(t *testing.T) {
	raw := string(Render("http://host.docker.internal:8080", []string{"m-one", "m-two"}))

	for _, want := range []string{
		`active_model = "m-one"`,
		"[[providers]]\nname = \"rnes-llamacpp\"\napi_base = \"http://host.docker.internal:8080/v1\"\napi_key_env_var = \"\"",
		"[[models]]\nname = \"m-one\"\nprovider = \"rnes-llamacpp\"\nalias = \"m-one\"",
		"[[models]]\nname = \"m-two\"\nprovider = \"rnes-llamacpp\"\nalias = \"m-two\"",
		"enable_telemetry = false",
		"enable_update_checks = false",
		"enable_auto_update = false",
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
}

func TestRenderWithoutModelsHasNoActiveModel(t *testing.T) {
	raw := string(Render("http://127.0.0.1:8080", nil))
	if strings.Contains(raw, "active_model") {
		t.Errorf("Render(nil models) set an active model:\n%s", raw)
	}
	if !strings.Contains(raw, "[[providers]]") {
		t.Errorf("Render(nil models) dropped the provider:\n%s", raw)
	}
}

func TestRenderTrimsATrailingSlashFromTheBaseURL(t *testing.T) {
	raw := string(Render("http://127.0.0.1:8080/", []string{"m"}))
	if !strings.Contains(raw, `api_base = "http://127.0.0.1:8080/v1"`) {
		t.Errorf("api_base not normalized:\n%s", raw)
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

func TestSetModelReplacesOnlyTheActiveModelLine(t *testing.T) {
	raw := Render("http://127.0.0.1:8080", []string{"m-one", "m-two"})
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
