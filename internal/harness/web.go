package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// webSandboxPort is the port opencode's server listens on inside the
	// sandbox. The proxy's forwarder and the container agree on it.
	webSandboxPort = 4096
	// proxyForwardPort is where rnes-proxy listens inside the proxy
	// container for the web forward. It is published on host loopback at a
	// random port.
	proxyForwardPort = 4097
	// webReadyTimeout bounds how long rnes waits for the server to answer.
	webReadyTimeout = 60 * time.Second
	// webPasswordEnv makes opencode's server use a password rnes knows,
	// instead of printing a generated one.
	webPasswordEnv = "OPENCODE_PASSWORD"
)

// newWebPassword returns a random password for one session's web server.
func newWebPassword() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a web password: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// freePort picks a loopback TCP port that is free right now. The caller binds
// it immediately; the race window is negligible and a lost race fails loudly.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func webBaseURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// serveArgs is opencode's argv for a standalone web server on port.
func serveArgs(port int) []string {
	return []string{"serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
}

// tuiServerArgs is opencode's argv for the TUI attached to a server, rather
// than starting its own.
func tuiServerArgs(baseURL string) []string {
	return []string{"--server", baseURL, "."}
}

// waitWebReady polls the server's unauthenticated-with-password info endpoint
// until it answers, the process exits, or the timeout runs out.
func waitWebReady(baseURL, password string, timeout time.Duration, done <-chan struct{}) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/api/info", nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth("opencode", password)
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("%s", resp.Status)
		} else {
			last = err
		}
		if done != nil {
			select {
			case <-done:
				return fmt.Errorf("the web server exited before it was ready: %v", last)
			default:
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the web server didn't answer within %s: %v", timeout, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pairLink asks the server for a one-time pairing code and returns the link a
// browser opens. The link expires in five minutes; opening it sets a session
// cookie and loads the web UI.
func pairLink(baseURL, password string) (string, error) {
	body, err := json.Marshal(map[string]any{})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/pair", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("opencode", password)
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("pairing: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pairing: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("pairing: decoding the response: %w", err)
	}
	if out.Code == "" {
		return "", fmt.Errorf("pairing: the server returned no code")
	}
	return baseURL + "/auth/connect/" + url.PathEscape(out.Code), nil
}

// browserCommand returns argv that opens url in the default browser, or an
// error on a platform rnes doesn't know.
func browserCommand(url string) ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", url}, nil
	case "linux":
		return []string{"xdg-open", url}, nil
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}, nil
	default:
		return nil, fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
	}
}

// openBrowser opens url in the default browser. Failure is not fatal: the
// caller prints the link as well.
func openBrowser(url string) error {
	argv, err := browserCommand(url)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// printWebLink prints the pairing link, and tries to open it.
func printWebLink(link string) {
	fmt.Fprintf(os.Stderr, "rnes: web UI: %s\n", link)
	fmt.Fprintln(os.Stderr, "rnes: (one-time link: it expires in 5 minutes; the browser then keeps the session for 30 days)")
	if err := openBrowser(link); err != nil {
		fmt.Fprintf(os.Stderr, "rnes: note: open that link in your browser (%v)\n", err)
	}
}
