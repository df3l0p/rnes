package harness

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/df3l0p/rnes/images"
	"github.com/df3l0p/rnes/internal/opencodeconfig"
	"github.com/df3l0p/rnes/internal/proxy"
	"github.com/df3l0p/rnes/internal/skills"
)

const (
	// containerHost is how the proxy container reaches the host's network
	// namespace. Docker Desktop provides it; on Linux startProxyNet adds it
	// with --add-host. The sandbox container itself never resolves it: all
	// its egress, including to the host's llama-server, goes through the
	// proxy instead.
	containerHost = "host.docker.internal"
	// containerConfigPath is where opencode's generated config is mounted.
	// It's kept out of the agent's own config directory so docker doesn't
	// create that directory root-owned inside the container.
	containerConfigPath = "/etc/rnes/opencode.jsonc"
	containerWorkdir    = "/workspace"
	containerHome       = "/home/rnes"
	// containerCADir is where the session's CA volume is mounted: not under
	// /etc/rnes, where the config above is bind-mounted as a single file.
	containerCADir = "/etc/rnes-ca"
	// containerCAPath is the trust bundle in it (the system roots plus the
	// session CA), named by rnes-proxy's -ca-out default.
	containerCAPath = containerCADir + "/ca.pem"
	// containerConfigHome is opencode's ~/.config. With skills attached it is
	// a host staging directory (see stageConfigHome); otherwise the image's.
	containerConfigHome = containerHome + "/.config"
	// containerSkillsDir is where opencode looks for global skills; each
	// attached skill is mounted read-only at <containerSkillsDir>/<name>.
	containerSkillsDir = containerConfigHome + "/opencode/skills"
)

// caTrustEnv is the environment that makes the agent's tools trust
// containerCAPath: Node (and so opencode), Go's crypto/x509 (SSL_CERT_FILE),
// OpenSSL-based clients, git, curl and Python's requests. Several of these
// replace the system roots instead of adding to them, which is why the bundle
// carries the system roots too.
var caTrustEnv = []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "GIT_SSL_CAINFO", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE"}

// sandboxProfile captures the harness-specific pieces of a containerized
// agent so Sandbox can share image building, proxy networking, credential
// placeholders, .rnes policy protection, and container lifecycle across agents.
type sandboxProfile struct {
	// harness names the base image ("base-<harness>").
	harness string
	// needsModelServer reports whether the session uses a local llama-server.
	needsModelServer bool
	// usesGeneratedConfig reports whether Configure writes a session-local
	// config file that Run mounts into the container (opencode.jsonc).
	usesGeneratedConfig bool
	// hostConfig is the host config file path used by Configure when
	// usesGeneratedConfig is true.
	hostConfig string
	// openRouterEnv is the container env var set to openrouterPlaceholder when
	// OpenRouterKey is active (e.g. "OPENROUTER_API_KEY" for opencode).
	openRouterEnv string
	// extraPolicy is appended to the session's proxy policy (e.g. agy's
	// no-inspect Google endpoints).
	extraPolicy string
	// authVolume returns the per-project Docker named volume for dir, or ""
	// when the harness uses no auth volume.
	authVolume func(dir string) string
	// authLabel is the Docker volume label applied when creating authVolume.
	authLabel string
	// authMountTargets are the container paths authVolume is mounted
	// read-write at. One volume may back several credential locations (agy
	// keeps OAuth state under both ~/.gemini/antigravity-cli and
	// ~/.config/antigravity); the paths' file names must not collide, since
	// they all land at the volume's root.
	authMountTargets []string
	// skillsConfigHome is the container parent directory staged from the host
	// when skills (or skill commands) are mounted.
	skillsConfigHome string
	// skillsRelDir is the path relative to skillsConfigHome where each skill
	// is mounted read-only.
	skillsRelDir string
	// generateSkillCommands enables opencode's synthetic /<skill> command
	// generation into skillsConfigHome.
	generateSkillCommands bool
	// containerArgs returns the arguments passed to the image's ENTRYPOINT.
	containerArgs func(modelID string, modelSet bool, extraArgs []string) []string
}

