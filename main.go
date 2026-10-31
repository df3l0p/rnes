// Command rnes starts a local llama-server, points opencode at it, and runs
// opencode in the current directory.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/df3l0p/rnes/images"
	"github.com/df3l0p/rnes/internal/harness"
	"github.com/df3l0p/rnes/internal/llamaserver"
	"github.com/df3l0p/rnes/internal/proxy"
	"github.com/df3l0p/rnes/internal/skills"
)

const providerKey = "llama-cpp"

// cliConfig holds rnes's flags. Parsed into a struct (rather than package
// globals or ad-hoc locals in run()) so flag parsing stays in one place and
// run() takes a plain value.
type cliConfig struct {
	model    string
	modelSet bool // -model was passed explicitly, even if equal to the default
	host     string
	hostSet  bool // -host was passed explicitly, even if equal to the default
	port     int
	portSet  bool // -port was passed explicitly, even if equal to the default
	harness  string
	// harnessSet reports whether -harness was passed explicitly (used by
	// -sandbox-update to scope a rebuild to one harness instead of all of them).
	harnessSet bool
	sandbox    bool
	image      string
	build      bool
	// args are the trailing command-line arguments after "--", forwarded to
	// the harness executable.
	args []string
	// sandboxUpdate rebuilds the sandbox images and exits without running the
	// agent. applyImplications turns it into sandbox and build.
	sandboxUpdate bool
	// generatePolicy writes a starter .rnes/policy.txt in the current
	// directory and exits without running the agent.
	generatePolicy bool
	// allNet lets the sandbox reach any public host, not just the
	// default allow-list.
	allNet bool
	// noInspect turns off TLS interception in the sandbox's proxy.
	noInspect bool
	// web opens the session's web UI in the browser at startup. noWeb turns
	// off serving it at all (the sandbox exposes it on a loopback port by
	// default; on the host opencode's own server already serves it).
	web   bool
	noWeb bool
	// githubToken is RNES_GITHUB_TOKEN, read (and unset) at startup; empty means
	// no GitHub credentials. It is only kept with -sandbox.
	githubToken string
	// openrouterKey is RNES_OPENROUTER_API_KEY, handled like githubToken.
	openrouterKey string
	// skills are the -skill values (names, paths or "all") and skillsDirs the
	// -skills-dir folders, both in the order given.
	skills     stringList
	skillsDirs stringList
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

const defaultHost = "127.0.0.1"

// validate rejects flag combinations that only make sense with -sandbox.
func (c cliConfig) validate() error {
	if c.generatePolicy && c.sandboxUpdate {
		return fmt.Errorf("-generate-policy and -sandbox-update cannot be used together")
	}
	if c.web && c.noWeb {
		return fmt.Errorf("-web and -no-web cannot be used together")
	}
	if c.web && c.harness != "opencode" {
		return fmt.Errorf("-web requires -harness opencode")
	}
	if !c.sandbox && (c.image != "" || c.build || c.allNet || c.noInspect || len(c.skills) > 0 || len(c.skillsDirs) > 0) {
		return fmt.Errorf("-image, -build, -all-net, -no-inspect, -skill and -skills-dir require -sandbox")
	}
	if c.githubToken != "" {
		if err := proxy.ValidateToken(c.githubToken); err != nil {
			return fmt.Errorf("RNES_GITHUB_TOKEN is unusable: %w", err)
		}
		if c.noInspect {
			return fmt.Errorf("RNES_GITHUB_TOKEN is set, but -no-inspect stops the proxy from decrypting HTTPS, so it couldn't inject the token; unset one of them")
		}
	}
	if c.openrouterKey != "" && !c.ignoresOpenRouterKey() {
		if err := proxy.ValidateToken(c.openrouterKey); err != nil {
			return fmt.Errorf("RNES_OPENROUTER_API_KEY is unusable: %w", err)
		}
		if c.noInspect {
			return fmt.Errorf("RNES_OPENROUTER_API_KEY is set, but -no-inspect stops the proxy from decrypting HTTPS, so it couldn't inject the key; unset one of them")
		}
	}
	return nil
}

// ignoresOpenRouterKey reports whether the selected harness never uses the
// OpenRouter provider, so RNES_OPENROUTER_API_KEY is dropped with a note (see
// run) even when it would otherwise be rejected as unusable or at odds with
// -no-inspect.
func (c cliConfig) ignoresOpenRouterKey() bool {
	if !c.sandbox {
		return false
	}
	needsModelServer, err := harness.NeedsModelServerName(c.harness)
	return err == nil && !needsModelServer
}

// checkTrailingArgs rejects positional arguments that aren't behind the "--"
// passthrough: flag parsing stops at the first non-flag argument, so a stray
// positional would silently swallow the rest of the line into the agent's
// arguments instead of being parsed as rnes flags.
func checkTrailingArgs(argv, args []string) error {
	if len(args) == 0 || slices.Contains(argv, "--") {
		return nil
	}
	return fmt.Errorf("unexpected argument %q: put arguments for the agent after \"--\"", args[0])
}

// newSession resolves everything run() needs before any server or container
// starts, in one order: flags that only model-server sessions accept are
// checked and a cloud harness's unused OpenRouter key is dropped first (so
// nothing downstream sees credentials or flags that only a model-server
// session would use), then skills and the sandbox policy (so a bad -skill or
// .rnes/policy.txt fails with nothing left behind), then the harness itself.
// Without -sandbox the harness reports its own error (e.g. agy requires it).
// The returned cfg carries any adjustments (the dropped OpenRouter key).
func newSession(cfg cliConfig, home, cwd string, stderr io.Writer) (cliConfig, harness.Harness, []skills.Skill, error) {
	if cfg.sandbox {
		needsModelServer, err := harness.NeedsModelServerName(cfg.harness)
		if err != nil {
			return cfg, nil, nil, err
		}
		if err := checkModelServerFlags(cfg, needsModelServer); err != nil {
			return cfg, nil, nil, err
		}
		if !needsModelServer && cfg.openrouterKey != "" {
			fmt.Fprintf(stderr, "rnes: note: RNES_OPENROUTER_API_KEY is ignored with -harness %s\n", cfg.harness)
			cfg.openrouterKey = ""
		}
	}
	attached, err := resolveSkills(cfg, home, cwd, stderr)
	if err != nil {
		return cfg, nil, nil, err
	}
	policyBytes, err := resolvePolicy(cfg, cwd, stderr)
	if err != nil {
		return cfg, nil, nil, err
	}
	h, err := harness.New(cfg.harness, harnessOptions(cfg, attached, policyBytes))
	if err != nil {
		return cfg, nil, nil, err
	}
	return cfg, h, attached, nil
}

// checkModelServerFlags rejects an explicit -host or -port when the selected
// harness doesn't run against a local llama-server.
func checkModelServerFlags(c cliConfig, needsModelServer bool) error {
	if needsModelServer {
		return nil
	}
	var given []string
	if c.hostSet {
		given = append(given, "-host")
	}
	if c.portSet {
		given = append(given, "-port")
	}
	if len(given) == 0 {
		return nil
	}
	return fmt.Errorf("%s cannot be used with -harness %s (it doesn't use llama-server)", strings.Join(given, " and "), c.harness)
}

// harnessOptions turns the parsed flags into the harness's Options.
func harnessOptions(c cliConfig, attached []skills.Skill, policy []byte) harness.Options {
	return harness.Options{
		Sandbox:       c.sandbox,
		Image:         c.image,
		Build:         c.build,
		AllNet:        c.allNet,
		NoInspect:     c.noInspect,
		GitHubToken:   c.githubToken,
		OpenRouterKey: c.openrouterKey,
		Skills:        attached,
		Policy:        policy,
		ModelSet:      c.modelSet,
		Args:          c.args,
		WebExpose:     !c.noWeb,
		WebOpen:       c.web,
	}
}

// rebuildAllHarnesses reports whether -sandbox-update should rebuild every
// harness's images: only when -harness wasn't given, since an explicit
// -harness scopes the rebuild to that harness.
func rebuildAllHarnesses(c cliConfig) bool {
	return c.sandboxUpdate && !c.harnessSet
}

// runHarness launches h in the foreground. The agent shares rnes's foreground
// process group, so Ctrl+C reaches both; notifying (and not reacting) keeps
// rnes alive to run its cleanup (stopping llama-server, tearing down the
// sandbox's proxy and container) once the agent exits.
func runHarness(h harness.Harness, dir, providerKey, model string) error {
	sigCh := make(chan os.Signal, 1)
	// SIGHUP (terminal closed) is included for the same reason, so cleanup
	// still runs once the agent exits.
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	return h.Run(dir, providerKey, model)
}

// applyImplications derives flags implied by others: -sandbox-update is a
// sandbox action, and refreshing an image means rebuilding it even if it
// exists (-build's behaviour), since the tags only change with the
// Dockerfiles — a cached or existing image would keep serving the old
// packages and the old opencode.
func (c *cliConfig) applyImplications() {
	if c.sandboxUpdate {
		c.sandbox = true
		c.build = true
	}
}

// envHelp is the Environment section of -h.
const envHelp = `
Environment:
  RNES_GITHUB_TOKEN   GitHub token the sandbox's proxy injects for git and gh at
                      github.com and api.github.com (requires -sandbox). The sandbox
                      only ever holds a placeholder. Prefer a fine-grained token
                      scoped to what the agent needs.
                      To reuse your logged-in gh account instead (broad scopes: the
                      agent can do anything your account can):
                        export RNES_GITHUB_TOKEN=$(gh auth token)
  RNES_OPENROUTER_API_KEY
                      OpenRouter API key the sandbox's proxy injects at openrouter.ai
                      (requires -sandbox; ignored with -harness agy). The sandbox only
                      ever holds a placeholder, but an agent in it can still make
                      OpenRouter calls, so use a dedicated key with a low credit limit.
                      Local llama stays the default; switch to an openrouter model
                      inside opencode. Prompts and code context leave your machine when
                      you do.
`

// trailingArgsHelp documents the "--" passthrough.
const trailingArgsHelp = `
Arguments after "--" are forwarded to the harness executable, e.g.
  rnes -sandbox -harness agy -- --version
`

// usage is flag.Usage: the flag defaults followed by the environment.
func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage of %s: [flags] [-- harness-args...]\n", os.Args[0])
	flag.CommandLine.SetOutput(w)
	flag.PrintDefaults()
	fmt.Fprint(w, trailingArgsHelp)
	fmt.Fprint(w, envHelp)
}

