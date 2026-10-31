package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/df3l0p/rnes/internal/proxy"
	"github.com/df3l0p/rnes/internal/skills"
)

func TestNewAgyRequiresSandbox(t *testing.T) {
	if _, err := New("agy", Options{}); err == nil || !strings.Contains(err.Error(), "-sandbox") {
		t.Fatalf("New(agy, Sandbox=false) = %v, want -sandbox error", err)
	}
	h, err := New("agy", Options{Sandbox: true})
	if err != nil {
		t.Fatalf("New(agy, Sandbox=true): %v", err)
	}
	if NeedsModelServer(h) {
		t.Error("agy must not require a local llama-server")
	}
}

func TestNewAgyRejectsAPolicyThatInspectsItsNoInspectEndpoints(t *testing.T) {
	// agy needs its Google endpoints tunnelled unread; a project policy that
	// lists one of them for TLS inspection can't be honored, so constructing
	// the harness must fail rather than silently downgrade the user's entry.
	_, err := New("agy", Options{Sandbox: true, Policy: []byte("github.com:443\nwww.googleapis.com:443\n")})
	if err == nil || !strings.Contains(err.Error(), "www.googleapis.com:443") || !strings.Contains(err.Error(), ProjectPolicyRel) {
		t.Fatalf("New(agy, inspecting policy) = %v, want an error naming the host and %s", err, ProjectPolicyRel)
	}

	// Marking the entry no-inspect in the project policy resolves the conflict.
	if _, err := New("agy", Options{Sandbox: true, Policy: []byte("no-inspect www.googleapis.com:443\n")}); err != nil {
		t.Fatalf("New(agy, no-inspect policy): %v", err)
	}
	// The built-in defaults name none of agy's endpoints, so they never conflict.
	if _, err := New("agy", Options{Sandbox: true}); err != nil {
		t.Fatalf("New(agy, default policy): %v", err)
	}
	// Neither does an empty one.
	if _, err := New("agy", Options{Sandbox: true, Policy: []byte{}}); err != nil {
		t.Fatalf("New(agy, empty policy): %v", err)
	}
}