// Sandbox runs a coding agent in a Docker container. The container has no
// direct route to the host; all its egress (including to the host's
// llama-server, when used) goes through its per-session egress proxy, which is
// the one that resolves host.docker.internal. It needs docker on the host, not
// the agent executable itself.
type Sandbox struct {
	profile sandboxProfile
	// name selects the bundled image (see package images).
	name string
	// image is the tag sessions run, set by Prepare.
	image string
	build bool
	// hostConfig is the user's opencode config, used as the base of the
	// generated one when profile.usesGeneratedConfig is true.
	hostConfig string
	// generated is the temp file written by Configure and mounted into the
	// container; removed when Run returns.
	generated string
	// hostIP is what containerHost resolves to inside the proxy container:
	// the bridge gateway IP that llama-server listens on (set by BindHost),
	// or host-gateway when Docker provides the route itself.
	hostIP string
	// proxyImage is the tag of this session's egress proxy image, set by
	// Prepare.
	proxyImage string
	// modelServer is llama-server's "host:port" as the proxy container reaches
	// it (host.docker.internal:<port>), set by Configure. It's added to the
	// proxy's allow-list; it is not the proxy's own address.
	modelServer string
	// allNet adds * to the proxy policy (see Options.AllNet).
	allNet bool
	// inspect has the proxy terminate TLS for the policy's explicit hosts
	// (see Options.NoInspect).
	inspect bool
	// githubToken is the real token, held only to hand to the proxy; the sandbox
	// container never sees it. githubPlaceholder stands in for it there.
	githubToken       string
	githubPlaceholder string
	// openrouterKey and openrouterPlaceholder are the same for OpenRouter.
	openrouterKey         string
	openrouterPlaceholder string
	// skills are mounted read-only under profile.skillsConfigHome.
	skills []skills.Skill
	// customPolicy, when non-nil, replaces defaultPolicy as the base of the
	// session's egress allow-list (see Options.Policy).
	customPolicy []byte
	// configHome is the host staging directory mounted at
	// profile.skillsConfigHome when skills are attached, set by Run.
	configHome string
	// modelSet and extraArgs come from Options.ModelSet and Options.Args.
	modelSet  bool
	extraArgs []string
}

// hostOS is runtime.GOOS, replaceable in tests.
var hostOS = runtime.GOOS

