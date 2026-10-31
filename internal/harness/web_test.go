package harness

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPairLink(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pair" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /api/pair", r.Method, r.URL.Path)
		}
		gotUser, gotPass, _ = r.BasicAuth()
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"code":"abc-123","expires_in":300}`))
	}))
	defer srv.Close()

	link, err := pairLink(srv.URL, "secret")
	if err != nil {
		t.Fatalf("pairLink: %v", err)
	}
	want := srv.URL + "/auth/connect/abc-123"
	if link != want {
		t.Errorf("link = %q, want %q", link, want)
	}
	if gotUser != "opencode" || gotPass != "secret" {
		t.Errorf("basic auth = %q/%q, want opencode/secret", gotUser, gotPass)
	}
}

func TestPairLinkReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := pairLink(srv.URL, "bad"); err == nil {
		t.Fatal("pairLink succeeded against a 401, want an error")
	}
}

func TestWaitWebReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"version":"test"}`))
	}))
	defer srv.Close()
	if err := waitWebReady(srv.URL, "secret", time.Second, nil); err != nil {
		t.Fatalf("waitWebReady: %v", err)
	}
}

func TestWaitWebReadyTimesOutAndReportsExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if err := waitWebReady(srv.URL, "bad", 150*time.Millisecond, nil); err == nil {
		t.Fatal("waitWebReady succeeded against a 401, want a timeout error")
	}

	done := make(chan struct{})
	close(done)
	if err := waitWebReady(srv.URL, "bad", time.Minute, done); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("waitWebReady with a closed done = %v, want an exited error", err)
	}
}

func TestServeAndTUIServerArgs(t *testing.T) {
	if got := strings.Join(serveArgs(4321), " "); got != "serve --hostname 127.0.0.1 --port 4321" {
		t.Errorf("serveArgs = %q", got)
	}
	if got := strings.Join(tuiServerArgs("http://127.0.0.1:4321"), " "); got != "--server http://127.0.0.1:4321 ." {
		t.Errorf("tuiServerArgs = %q", got)
	}
	if got := webBaseURL(4321); got != "http://127.0.0.1:4321" {
		t.Errorf("webBaseURL = %q", got)
	}
}

func TestFreePortIsUsable(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if port <= 0 {
		t.Fatalf("freePort = %d, want a positive port", port)
	}
}

func TestNewWebPasswordIsRandomAndLongEnough(t *testing.T) {
	a, err := newWebPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newWebPassword()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two passwords are identical")
	}
	if len(a) < 32 {
		t.Errorf("password %q is too short", a)
	}
}

// TestWebFlowAgainstRealOpencode exercises the exact sequence -web uses
// (serve with a known password, wait for /api/info, pair) against the real
// binary, when it's installed.
func TestWebFlowAgainstRealOpencode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real opencode check in -short mode")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}
	password := "rnes-test-password"
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	base := webBaseURL(port)
	dir := t.TempDir()
	cmd := exec.Command("opencode", serveArgs(port)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		webPasswordEnv+"="+password,
		"XDG_DATA_HOME="+t.TempDir(),
		"XDG_STATE_HOME="+t.TempDir(),
		"XDG_CACHE_HOME="+t.TempDir(),
	)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting opencode serve: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	if err := waitWebReady(base, password, 30*time.Second, nil); err != nil {
		t.Fatalf("waitWebReady: %v", err)
	}
	link, err := pairLink(base, password)
	if err != nil {
		t.Fatalf("pairLink: %v", err)
	}
	if !strings.HasPrefix(link, base+"/auth/connect/") {
		t.Errorf("link = %q, want a pairing link on %s", link, base)
	}
}