// tokenWarning is the warning for a token that isn't a fine-grained personal
// access token ("github_pat_…"), or "" for one that is. Classic tokens and the
// OAuth token `gh auth token` returns can't be limited to specific repositories.
func tokenWarning(token string) string {
	if strings.HasPrefix(token, "github_pat_") {
		return ""
	}
	return "RNES_GITHUB_TOKEN isn't a fine-grained token (github_pat_…), so it can't be limited to specific repositories; a fine-grained token is safer"
}

// sessionCredential returns val only when a session will use it: it needs a
// sandbox, and -sandbox-update exits before one starts. env names the variable
// in the note printed when it is dropped.
func (c cliConfig) sessionCredential(env, val string) string {
	switch {
	case val == "":
	case c.generatePolicy:
		fmt.Fprintf(os.Stderr, "rnes: note: %s is ignored with -generate-policy (no session runs)\n", env)
	case !c.sandbox:
		fmt.Fprintf(os.Stderr, "rnes: note: %s is ignored without -sandbox\n", env)
	case c.sandboxUpdate:
		fmt.Fprintf(os.Stderr, "rnes: note: %s is ignored with -sandbox-update (no session runs)\n", env)
	default:
		return val
	}
	return ""
}

// dropSkillsForUpdate clears the skill flags with -sandbox-update, which exits
// before a session starts, with a note like sessionCredential's.
func (c *cliConfig) dropSkillsForUpdate() {
	if c.sandboxUpdate && (len(c.skills) > 0 || len(c.skillsDirs) > 0) {
		fmt.Fprintln(os.Stderr, "rnes: note: -skill and -skills-dir are ignored with -sandbox-update (no session runs)")
		c.skills, c.skillsDirs = nil, nil
	}
}