// isLocalIP reports whether ip is assigned to one of this machine's
// interfaces, i.e. whether a server here can listen on it. Replaceable in
// tests.
var isLocalIP = func(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func newSandbox(opts Options, hostConfig string) *Sandbox {
	return newSandboxWithProfile(opts, opencodeProfile(hostConfig))
}

// checkExtraPolicy rejects a session policy that lists one of the profile's
// harness-required no-inspect endpoints for TLS inspection: the harness only
// works with those endpoints tunnelled unread, so the two can't both apply,
// and silently downgrading the user's entry would hide that from them. The
// profile's own defaults (used when policy is nil) never conflict, but the
// check covers them anyway so a future harness can't ship one that does.
func checkExtraPolicy(policy []byte, profile sandboxProfile) error {
	if profile.extraPolicy == "" {
		return nil
	}
	user := policy
	if user == nil {
		user = defaultPolicy
	}
	if len(user) == 0 {
		return nil
	}
	userPol, err := proxy.ParseUserPolicy(bytes.NewReader(user))
	if err != nil {
		return fmt.Errorf("checking the session policy: %w", err)
	}
	extraPol, err := proxy.ParsePolicy(strings.NewReader(profile.extraPolicy))
	if err != nil {
		return fmt.Errorf("checking the %s harness policy: %w", profile.harness, err)
	}
	var conflicts []string
	for hp := range extraPol.NoInspect {
		if userPol.Explicit(hp) && !userPol.SkipsInspect(hp) {
			conflicts = append(conflicts, hp)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)
	return fmt.Errorf("%s requires no-inspect access to %s, but %s lists it for TLS inspection; mark it 'no-inspect' in %s or remove it there",
		profile.harness, strings.Join(conflicts, ", "), ProjectPolicyRel, ProjectPolicyRel)
}

func newSandboxWithProfile(opts Options, profile sandboxProfile) *Sandbox {
	name := opts.Image
	if name == "" {
		name = images.Default
	}
	return &Sandbox{
		profile:       profile,
		name:          name,
		build:         opts.Build,
		allNet:        opts.AllNet,
		inspect:       !opts.NoInspect,
		githubToken:   opts.GitHubToken,
		openrouterKey: opts.OpenRouterKey,
		skills:        opts.Skills,
		customPolicy:  opts.Policy,
		hostConfig:    profile.hostConfig,
		hostIP:        "host-gateway",
		modelSet:      opts.ModelSet,
		extraArgs:     opts.Args,
	}
}

// NeedsModelServer reports whether this sandbox session uses a local
// llama-server.
func (s *Sandbox) NeedsModelServer() bool {
	return s.profile.needsModelServer
}

// AuthVolume returns the Docker named volume used to persist OAuth/session
// state for dir, or "" when this sandbox uses no auth volume.
func (s *Sandbox) AuthVolume(dir string) string {
	if s.profile.authVolume == nil {
		return ""
	}
	return s.profile.authVolume(dir)
}

// BindHost returns the address llama-server should listen on so the container
// can reach it without exposing it to the network.
//
// Where Docker runs in a VM (any macOS, Docker Desktop) it forwards
// host.docker.internal to the host's loopback, so 127.0.0.1 is enough. With a
// native Linux engine the container's route to the host is the default
// bridge's gateway IP, so that is the address to listen on: it isn't reachable
// from outside the machine. The container is then pointed at that same IP, so
// the two can't disagree.
func (s *Sandbox) BindHost() (string, error) {
	if hostOS == "darwin" {
		return "127.0.0.1", nil
	}
	out, err := exec.Command("docker", "info", "--format", "{{.OperatingSystem}}").Output()
	if err != nil {
		return "", fmt.Errorf("querying docker info: %w", err)
	}
	if strings.Contains(string(out), "Docker Desktop") {
		return "127.0.0.1", nil
	}

	out, err = exec.Command("docker", "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
	if err != nil {
		return "", fmt.Errorf("querying docker's bridge network: %w (pass -host to choose the listen address yourself)", err)
	}
	for _, field := range strings.Fields(string(out)) {
		if ip := net.ParseIP(field); ip != nil && ip.To4() != nil {
			// With rootless Docker or a remote daemon the bridge lives in
			// another network namespace, so its gateway isn't an address
			// llama-server could bind here.
			if !isLocalIP(ip) {
				return "", fmt.Errorf("docker's bridge gateway %s isn't an address of this machine (rootless or remote docker?); pass -host to choose the listen address yourself", field)
			}
			s.hostIP = field
			return field, nil
		}
	}
	return "", fmt.Errorf("couldn't determine docker's bridge gateway IP (pass -host to choose the listen address yourself)")
}

func (s *Sandbox) Available() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH (required for -sandbox): %w", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return fmt.Errorf("docker is installed but its daemon isn't reachable (is it running?): %w", err)
	}
	return nil
}

// Prepare makes sure the selected sandbox image exists locally. rnes never pulls
// its images from a registry (supply-chain hygiene): it uses a local image if
// present, and otherwise builds it from the bundled Dockerfile, failing if the
// build fails. Every selectable image is a layer on the harness's internal base
// image, which is ensured first. With -build both are rebuilt even if they exist.
//
// Images are tagged rnes-sandbox-<name>:<hash>, the hash covering the Dockerfile
// (and, for a layer, its base's tag), so a changed Dockerfile builds a new image
// and an unchanged one is reused. An unknown name fails before docker is used.
func (s *Sandbox) Prepare() error {
	return s.prepare([]string{s.name})
}

// PrepareAll is Prepare for every bundled selectable image on this harness's
// base, with the base and the proxy image built once: what -sandbox-update does
// when -harness is explicitly selected without -image.
func (s *Sandbox) PrepareAll() error {
	return s.prepare(images.Names())
}

func (s *Sandbox) prepare(names []string) error {
	if err := s.prepareImages(names); err != nil {
		return err
	}
	proxyTag, err := prepareProxyImage(s.build)
	if err != nil {
		return fmt.Errorf("preparing proxy image: %w", err)
	}
	s.proxyImage = proxyTag
	return nil
}

func (s *Sandbox) prepareImages(names []string) error {
	base, err := images.ReadBase(s.profile.harness)
	if err != nil {
		return err
	}
	dockerfiles := make([][]byte, len(names))
	for i, name := range names {
		if dockerfiles[i], err = images.Read(name); err != nil {
			return err
		}
	}

	baseTag := imageTag(images.BaseName(s.profile.harness), base, "")
	if err := s.ensure(baseTag, base, ""); err != nil {
		return err
	}

	for i, name := range names {
		tag := imageTag(name, dockerfiles[i], baseTag)
		if err := s.ensure(tag, dockerfiles[i], baseTag); err != nil {
			return err
		}
		if name == s.name {
			s.image = tag
		}
	}
	return nil
}

// ensure builds tag from dockerfile unless it exists locally (or -build was
// given). A non-empty baseTag is passed as the RNES_BASE build arg. -build also
// skips docker's layer cache: the Dockerfiles' apt and installer steps are
// unpinned, so cached layers would keep serving old packages and an old agent.
func (s *Sandbox) ensure(tag string, dockerfile []byte, baseTag string) error {
	if !s.build && exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "rnes: building %s\n", tag)
	args := []string{"build", "-t", tag, "-"}
	if baseTag != "" {
		args = layerBuildArgs(tag, baseTag)
	}
	if s.build {
		args = append([]string{"build", "--no-cache"}, args[1:]...)
	}
	// A Dockerfile on stdin means no build context, which is all it needs.
	cmd := exec.Command("docker", args...)
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", tag, err)
	}
	return nil
}

