package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestRunRefreshesDownloadedModel(t *testing.T) {
	if os.Getenv("RNES_TEST_MODEL_DOWNLOAD") == "1" {
		flag.CommandLine = flag.NewFlagSet("rnes", flag.ContinueOnError)
		os.Args = []string{"rnes", "-model", "test/model:Q4_K_M", "-port",
			os.Getenv("RNES_TEST_MODEL_PORT")}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	downloaded := filepath.Join(dir, "downloaded")
	started := filepath.Join(dir, "agent-started")
	var refreshed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			return
		case "/v1/models":
			_, err := os.Stat(downloaded)
			if err != nil && r.URL.Query().Has("reload") {
				http.Error(w, "initial discovery must not refresh the catalog", http.StatusBadRequest)
				return
			}
			if err == nil && r.URL.Query().Get("reload") == "1" {
				refreshed.Store(true)
			}
			if refreshed.Load() {
				fmt.Fprint(w, `{"data":[{"id":"test/model:Q4_K_M"}],"object":"list"}`)
				return
			}
			fmt.Fprint(w, `{"data":[],"object":"list"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	scripts := map[string]string{
		"llama": `#!/bin/bash
set -euo pipefail
test "$#" -eq 3
test "$1" = download
test "$2" = -hf
test "$3" = test/model:Q4_K_M
: > "$RNES_TEST_DOWNLOADED"
`,
		"opencode": `#!/bin/bash
set -euo pipefail
if [ "$1" = --version ]; then
  echo 2.0.0
else
  : > "$RNES_TEST_AGENT_STARTED"
fi
`,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunRefreshesDownloadedModel$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"RNES_TEST_MODEL_DOWNLOAD=1", "RNES_TEST_DOWNLOADED="+downloaded,
		"RNES_TEST_AGENT_STARTED="+started,
		"RNES_TEST_MODEL_PORT="+strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port),
		"HOME="+dir, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rnes did not launch the agent after downloading the model: %v\n%s", err, out)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("agent did not start: %v", err)
	}
}