// resolveSkills resolves -skill and -skills-dir against the host, printing any
// warnings to stderr. Without either flag it attaches nothing and reads
// nothing.
func resolveSkills(c cliConfig, home, cwd string, stderr io.Writer) ([]skills.Skill, error) {
	if len(c.skills) == 0 && len(c.skillsDirs) == 0 {
		return nil, nil
	}
	attached, warnings, err := skills.Resolve(skills.Request{Values: c.skills, Dirs: c.skillsDirs, Home: home, Cwd: cwd, Harness: c.harness})
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "rnes: warning:", w)
	}
	if len(c.skills) == 0 {
		fmt.Fprintln(stderr, "rnes: note: -skills-dir only adds folders to look -skill names up in; without -skill nothing is attached")
	}
	return attached, nil
}

// resolvePolicy prepares <cwd>/.rnes and loads <cwd>/.rnes/policy.txt for a
// sandbox session, or notes that the file is ignored when running on the host.
func resolvePolicy(c cliConfig, cwd string, stderr io.Writer) ([]byte, error) {
	if c.sandboxUpdate {
		return nil, nil
	}
	if !c.sandbox {
		if harness.HasProjectPolicy(cwd) {
			fmt.Fprintf(stderr, "rnes: note: %s is ignored without -sandbox\n", harness.ProjectPolicyRel)
		}
		return nil, nil
	}
	raw, pol, found, err := harness.LoadProjectPolicy(cwd)
	if err != nil {
		return nil, err
	}
	if _, err := harness.EnsureProjectDir(cwd); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	hosts := pol.SortedAllow()
	if len(hosts) == 0 {
		scope := "model server only"
		if needs, err := harness.NeedsModelServerName(c.harness); err == nil && !needs {
			scope = "harness endpoints only"
		}
		fmt.Fprintf(stderr, "rnes: sandbox policy (%s): none (%s)\n", harness.ProjectPolicyRel, scope)
	} else {
		shown := make([]string, len(hosts))
		for i, hp := range hosts {
			if pol.SkipsInspect(hp) {
				hp += " (no-inspect)"
			}
			shown[i] = hp
		}
		fmt.Fprintf(stderr, "rnes: sandbox policy (%s): %s\n", harness.ProjectPolicyRel, strings.Join(shown, ", "))
	}
	return raw, nil
}