// imageTag names an image built from dockerfile. baseTag is the tag of the
// image it layers on, or "" for the base image itself.
func imageTag(name string, dockerfile []byte, baseTag string) string {
	sum := sha256.New()
	sum.Write(dockerfile)
	sum.Write([]byte{0})
	sum.Write([]byte(baseTag))
	return "rnes-sandbox-" + name + ":" + hex.EncodeToString(sum.Sum(nil))[:12]
}

// layerBuildArgs is the docker argv that builds a layer's Dockerfile, read from
// stdin, with the base image's tag as its RNES_BASE build arg.
func layerBuildArgs(tag, baseTag string) []string {
	return []string{"build", "--build-arg", "RNES_BASE=" + baseTag, "-t", tag, "-"}
}

// Configure writes a copy of the host opencode config, with the provider
// pointed at baseURL as seen from inside a container, to a private temp file.
// For harnesses that don't use a generated config file, Configure is a no-op.
func (s *Sandbox) Configure(providerKey, baseURL string, modelIDs []string) error {
	if !s.profile.usesGeneratedConfig {
		return nil
	}
	containerURL, err := containerBaseURL(baseURL)
	if err != nil {
		return err
	}
	if u, err := url.Parse(containerURL); err == nil {
		s.modelServer = u.Host // already "host.docker.internal:<port>"
	}
	out, err := opencodeconfig.Render(s.hostConfig, providerKey, opencodeProvider(containerURL, modelIDs))
	if err != nil {
		return err
	}
	// A literal key in the host's openrouter block would reach the container
	// and override the placeholder, so the proxy would never be involved.
	if s.openrouterKey != "" {
		var dropped bool
		if out, dropped, err = opencodeconfig.DropProviderOption(out, "openrouter", "apiKey"); err != nil {
			return err
		}
		if dropped {
			fmt.Fprintln(os.Stderr, "rnes: warning: removed the openrouter apiKey from the sandbox's copy of your opencode config; the proxy injects RNES_OPENROUTER_API_KEY instead")
		}
	}
	// CreateTemp creates the file 0600; the container runs as the same uid.
	f, err := os.CreateTemp("", "rnes-sandbox-config-*.jsonc")
	if err != nil {
		return fmt.Errorf("creating sandbox config: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	s.generated = f.Name()
	return nil
}

func (s *Sandbox) Run(dir, providerKey, modelID string) error {
	if s.profile.usesGeneratedConfig {
		if s.generated == "" {
			return fmt.Errorf("sandbox: Configure must be called before Run")
		}
		defer os.Remove(s.generated)
	}
	if s.image == "" || s.proxyImage == "" {
		return fmt.Errorf("sandbox: Prepare must be called before Run")
	}
	if s.profile.usesGeneratedConfig {
		if err := s.applyModel(providerKey, modelID); err != nil {
			return err
		}
	}
	// Ensure <dir>/.rnes exists on the host (owned by the user, never a
	// symlink) so runArgs can always bind-mount it read-only at /workspace/.rnes.
	if _, err := EnsureProjectDir(dir); err != nil {
		return err
	}

	if vol := s.AuthVolume(dir); vol != "" {
		volArgs := []string{"volume", "create"}
		if s.profile.authLabel != "" {
			volArgs = append(volArgs, "--label", s.profile.authLabel)
		}
		volArgs = append(volArgs, vol)
		if out, err := exec.Command("docker", volArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("creating auth volume %s: %w: %s", vol, err, strings.TrimSpace(string(out)))
		}
	}

	// Stage the skills parent directory when skills are attached (or, for
	// opencode, when project skills get synthetic /<name> commands).
	var commands []string
	if s.profile.generateSkillCommands {
		commands = s.skillCommandNames(dir)
	}
	if len(s.skills) > 0 || len(commands) > 0 {
		home, err := stageSkillsHome(s.profile.skillsRelDir, s.skills)
		if err != nil {
			return err
		}
		s.configHome = home
		if s.profile.generateSkillCommands {
			s.writeSkillCommands(home, commands)
		}
		defer func() {
			if err := os.RemoveAll(home); err != nil {
				fmt.Fprintf(os.Stderr, "rnes: warning: couldn't remove the sandbox's skills staging directory %s: %v\n", home, err)
			}
		}()
	}

	if s.githubToken != "" {
		placeholder, err := newPlaceholder()
		if err != nil {
			return err
		}
		s.githubPlaceholder = placeholder
	}
	if s.openrouterKey != "" {
		placeholder, err := newPlaceholder()
		if err != nil {
			return err
		}
		s.openrouterPlaceholder = placeholder
	}
	providers, err := s.providerConfig()
	if err != nil {
		return err
	}
	pn, err := startProxyNet(s.proxyImage, s.hostIP, s.policy(), s.inspect, providers)
	if err != nil {
		return fmt.Errorf("starting sandbox proxy: %w", err)
	}
	defer pn.stop()
	if pn.logFile != "" {
		// To stderr, before the agent takes the terminal over.
		fmt.Fprintf(os.Stderr, "rnes: sandbox proxy log: %s\n", pn.logFile)
	}

	// Unique per invocation so parallel sessions never collide, whatever port
	// they share.
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("generating container name: %w", err)
	}
	name := fmt.Sprintf("rnes-%d-%s", os.Getpid(), hex.EncodeToString(suffix[:]))

	// Backstop for a container left behind if docker's client dies without the
	// daemon noticing; --rm covers the normal path.
	defer exec.Command("docker", "rm", "-f", name).Run()

	cmd := exec.Command("docker", s.runArgsForModel(name, dir, pn, stdinIsTTY(), modelID)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	// Stop the proxy before reading its log so the follower has flushed any
	// final lines; defer pn.stop() above remains a no-op backstop.
	pn.stop()
	if summary := BlockedSummary(pn.logFile); summary != "" {
		fmt.Fprintf(os.Stderr, "rnes: proxy blocked: %s (see %s)\n", summary, pn.logFile)
	}
	return runErr
}

// applyModel makes the session start on providerKey/modelID. The harness's
// session UI may not accept a model flag, so the model rides in the
// generated config's top-level "model" default. The config is session-local
// and mounted read-only into the container, so nothing outlives the session.
func (s *Sandbox) applyModel(providerKey, modelID string) error {
	raw, err := os.ReadFile(s.generated)
	if err != nil {
		return fmt.Errorf("reading generated config: %w", err)
	}
	raw, err = opencodeconfig.SetModel(raw, providerKey+"/"+modelID)
	if err != nil {
		return fmt.Errorf("setting the sandbox's model: %w", err)
	}
	if err := os.WriteFile(s.generated, raw, 0o600); err != nil {
		return fmt.Errorf("writing generated config: %w", err)
	}
	return nil
}

// policy is this session's proxy allow-list: the project's policy (or the
// shipped defaults) plus any harness-specific endpoints, llama-server, and
// with -all-net any public host. Built fresh each call, so sessions never share
// or change defaultPolicy.
func (s *Sandbox) policy() []byte {
	var b bytes.Buffer
	if s.customPolicy != nil {
		b.Write(s.customPolicy)
	} else {
		b.Write(defaultPolicy)
	}
	if s.profile.extraPolicy != "" {
		b.WriteString("\n" + s.profile.extraPolicy)
	}
	if s.modelServer != "" {
		b.WriteString("\n# this session's llama-server\nprivate " + s.modelServer + "\n")
	}
	if s.openrouterKey != "" {
		b.WriteString("\n# OpenRouter (RNES_OPENROUTER_API_KEY is set)\nopenrouter.ai:443\n")
	}
	if s.allNet {
		b.WriteString("\n# -all-net: any public host\n*\n")
	}
	return b.Bytes()
}

// runArgs builds the docker argv for one session without an explicit model ID
// (kept as a convenience for existing tests).
func (s *Sandbox) runArgs(name, dir string, pn *proxyNet, tty bool) []string {
	return s.runArgsForModel(name, dir, pn, tty, "")
}

// runArgsForModel builds the docker argv for one session. The sandbox gets no
// direct host.docker.internal route or unrestricted egress: it only reaches
// pn's network, and all HTTP(S) traffic is forced through pn's proxy
// container. Both spellings of the proxy variables are set: curl, for one,
// ignores uppercase HTTP_PROXY.
func (s *Sandbox) runArgsForModel(name, dir string, pn *proxyNet, tty bool, modelID string) []string {
	flags := "-i"
	if tty {
		flags = "-it"
	}
	args := []string{
		// --pull=never: Prepare already ensured the image locally; never let
		// docker fetch one from a registry behind our back.
		"run", "--rm", "--pull=never", flags,
		"--name", name,
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges:true",
		"--network", pn.network,
		"-e", "HTTP_PROXY=" + pn.proxyURL(),
		"-e", "HTTPS_PROXY=" + pn.proxyURL(),
		"-e", "http_proxy=" + pn.proxyURL(),
		"-e", "https_proxy=" + pn.proxyURL(),
		// Loopback inside the sandbox is its own network namespace, not the
		// host's or the proxy's — never send it through the proxy.
		"-e", "NO_PROXY=localhost,127.0.0.1,::1",
		"-e", "no_proxy=localhost,127.0.0.1,::1",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "HOME=" + containerHome,
	}
	if s.profile.usesGeneratedConfig {
		args = append(args, "-e", "OPENCODE_CONFIG="+containerConfigPath)
	}
	args = append(args,
		"-v", dir+":"+containerWorkdir,
		"--mount", bindMount(filepath.Join(dir, ProjectStateDir), containerWorkdir+"/"+ProjectStateDir, true),
	)
	if s.profile.usesGeneratedConfig {
		args = append(args, "-v", s.generated+":"+containerConfigPath+":ro")
	}
	if vol := s.AuthVolume(dir); vol != "" && len(s.profile.authMountTargets) > 0 {
		for _, target := range s.profile.authMountTargets {
			args = append(args, "-v", vol+":"+target)
		}
	}
	// The proxy terminates TLS for some hosts with its own CA: trust it, via
	// the volume it wrote its bundle to. Mounted at runtime, never baked into
	// an image, since the CA is per session.
	if pn.caVolume != "" {
		args = append(args, "-v", pn.caVolume+":"+containerCADir+":ro")
		for _, v := range caTrustEnv {
			args = append(args, "-e", v+"="+containerCAPath)
		}
	}
	if s.githubPlaceholder != "" {
		args = append(args, githubEnv(s.githubPlaceholder)...)
	}
	if s.openrouterPlaceholder != "" && s.profile.openRouterEnv != "" {
		args = append(args, "-e", s.profile.openRouterEnv+"="+s.openrouterPlaceholder)
	}
	args = append(args, s.skillMounts()...)
	args = append(args, "-w", containerWorkdir, s.image)
	if s.profile.containerArgs != nil {
		args = append(args, s.profile.containerArgs(modelID, s.modelSet, s.extraArgs)...)
	}
	return args
}

// newPlaceholder returns an opaque stand-in for a credential.
func newPlaceholder() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a credential placeholder: %w", err)
	}
	return "rnes_placeholder_" + hex.EncodeToString(b[:]), nil
}