func TestAgyAuthVolumeIsPerProjectAndCanonical(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "proj-a")
	projB := filepath.Join(root, "proj-b")
	linkA := filepath.Join(root, "link-a")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(projA, linkA); err != nil {
		t.Fatal(err)
	}

	h, err := New("agy", Options{Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	volA := AuthVolume(h, projA)
	volLinkA := AuthVolume(h, linkA)
	volB := AuthVolume(h, projB)

	if !strings.HasPrefix(volA, "rnes-auth-agy-") || len(volA) != len("rnes-auth-agy-")+12 {
		t.Errorf("volA = %q, want rnes-auth-agy-<12 hex>", volA)
	}
	if volA != volLinkA {
		t.Errorf("symlink to same project gave %q vs %q", volLinkA, volA)
	}
	if volA == volB {
		t.Errorf("distinct projects shared auth volume %q", volA)
	}
}

func TestAgyPolicyMarksGoogleEndpointsNoInspectWhileKeepingGitHubInspected(t *testing.T) {
	h, err := New("agy", Options{Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	s := h.(*Sandbox)
	pol, err := proxy.ParsePolicy(bytes.NewReader(s.policy()))
	if err != nil {
		t.Fatalf("ParsePolicy(agy): %v", err)
	}

	for _, google := range []string{
		"accounts.google.com:443",
		"oauth2.googleapis.com:443",
		"www.googleapis.com:443",
		"antigravity.google:443",
		"antigravity.googleapis.com:443",
		"antigravity-pa.googleapis.com:443",
		"antigravity-unleash.goog:443",
		"cloudcode-pa.googleapis.com:443",
		"daily-cloudcode-pa.googleapis.com:443",
		"autopush-cloudcode-pa.sandbox.googleapis.com:443",
		"preprod-daily-cloudcode-pa.sandbox.googleapis.com:443",
		"aicode.googleapis.com:443",
		"autopush-aicode.sandbox.googleapis.com:443",
		"generativelanguage.googleapis.com:443",
		"businessaicode.googleapis.com:443",
		"aiplatform.googleapis.com:443",
	} {
		if !pol.Explicit(google) || !pol.SkipsInspect(google) {
			t.Errorf("%s: Explicit=%v SkipsInspect=%v, want both true", google, pol.Explicit(google), pol.SkipsInspect(google))
		}
	}

	// Default policy entries like github.com stay intercepted for audit & token injection.
	if !pol.Explicit("github.com:443") || pol.SkipsInspect("github.com:443") {
		t.Errorf("github.com:443 should remain inspected: Explicit=%v SkipsInspect=%v", pol.Explicit("github.com:443"), pol.SkipsInspect("github.com:443"))
	}

	// With an empty .rnes/policy.txt, only agy's no-inspect endpoints are allowed,
	// leaving zero intercepted DNS hosts.
	hEmpty, _ := New("agy", Options{Sandbox: true, Policy: []byte{}})
	emptyPol, err := proxy.ParsePolicy(bytes.NewReader(hEmpty.(*Sandbox).policy()))
	if err != nil {
		t.Fatalf("ParsePolicy(agy empty custom policy): %v", err)
	}
	if got := emptyPol.DNSHosts(); len(got) != 0 {
		t.Errorf("empty custom policy DNSHosts = %v, want empty (all agy endpoints are no-inspect)", got)
	}
	if !emptyPol.SkipsInspect("cloudcode-pa.googleapis.com:443") || emptyPol.Explicit("github.com:443") {
		t.Errorf("unexpected empty custom policy state: %+v", emptyPol)
	}
}

func TestAgyRunArgsMountsAuthVolumeSkillsSiblingAndForwardsModelAndArgs(t *testing.T) {
	workdir := t.TempDir()
	pn := &proxyNet{network: "rnes-net-1", container: "rnes-proxy-1", caVolume: "rnes-ca-1"}

	// Default launch: ModelSet=false omits --model even if a default model string is passed.
	hDefault, _ := New("agy", Options{Sandbox: true})
	sDefault := hDefault.(*Sandbox)
	sDefault.image = "rnes-sandbox-default:agy1"
	vol := sDefault.AuthVolume(workdir)

	defaultArgs := sDefault.runArgsForModel("rnes-1", workdir, pn, false, "unwanted-default")
	joined := strings.Join(defaultArgs, " ")
	for _, want := range []string{
		"-v " + vol + ":/home/rnes/.gemini/antigravity-cli",
		"-v " + vol + ":/home/rnes/.config/antigravity",
		"--mount type=bind,source=" + filepath.Join(workdir, ".rnes") + ",target=/workspace/.rnes,readonly",
		"-v rnes-ca-1:/etc/rnes-ca:ro",
		"-e SSL_CERT_FILE=/etc/rnes-ca/ca.pem",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("default agy runArgs missing %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"OPENCODE_CONFIG", "opencode.jsonc", "--model", "unwanted-default", ".gemini/config"} {
		if strings.Contains(joined, bad) {
			t.Errorf("default agy runArgs must not contain %q:\n%s", bad, joined)
		}
	}
	// Image is the last argument when neither -model nor -- args were given.
	if defaultArgs[len(defaultArgs)-1] != "rnes-sandbox-default:agy1" {
		t.Errorf("expected image to be final arg, got %q", defaultArgs[len(defaultArgs)-3:])
	}

	// Explicit -model + trailing -- args + attached skills.
	hCustom, _ := New("agy", Options{
		Sandbox:  true,
		ModelSet: true,
		Args:     []string{"--continue", "--mode", "plan"},
		Skills:   []skills.Skill{{Name: "my-skill", Dir: "/host/skills/my-skill"}},
	})
	sCustom := hCustom.(*Sandbox)
	sCustom.image = "rnes-sandbox-default:agy1"
	sCustom.configHome = "/tmp/staged-agy-config"

	customJoined := strings.Join(sCustom.runArgsForModel("rnes-2", workdir, pn, false, "gemini-2.5-pro"), " ")
	for _, want := range []string{
		"-v " + vol + ":/home/rnes/.gemini/antigravity-cli",
		"-v " + vol + ":/home/rnes/.config/antigravity",
		"--mount type=bind,source=/tmp/staged-agy-config,target=/home/rnes/.gemini/config ",
		"--mount type=bind,source=/host/skills/my-skill,target=/home/rnes/.gemini/config/skills/my-skill,readonly ",
		"rnes-sandbox-default:agy1 --model gemini-2.5-pro --continue --mode plan",
	} {
		if !strings.Contains(customJoined, want) {
			t.Errorf("custom agy runArgs missing %q:\n%s", want, customJoined)
		}
	}
}

func TestAgyRunCreatesLabeledAuthVolumeWithoutRequiringConfigure(t *testing.T) {
	logPath := fakeDockerProxy(t, nil, nil)
	t.Setenv("TMPDIR", t.TempDir())
	workdir := t.TempDir()
	skillDir := t.TempDir()
	// Also place a project skill in .opencode/skills to verify agy does NOT
	// generate synthetic opencode command files.
	mkSkill(t, filepath.Join(workdir, ".opencode", "skills"), "proj-only")

	h, err := New("agy", Options{
		Sandbox: true,
		Skills:  []skills.Skill{{Name: "attached", Dir: skillDir}},
		Args:    []string{"--version"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := h.(*Sandbox)
	s.image, s.proxyImage = "rnes-sandbox-default:agytest", "rnes-sandbox-proxy:test"

	if err := s.Run(workdir, "", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}

	vol := s.AuthVolume(workdir)
	wantVolCreate := "volume create --label rnes.auth=agy " + vol
	var sawVolCreate, sawRun bool
	for _, c := range calls(t, logPath) {
		if c == wantVolCreate {
			sawVolCreate = true
		}
		if strings.HasPrefix(c, "run --rm") {
			sawRun = true
			if !strings.Contains(c, "-v "+vol+":/home/rnes/.gemini/antigravity-cli") {
				t.Errorf("run call missing auth volume mount: %s", c)
			}
			if !strings.Contains(c, "-v "+vol+":/home/rnes/.config/antigravity") {
				t.Errorf("run call missing credential volume mount: %s", c)
			}
			if !strings.Contains(c, "source="+skillDir+",target=/home/rnes/.gemini/config/skills/attached,readonly") {
				t.Errorf("run call missing read-only skill mount: %s", c)
			}
			if !strings.HasSuffix(c, "rnes-sandbox-default:agytest --version") {
				t.Errorf("run call missing trailing --version arg: %s", c)
			}
		}
		if c == "volume rm "+vol {
			t.Errorf("auth volume %s must persist across sessions, not be removed on stop", vol)
		}
	}
	if !sawVolCreate || !sawRun {
		t.Errorf("sawVolCreate=%v sawRun=%v; calls=%v", sawVolCreate, sawRun, calls(t, logPath))
	}
}