// githubTokenFromEnv returns RNES_GITHUB_TOKEN and removes it from rnes's
// environment, so nothing rnes starts afterwards (docker, llama-server) inherits it.
func githubTokenFromEnv() string {
	tok := os.Getenv("RNES_GITHUB_TOKEN")
	os.Unsetenv("RNES_GITHUB_TOKEN")
	return tok
}

// openRouterKeyFromEnv returns RNES_OPENROUTER_API_KEY and removes it from rnes's
// environment, like githubTokenFromEnv.
func openRouterKeyFromEnv() string {
	key := os.Getenv("RNES_OPENROUTER_API_KEY")
	os.Unsetenv("RNES_OPENROUTER_API_KEY")
	return key
}

// activeProviders names the credential providers a session will use, for the
// startup line.
func activeProviders(c cliConfig) []string {
	var names []string
	if c.githubToken != "" {
		names = append(names, "github")
	}
	if c.openrouterKey != "" {
		names = append(names, "openrouter")
	}
	return names
}

// listenHost is the address llama-server listens on, and that rnes itself uses
// to reach it. An explicit -host (hostSet, or any non-default value) is used as
// is, with a warning if it's a wildcard. Without one, a harness that runs the agent in a
// container picks the narrowest address the container can reach, which on a
// native Linux engine isn't the host's loopback.
func listenHost(h harness.Harness, host string, hostSet bool) (string, error) {
	if hostSet || host != defaultHost {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			fmt.Fprintf(os.Stderr, "rnes: warning: -host %s makes llama-server reachable from your whole network\n", host)
		}
		return host, nil
	}
	if b, ok := h.(harness.BindHoster); ok {
		return b.BindHost()
	}
	return host, nil
}