// skillMounts are the docker arguments for the staged skills parent directory
// (nothing when the session has none) and for attaching s.skills: each skill's
// directory bind-mounted read-only at <skillsConfigHome>/<skillsRelDir>/<name>,
// so it stays live and nothing else on the host is visible. A symlink inside a
// skill resolves in the container, so one pointing outside its directory
// dangles.
//
// Docker would create the mount points' missing parents root-owned, leaving the
// agent unable to write its config directory. So skillsConfigHome itself is a
// host staging directory owned by the session's user, with the mount points
// already in it (see stageSkillsHome).
func (s *Sandbox) skillMounts() []string {
	if s.configHome == "" {
		return nil
	}
	targetBase := s.profile.skillsConfigHome + "/" + s.profile.skillsRelDir
	args := []string{"--mount", bindMount(s.configHome, s.profile.skillsConfigHome, false)}
	for _, sk := range s.skills {
		args = append(args, "--mount", bindMount(sk.Dir, targetBase+"/"+sk.Name, true))
	}
	return args
}

// bindMount is a --mount value. Its fields are CSV, as docker parses them, so a
// path with a comma or a quote still means what it says (unlike -v, which
// splits on colons).
func bindMount(src, dst string, readonly bool) string {
	fields := []string{"type=bind", "source=" + src, "target=" + dst}
	if readonly {
		fields = append(fields, "readonly")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(fields) // a strings.Builder can't fail
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// stageConfigHome creates opencode's staged ~/.config directory with
// opencode/skills/<name> mount points.
func stageConfigHome(attached []skills.Skill) (string, error) {
	return stageSkillsHome("opencode/skills", attached)
}

// stageSkillsHome creates the host directory mounted at skillsConfigHome when
// skills are attached: private to the user, with an empty <relDir>/<name> mount
// point per skill. Creating the mount points here means docker doesn't create
// them, as root, in a directory rnes must remove afterwards.
func stageSkillsHome(relDir string, attached []skills.Skill) (string, error) {
	dir, err := os.MkdirTemp("", "rnes-sandbox-home-config-*")
	if err != nil {
		return "", fmt.Errorf("creating the sandbox's skills staging directory: %w", err)
	}
	for _, sk := range attached {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(relDir), sk.Name), 0o755); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("creating the sandbox's skills staging directory: %w", err)
		}
	}
	return dir, nil
}