func parseFlags() (cliConfig, error) {
	var cfg cliConfig
	flag.StringVar(&cfg.model, "model", "unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M", "model to use in the harness (downloaded if not cached); every cached model is made available; with -harness agy, forwarded as --model only when given")
	flag.StringVar(&cfg.host, "host", defaultHost, "llama-server host (with -sandbox and the default, the narrowest address containers can reach: the loopback on Docker Desktop, the docker bridge gateway on Linux; rejected for harnesses that don't use llama-server)")
	flag.IntVar(&cfg.port, "port", 8080, "llama-server port (rejected for harnesses that don't use llama-server)")
	flag.StringVar(&cfg.harness, "harness", "opencode", "coding agent harness to run (available: "+strings.Join(harness.Names(), ", ")+")")
	flag.BoolVar(&cfg.sandbox, "sandbox", false, "run the harness in a Docker container that mounts the current directory")
	flag.StringVar(&cfg.image, "image", "", "bundled sandbox image to use (default "+images.Default+"; available: "+strings.Join(images.Names(), ", ")+"); requires -sandbox")
	flag.BoolVar(&cfg.build, "build", false, "rebuild the sandbox image from scratch, without docker's layer cache, even if it exists (it is built when missing, never pulled); requires -sandbox")
	flag.BoolVar(&cfg.sandboxUpdate, "sandbox-update", false, "rebuild the sandbox images (every bundled one for every harness, unless -harness selects one or -image selects an image; plus each base and the proxy image) from scratch, without docker's layer cache, so it picks up new packages and a newer agent, then exit without running the agent; implies -sandbox")
	flag.BoolVar(&cfg.generatePolicy, "generate-policy", false, "write a starter .rnes/policy.txt in the current directory for -sandbox and exit without running the agent")
	flag.BoolVar(&cfg.allNet, "all-net", false, "let the sandbox reach any public host over http(s), not just the default allow-list (your machine and LAN stay blocked apart from llama-server); requires -sandbox")
	flag.BoolVar(&cfg.noInspect, "no-inspect", false, "don't terminate TLS in the sandbox's proxy: tunnel every HTTPS connection unread (by default the proxy decrypts the allow-listed hosts with a CA made for the session, and logs each request); requires -sandbox")
	flag.BoolVar(&cfg.web, "web", false, "open opencode's web UI for the session in your browser at startup (requires -harness opencode); the TUI keeps running and both share one live session")
	flag.BoolVar(&cfg.noWeb, "no-web", false, "don't serve the session's web UI at all (with -sandbox it is exposed on a loopback port by default; on the host opencode's server always serves it, so this only saves the sandbox forward)")
	flag.Var(&cfg.skills, "skill", "attach a host skill to the sandbox, read-only: a skill name (looked up in each -skills-dir, then the active harness's global skills folder, ~/.gemini/config/skills, ~/.agents/skills and ~/.claude/skills), a path to one skill directory, or \"all\"; repeatable; requires -sandbox")
	flag.Var(&cfg.skillsDirs, "skills-dir", "a host folder whose subdirectories are skills, searched before the default ones for -skill names and \"all\"; attaches nothing on its own; repeatable; requires -sandbox")
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "model":
			cfg.modelSet = true
		case "host":
			cfg.hostSet = true
		case "port":
			cfg.portSet = true
		case "harness":
			cfg.harnessSet = true
		}
	})
	cfg.args = flag.Args()
	if err := checkTrailingArgs(os.Args[1:], cfg.args); err != nil {
		return cliConfig{}, err
	}
	cfg.applyImplications()
	cfg.dropSkillsForUpdate()
	cfg.githubToken = cfg.sessionCredential("RNES_GITHUB_TOKEN", githubTokenFromEnv())
	cfg.openrouterKey = cfg.sessionCredential("RNES_OPENROUTER_API_KEY", openRouterKeyFromEnv())
	return cfg, cfg.validate()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rnes:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving current directory: %w", err)
	}
	if cfg.generatePolicy {
		if _, err := harness.GeneratePolicy(cwd); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "rnes: wrote %s\n", harness.ProjectPolicyRel)
		return nil
	}
	cfg, h, attached, err := newSession(cfg, home, cwd, os.Stderr)
	if err != nil {
		return err
	}
	if vol := harness.AuthVolume(h, cwd); vol != "" && !cfg.sandboxUpdate {
		fmt.Fprintf(os.Stderr, "rnes: %s auth: persisted for this project in Docker volume %s (live OAuth inside the sandbox; list with 'docker volume ls --filter label=rnes.auth', remove with 'docker volume rm %s')\n", cfg.harness, vol, vol)
		if cfg.harness == "agy" {
			fmt.Fprintf(os.Stderr, "rnes: if agy asks you to sign in: choose Google OAuth, open the URL it prints in a browser on this machine, and paste the authorization code back before its timeout; the login then persists in the volume for this project\n")
		}
		if cfg.allNet {
			fmt.Fprintf(os.Stderr, "rnes: warning: -all-net lets the sandbox reach any public host while holding your live %s OAuth session in %s\n", cfg.harness, vol)
		}
	}
	if cfg.githubToken != "" {
		fmt.Fprintln(os.Stderr, "rnes: GitHub token: the sandbox proxy injects it at github.com and api.github.com; the sandbox only holds a placeholder (use a fine-grained token scoped to what the agent needs)")
		if w := tokenWarning(cfg.githubToken); w != "" {
			fmt.Fprintln(os.Stderr, "rnes: warning:", w)
		}
	}
	if cfg.openrouterKey != "" {
		fmt.Fprintln(os.Stderr, "rnes: OpenRouter key: the sandbox proxy injects it at openrouter.ai; the sandbox only holds a placeholder (use a dedicated key with a low credit limit). Prompts leave your machine when you pick an openrouter model.")
	}
	if names := activeProviders(cfg); len(names) > 0 {
		fmt.Fprintln(os.Stderr, "rnes: credential providers:", strings.Join(names, ", "))
	}
	if len(attached) > 0 {
		fmt.Fprintln(os.Stderr, "rnes: skills (read-only in the sandbox):", skills.Summary(attached))
	}
	if err := h.Available(); err != nil {
		return err
	}
	// Before starting any server, so a failed image pull/build leaves nothing
	// running. -sandbox-update without -harness refreshes every harness's
	// images; with -harness it scopes the rebuild to that harness (and, when
	// -image is omitted, to every image on its base).
	if rebuildAllHarnesses(cfg) {
		if err := harness.PrepareAllHarnesses(harness.Options{Image: cfg.image, Build: cfg.build}); err != nil {
			return err
		}
	} else if a, ok := h.(harness.AllPreparer); ok && cfg.sandboxUpdate && cfg.image == "" {
		if err := a.PrepareAll(); err != nil {
			return err
		}
	} else if p, ok := h.(harness.Preparer); ok {
		if err := p.Prepare(); err != nil {
			return err
		}
	}
	// -sandbox-update is done once the images are freshly built: no server,
	// no session.
	if cfg.sandboxUpdate {
		fmt.Fprintln(os.Stderr, "rnes: sandbox images are up to date")
		return nil
	}

	// A harness backed by its own cloud/subscription service (agy) has no
	// llama-server to start or point at one: run it directly. cfg.model is
	// forwarded only when -model was explicit (Options.ModelSet).
	if !harness.NeedsModelServer(h) {
		return runHarness(h, cwd, providerKey, cfg.model)
	}

	// rnes talks to the server on the address it listens on: a server bound to a
	// specific interface doesn't answer on the loopback.
	host, err := listenHost(h, cfg.host, cfg.hostSet)
	if err != nil {
		return err
	}
	opts := llamaserver.Options{Host: host, Port: cfg.port}
	baseURL := opts.BaseURL()
	if host != cfg.host {
		// A server already on the loopback would be reused by the host but
		// isn't reachable from the container, and starting another on the same
		// port would fail confusingly.
		if loopback := (llamaserver.Options{Host: cfg.host, Port: cfg.port}).BaseURL(); !llamaserver.IsHealthy(baseURL) && llamaserver.IsHealthy(loopback) {
			return fmt.Errorf("a llama-server at %s isn't reachable from the sandbox (it needs to listen on %s); stop it or use -port", loopback, host)
		}
	}

	// Register as a user of this port's llama-server for the whole run, so
	// that if it's shared with another concurrent rnes instance, whichever one
	// started it only stops it once every registered instance is gone.
	unregister, err := llamaserver.Register(cfg.port)
	if err != nil {
		return err
	}
	defer unregister()

	var proc *llamaserver.Process

	if llamaserver.IsHealthy(baseURL) {
		fmt.Fprintf(os.Stderr, "rnes: reusing existing llama-server at %s\n", baseURL)
	} else {
		command, err := llamaserver.FindCommand()
		if err != nil {
			return err
		}

		// llama-server's log lines must not go to os.Stderr: it shares this
		// terminal with opencode's full-screen TUI, and raw log output
		// interleaved with opencode's screen redraws corrupts the display.
		// Mode 0600 since the log may include request/model details tied to
		// the user's session.
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("rnes-llama-server-%d.log", cfg.port))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("creating llama-server log file: %w", err)
		}
		defer logFile.Close()

		fmt.Fprintf(os.Stderr, "rnes: starting llama-server on %s (logs: %s)\n", baseURL, logPath)
		proc, err = llamaserver.Start(command, opts, logFile)
		if err != nil {
			return err
		}
		if err := llamaserver.RecordServer(cfg.port, proc.Pid()); err != nil {
			_ = proc.Stop()
			return fmt.Errorf("recording llama-server pid: %w", err)
		}
		if err := llamaserver.WaitHealthy(baseURL, 2*time.Minute, proc); err != nil {
			_ = proc.Stop()
			_ = llamaserver.StopRecordedServer(cfg.port) // clears the record
			return fmt.Errorf("%w (see log: %s)", err, logPath)
		}
	}

	modelIDs, err := llamaserver.DiscoverModels(baseURL)
	if err != nil {
		return fmt.Errorf("discovering models: %w", err)
	}

	model, ok := llamaserver.ResolveModel(modelIDs, cfg.model)
	if !ok {
		fmt.Fprintf(os.Stderr, "rnes: %s is not served by %s, trying to download it\n", cfg.model, baseURL)
		if err := llamaserver.Download(cfg.model, os.Stderr); err != nil {
			return err
		}
		modelIDs, err = llamaserver.DiscoverModels(baseURL)
		if err != nil {
			return fmt.Errorf("discovering models: %w", err)
		}
		model, ok = llamaserver.ResolveModel(modelIDs, cfg.model)
		if !ok {
			return fmt.Errorf("model %q is still not served by %s after download; if that llama-server was started with a fixed model (-hf/-m) or before the download, stop it (or use -port) so rnes can start a fresh one", cfg.model, baseURL)
		}
	}

	if err := h.Configure(providerKey, baseURL, modelIDs); err != nil {
		return fmt.Errorf("configuring the harness: %w", err)
	}

	runErr := runHarness(h, cwd, providerKey, model)

	// The last rnes session out stops an rnes-started server, even if a different
	// session started it. Servers rnes didn't start have no record and are left
	// alone.
	if llamaserver.OtherSessionsActive(cfg.port) {
		fmt.Fprintln(os.Stderr, "rnes: another rnes session is still using llama-server, leaving it up")
	} else if proc != nil {
		fmt.Fprintln(os.Stderr, "rnes: stopping llama-server")
		if err := proc.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "rnes: failed to stop llama-server:", err)
		}
		_ = llamaserver.StopRecordedServer(cfg.port) // clears the record
	} else if err := llamaserver.StopRecordedServer(cfg.port); err != nil {
		fmt.Fprintln(os.Stderr, "rnes: failed to stop llama-server:", err)
	}

	return runErr
}