// skillCommandNames lists the skills the sandbox gets a /<name> command for:
// the attached ones plus the project's own, which come through the mounted
// workdir. A name opencode already has a command for is left out with a
// warning (see commandNames); project commands win in the container anyway.
// Only cwd is checked, not its ancestors: only the workdir is mounted, so
// host ancestors are not visible in the container.
func (s *Sandbox) skillCommandNames(cwd string) []string {
	names := make([]string, 0, len(s.skills))
	seen := make(map[string]bool, len(s.skills))
	for _, sk := range s.skills {
		names = append(names, sk.Name)
		seen[sk.Name] = true
	}
	for _, sk := range skills.DiscoverProject(cwd) {
		if !seen[sk.Name] {
			names = append(names, sk.Name)
			seen[sk.Name] = true
		}
	}
	sort.Strings(names)
	names, warnings := commandNames(commandScope{cwd: cwd, stop: cwd}, names)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "rnes: warning: %s\n", w)
	}
	return names
}

// writeSkillCommands writes a /<name> command file per name into the staged
// ~/.config (see opencode_commands.go), so the session can invoke the skills
// like host commands. A failed write warns but doesn't stop the session: the
// skills themselves still work.
func (s *Sandbox) writeSkillCommands(home string, names []string) {
	if len(names) == 0 {
		return
	}
	if err := writeCommandFiles(home, names); err != nil {
		fmt.Fprintf(os.Stderr, "rnes: warning: writing the sandbox's skill commands: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "rnes: skill commands: /%s\n", strings.Join(names, ", /"))
}

// providerConfig is the line of provider config for rnes-proxy's stdin, or nil
// when the session has no credentials.
func (s *Sandbox) providerConfig() ([]byte, error) {
	var cfgs []proxy.ProviderConfig
	if s.githubToken != "" {
		cfgs = append(cfgs, proxy.ProviderConfig{Name: "github", Token: s.githubToken, Placeholder: s.githubPlaceholder})
	}
	if s.openrouterKey != "" {
		cfgs = append(cfgs, proxy.ProviderConfig{Name: "openrouter", Token: s.openrouterKey, Placeholder: s.openrouterPlaceholder})
	}
	if len(cfgs) == 0 {
		return nil, nil
	}
	return proxy.EncodeProviders(cfgs)
}

// githubEnv is what makes gh and git use the placeholder: gh reads GH_TOKEN, and
// git gets a credential helper that answers with it (the empty first helper
// clears any inherited ones).
func githubEnv(placeholder string) []string {
	const key = "credential.https://github.com.helper"
	return []string{
		"-e", "GH_TOKEN=" + placeholder,
		"-e", "GH_NO_UPDATE_NOTIFIER=1",
		"-e", "GIT_TERMINAL_PROMPT=0",
		"-e", "GIT_CONFIG_COUNT=2",
		"-e", "GIT_CONFIG_KEY_0=" + key,
		"-e", "GIT_CONFIG_VALUE_0=",
		"-e", "GIT_CONFIG_KEY_1=" + key,
		"-e", `GIT_CONFIG_VALUE_1=!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f`,
	}
}

// containerBaseURL rewrites a host-side URL (e.g. http://127.0.0.1:8080) to
// the address the container uses to reach the same server.
func containerBaseURL(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing base URL %q: %w", baseURL, err)
	}
	port := u.Port() // read before Host is overwritten
	u.Host = containerHost
	if port != "" {
		u.Host = net.JoinHostPort(containerHost, port)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// stdinIsTTY reports whether stdin is a terminal. A ModeCharDevice check isn't
// enough (it's also true for /dev/null), and the isatty ioctl is
// platform-specific without golang.org/x/term, so ask the shell's test -t.
func stdinIsTTY() bool {
	cmd := exec.Command("sh", "-c", "test -t 0")
	cmd.Stdin = os.Stdin
	return cmd.Run() == nil
}
