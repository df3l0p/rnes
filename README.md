# rnes

`rnes` runs a coding agent against a local model with one command: it starts
`llama-server` (llama.cpp) if one isn't already running, points the agent at
it, and launches the agent in the current directory.

It can also run a harness that uses its own cloud backend instead of a local
model: `rnes -sandbox -harness agy` starts the Google Antigravity CLI (`agy`)
against your Google subscription, with no `llama-server` involved (see
*Antigravity* below). `-harness vibe` runs the Mistral Vibe CLI against the
local model, on the host or in the sandbox (see *Vibe* below).

Concretely, running `rnes` in a project:

1. Checks whether a `llama-server`/`llama serve` instance is already
   healthy on the target host/port; if not, starts one in router mode (no
   model flag), which serves every model in the llama.cpp cache and loads
   them on demand.
2. Discovers the served models and merges an OpenAI-compatible provider
   entry listing all of them into the agent's config. If `-model` isn't
   among them, it's downloaded first (`llama download`, unified CLI only),
   then the server's model catalog is refreshed.
3. Runs the agent in the current directory with `-model` selected.
4. On exit, stops the `llama-server` it started — unless another `rnes`
   process is still using it, in which case it's left running.

With a cloud harness (`agy`) steps 1, 2 and 4 are skipped: the agent talks to
its own provider, and only the sandbox's egress proxy runs alongside it.

## Requirements

- [opencode](https://opencode.ai) v2 on `PATH` for the default `opencode`
  harness, and [Vibe](https://github.com/mistralai/mistral-vibe) on `PATH` for
  `-harness vibe` on the host (`uv tool install mistral-vibe`).
  [`agy`](https://antigravity.google) and Vibe are installed inside their
  sandbox images, so a sandbox session needs only `docker` on the host.
- llama.cpp on `PATH` for harnesses that use a local model: either the unified
  `llama` CLI (`llama serve`) or the older standalone `llama-server` binary. On
  macOS, `brew install llama.cpp` provides both. Not needed for
  `-harness agy`.
- `git` on `PATH` for `-worktree`.

## Install

```sh
go install github.com/df3l0p/rnes@latest
```

or build from a checkout:

```sh
go build -o rnes .
```

## Usage

```sh
cd your-project
rnes
```

This starts (or reuses) a llama-server serving the default model, merges a
`llama-cpp` provider into opencode's config, and runs `opencode .`. opencode
v2's full-screen UI has no model flag, so on the host you pick the
llama-server model with `/models`; in the sandbox, rnes writes the model into
the session's generated config so it starts on it.

### Flags

| Flag        | Default                                          | Description                                     |
|-------------|---------------------------------------------------|--------------------------------------------------|
| `-model`    | `unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M`             | Model to serve; a sandbox session starts on it, and so does `-harness vibe` on the host (via `VIBE_ACTIVE_MODEL`). With opencode on the host, pick it via `/models`. With `-harness agy`, forwarded as `--model` only when given |
| `-host`     | `127.0.0.1`                                       | llama-server host (with `-sandbox`, the narrowest address containers can reach; see below); rejected for harnesses that don't use llama-server |
| `-port`     | `8080`                                            | llama-server port; rejected for harnesses that don't use llama-server |
| `-harness`  | `opencode`                                        | Coding agent to run: `opencode`, `agy` (see *Antigravity* below) or `vibe` (see *Vibe* below) |
| `-sandbox`  | off                                               | Run the agent in a Docker container (see below)  |
| `-worktree` | none                                              | Create (or reuse) the linked git worktree `<repository>/.worktree/<name>` and run the session there; works on the host and with `-sandbox` (see *Worktrees* below) |
| `-image`    | `default`                                         | Bundled sandbox image to use (see below; requires `-sandbox`) |
| `-build`    | off                                               | Rebuild the sandbox image even if it exists (requires `-sandbox`) |
| `-sandbox-update` | off                                         | Rebuild the sandbox images from scratch, then exit without running the agent (implies `-sandbox` and `-build`). Without `-harness`, rebuilds every harness's images; with it, only that harness's (and, without `-image`, every image on its base) |
| `-generate-policy` | off                                        | Write a starter `.rnes/policy.yaml` in the current directory for `-sandbox`, then exit without running the agent |
| `-allow-public` | off                                          | Let the sandbox reach any public host, not just the allow-list (requires `-sandbox`; see below) |
| `-allow-private` | off                                         | Let the sandbox reach private addresses (RFC 1918 / IPv6 ULA) outside the allow-list; listed hosts keep their public-only check unless marked `private: true` in `policy.yaml` (requires `-sandbox`; see below) |
| `-no-inspect` | off                                             | Don't decrypt the sandbox's HTTPS in the proxy; tunnel it unread (requires `-sandbox`; see below) |
| `-no-keychain` | off                                            | Don't read the Mistral API key Vibe's browser sign-in stored in the macOS Keychain for `-sandbox -harness vibe` sessions (requires `-sandbox`; see *Mistral models* below) |
| `-web`      | off                                               | Open the session's web UI in your browser at startup; the TUI keeps running and both share one live session |
| `-no-web`   | off                                               | Don't serve the session's web UI at all (with `-sandbox` it is exposed on a loopback port by default) |
| `-skill`    | none                                              | Attach a host skill to the sandbox, read-only: a name, a path to a skill directory, or `all`. Repeatable (requires `-sandbox`; see *Skills* below) |
| `-skills-dir` | none                                            | Add a host folder of skills to search, before the default ones, for `-skill` names and `all`; attaches nothing on its own. Repeatable (requires `-sandbox`) |

Arguments after `--` are forwarded to the harness executable, so flags rnes
doesn't own reach the agent unchanged:

```sh
rnes -sandbox -harness agy -- --version
rnes -sandbox -harness agy -- --continue
rnes -sandbox -harness opencode -- --continue
rnes -sandbox -harness vibe -- --continue
```

### Worktrees

`-worktree <name>` runs a session in its own linked git worktree instead of
the current checkout:

```sh
cd your-project
rnes -worktree feature-01
rnes -sandbox -worktree feature-01
```

It creates `<repository>/.worktree/feature-01` (on a new `feature-01` branch,
or on an existing branch of that name) from the repository's current `HEAD`,
then treats that directory as the project for the rest of the session: the
harness starts there, `-skill` paths and `.rnes` policy are resolved there, and
`-sandbox` mounts it at `/workspace`. Running the same command again reuses the
worktree. The name must be one path component and a valid git branch name;
`-worktree` requires `git` and can't be combined with `-sandbox-update`.

A linked worktree's `.git` is a *file* pointing outside the worktree (at the
repository's shared `.git/worktrees/<name>`), so mounting only the worktree
would leave `git` unable to find the repository. To keep git working in the
sandbox, rnes also mounts that shared git directory read-write at the path the
`.git` file names, so git can update the worktree's index, HEAD and refs. Its
`hooks/` and `config` (including `config.worktree`) are mounted read-only, so
the agent can run git commands but cannot plant host hooks or change the
repository's git configuration. The rest of the repository is not mounted.
rnes-created worktrees use absolute paths; the bundled sandbox git (2.39)
rejects the `relativeworktrees` extension used by
`git worktree add --relative-paths`, so recreate a manual relative worktree
without that option (or use a newer git in the sandbox).

The worktree and its branch are left in place when the session ends, so the
next `-worktree <name>` continues where it stopped. Remove one with
`git worktree remove .worktree/<name>` (and `git branch -d <name>`). If a
worktree created by an older rnes still has a Docker Desktop for Mac
`deny delete` ACL on `.rnes`, clear it first with
`chmod -N .worktree/<name>/.rnes`; new sessions don't create that ACL.

Each harness owns its own config path internally (opencode's is its default
global config, `~/.config/opencode/opencode.jsonc`; vibe's is
`$VIBE_HOME/config.toml`, `~/.vibe/config.toml` by default) — there's no flag
for it.
`rnes` merges into that file without touching any other keys already there,
and points the agent at it via that agent's own config env var
(`OPENCODE_CONFIG` for opencode), so it works even if the agent's normal
config resolution would otherwise pick something else up. For vibe, rnes
replaces a marked block at the end of `config.toml` and leaves the rest of the
file, including your `active_model`, untouched (the session's model is pinned
through `VIBE_ACTIVE_MODEL` instead). `agy` runs only in the sandbox
and keeps its own state in a per-project Docker volume (see *Antigravity*
below).

### Web UI and diff review

opencode's server always serves the web UI. `-sandbox` sessions expose it on a
**loopback-only** port by default, so you can start working and switch to the
browser later without restarting; on the host the TUI's own server already
serves it, and `/web` (alias `/pair`) prints a fresh link whenever you want.
`-web` just opens the browser at startup:

```sh
rnes            # TUI; web available later
rnes -web       # TUI + browser opened now
rnes -no-web    # don't expose the web UI (sandbox: no loopback forward)
```

- **Sandbox, no `-web`:** rnes prints the address and password before the TUI
  starts (`rnes: web UI: http://127.0.0.1:<port> (password: …)`). Open it later
  and choose "Use password". `-web` instead prints and opens a one-time pairing
  link.
- **Host:** `/web` in the TUI prints a fresh one-time link to the same session.
- The TUI and the browser are two clients of **one** server, so they share the
  same live session: a prompt sent from either appears in both. (`opencode`'s
  sessions live in the server, and both clients subscribe to its events.)
- The web UI includes opencode's built-in **review** panel: the diff with
  unified/split views, a file filter, and inline **line and range comments**.
  That is the pane with live comments; the TUI's own `/diff` is read-only.
- **Host sessions with `-web`** serve on a loopback port rnes picks:

  ```text
  rnes: web server: http://127.0.0.1:54321 (log: …)
  rnes: web UI: http://127.0.0.1:54321/auth/connect/<code>
  ```

- **Sandbox sessions** serve inside the container, which is on an `--internal`
  network: Docker can't publish a port for it directly. Instead the sandbox's
  proxy container (the one that already has a route to the host) publishes a
  **loopback-only** port and forwards it to the sandbox's server. The browser
  still connects to `http://127.0.0.1:<port>/…`; the container never gets an
  inbound route and cannot use the forwarder to reach anything.
- The pairing link is one-time and expires in 5 minutes; opening it sets a
  cookie that lasts 30 days. `rnes` prints it either way, so if the browser
  can't be opened automatically (remote/headless), copy the link.
- Inside the TUI, `/web` (an alias of `/pair`) shows the pairing link too. On
  the host it points at the same server; in the sandbox that link is
  container-local, so use the one `rnes -web` prints instead.

`-web` works with and without `-sandbox`. The server's log is kept in
`$TMPDIR/rnes-opencode-serve-<pid>.log` on the host (and
`/tmp/rnes-opencode-serve.log` inside the sandbox).

### Environment

| Variable                  | Description |
| ------------------------- | ----------- |
| `RNES_GITHUB_TOKEN`       | GitHub token the sandbox's proxy injects for `git` and `gh` (requires `-sandbox`; see *GitHub credentials* below). Prefer a fine-grained token; to reuse your `gh` login, `export RNES_GITHUB_TOKEN=$(gh auth token)`. |
| `RNES_OPENROUTER_API_KEY` | OpenRouter API key the sandbox's proxy injects at `openrouter.ai` (requires `-sandbox`; ignored with `-harness agy` or `-harness vibe`, which have no OpenRouter provider; see *OpenRouter models* below). Set with `export RNES_OPENROUTER_API_KEY=$(cat ~/.local/share/opencode/auth.json \| jq -c -r '.openrouter.key')`. |
| `RNES_MISTRAL_API_KEY`    | Mistral API key the sandbox's proxy injects at `api.mistral.ai` (requires `-sandbox`; ignored with `-harness agy`; see *Mistral models* below). To reuse the key opencode stores, set with `export RNES_MISTRAL_API_KEY=$(cat ~/.local/share/opencode/auth.json \| jq -c -r '.mistral.key')`; a Vibe subscription key also works (on macOS, `export RNES_MISTRAL_API_KEY=$(security find-generic-password -s ai.mistral.vibe -a MISTRAL_API_KEY -w)` after browser sign-in, or let `-sandbox -harness vibe` read it for you; on Linux, copy the key from `https://chat.mistral.ai/code/extensions` (Advanced section)). |

Those three variables are just the built-in cases: any `-sandbox` session can
inject another host environment variable through `.rnes/policy.yaml` with
`key`, `value` and an optional `format` (see *Sandbox* above and
[`docs/policy.example.yaml`](docs/policy.example.yaml)).

### Sharing a llama-server across sessions

If a healthy llama-server is already listening on the target port, `rnes`
reuses it instead of starting a second one. Each running `rnes` process
registers itself as a user of that port; when an `rnes` that started the
server exits, it only stops the server once no other `rnes` process is still
registered against it.

Because the server runs in router mode, sessions sharing it can each pick a
different `-model`. A server started some other way with a fixed model
(`-hf`/`-m`) only serves that model; `rnes -model <other>` against it fails
with an error rather than silently using the wrong model — stop it or use
`-port`.

### Sandbox mode

`rnes -sandbox` runs the agent inside a Docker container instead of on the
host. The llama-server still runs on the host; the sandbox container has no
direct route to it and instead reaches it through its per-session egress
proxy, which is what resolves `host.docker.internal`. The current directory
is bind-mounted at `/workspace` (with `/workspace/.rnes` shadowed by a
read-only tmpfs, and `<cwd>/.rnes/policy.yaml` mounted read-only inside it when
present), and the container runs as your uid/gid with all Linux
capabilities dropped (`--cap-drop=ALL`) and `no-new-privileges` enabled so
files it writes keep your ownership and SUID/capability elevation is disabled.
Only `docker` is needed on the host (not the agent). The mounted workspace is
trusted by git (`safe.directory=/workspace`): Docker Desktop can report the
mount root as owned by another user, which git otherwise rejects as "dubious
ownership".

- **Config:** for the `opencode` harness, `rnes` generates a private copy of
  your opencode config with the `llama-cpp` provider's `baseURL` rewritten for
  the container and mounts it read-only. Your real config is not modified.
  For `vibe` it generates a `config.toml` from scratch instead — the local
  provider, one model per served model, and the session's model as the
  default — and puts it in a staged `~/.vibe` that is removed when the session
  ends. Nothing is copied from your own `config.toml`, so no credential it
  holds (an MCP server's static header, a provider's `extra_headers`) reaches
  the container; the cost is that the providers, models and MCP servers in your
  own `config.toml` are not available in a vibe sandbox session. A project
  `.vibe/config.toml` in the workspace still loads (see *Vibe* below), as does
  anything else in the mounted directory. `agy` has no generated config; it
  keeps its own state in a per-project volume (see *Antigravity* below).
- **Images:** `-image <name>` selects one of the images bundled in
  [`images/`](images), each a `<name>.Dockerfile`. The default, `default`,
  adds `jq`, `ripgrep`, `curl` and `gh` on top of the selected harness's base
  (opencode for `-harness opencode`, agy for `-harness agy`, vibe for
  `-harness vibe`). Every image is a layer on an internal per-harness base
  image (`base-opencode`, `base-agy`, `base-vibe`; each installs its agent,
  git and the non-root user) that isn't selectable itself.
  `rnes` never pulls a sandbox image from a registry: a missing image is built
  from its Dockerfile (a build failure is an error). `-build` rebuilds the
  images from scratch, ignoring docker's layer cache, so it also picks up new
  apt package versions and a newer agent; without it, an existing image
  is reused indefinitely. `-sandbox-update` is the same rebuild, but of every
  harness's images (or only `-harness`'s, and only `-image`'s when given), and
  it exits instead of running a session (it implies `-sandbox` and `-build`).
  Images are tagged `rnes-sandbox-<name>:<hash>` from the Dockerfile's contents
  (and that of the base), so a changed Dockerfile builds a new image and an
  unchanged one is reused. Note the build itself still fetches the
  `node:22-slim` or `debian:bookworm-slim` base image, apt packages and the
  agent installer; to control that, review or pin them in the Dockerfiles. Old
  tags are not
  removed automatically; list them with `docker images 'rnes-sandbox-*'` and
  delete the ones you no longer need with `docker rmi`.
  - **Adding an image:** add `images/<name>.Dockerfile` as a layer on a base:
    declare `ARG RNES_BASE`, make the final stage `FROM ${RNES_BASE}` (rnes
    passes the built base image as that build arg), and end with `USER rnes`,
    after a `USER root` for installs. A test checks every bundled Dockerfile
    against this. Dockerfiles are built without a build context, so they can't
    `COPY` local files.
  - **Adding a harness:** add `images/base-<harness>.Dockerfile` (standalone,
    ending as `USER rnes` with the agent as `ENTRYPOINT`), then register the
    harness in `internal/harness` as a `sandboxProfile`. The profile supplies
    the image/base name, whether it needs a llama-server, any extra policy, its
    argument list, and — for an agent that needs a config file — how to render
    it and where it lives (opencode's is mounted at a fixed path, vibe's is
    written into the staged home); the rest (proxy, credentials, skills,
    `.rnes` policy protection) is shared.
- **Parallel sessions:** each session gets a uniquely named container, and
  any number can share one host llama-server (same lifecycle rules as above).
- **Network exposure:** `rnes` never listens on all interfaces by default. With
  the default `-host`, llama-server listens on `127.0.0.1` on macOS and Docker
  Desktop (which forward `host.docker.internal` to the host's loopback), and on
  the default Docker bridge's gateway IP (usually `172.17.0.1`) with a native
  Linux engine. That address isn't reachable from your network, and the
  proxy container (the only one with a route to the host — see Egress below)
  is pointed at the same IP. An explicit `-host` is used as is (a wildcard
  like `0.0.0.0` prints a warning).
  - On Linux, an already-running server bound to `127.0.0.1` isn't reachable
    from the proxy container, so `rnes` stops with an error instead of reusing
    it.
- **Egress:** the sandbox container has no direct network access. Instead,
  each session gets its own proxy container on a private network between the
  two, and all of the sandbox's HTTP(S) traffic is forced through it via
  `HTTP_PROXY`/`HTTPS_PROXY`. By default, the proxy only allows the session's
  model server plus `registry.npmjs.org`, `proxy.golang.org`, `sum.golang.org`,
  `pypi.org`, `files.pythonhosted.org`, `crates.io`, `static.crates.io`,
  `github.com`, `api.github.com`, `models.dev`, `models.opencode.ai` and
  `opencode.ai` (all over https). A cloud model provider's host is added when
  its key is set (see below). Everything else is blocked, including non-HTTP
  protocols such as git over ssh. Those built-in defaults live in
  `internal/harness/policy.yaml`.
  - *Per-project policy (`.rnes/policy.yaml`):* run `rnes -generate-policy` in
    a project to write a starter `.rnes/policy.yaml` (it refuses to overwrite
    an existing file). When `.rnes/policy.yaml` is present, `-sandbox` uses it
    instead of the built-in defaults (still adding the session's llama-server
    for model-server harnesses, `openrouter.ai:443` when
    `RNES_OPENROUTER_API_KEY` is set, `api.mistral.ai:443` when
    `RNES_MISTRAL_API_KEY` is set, and a cloud harness's own endpoints), then
    prints the active allow-list at startup, marking entries that skip TLS
    interception with `(no-inspect)` and per-entry private ones with
    `(private)`. An empty `.rnes/policy.yaml` restricts the sandbox to those
    automatic entries alone. See
    [`docs/policy.example.yaml`](docs/policy.example.yaml) for a complete
    skeleton. The schema is:

    ```yaml
    policy: v1
    mode: allowlist

    hosts:
      # A plain entry: port 443 only, public addresses only.
      "github.com:443": {}

      # Every port on one host.
      "example.com:*": {}

      # A service on this machine or the LAN.
      "host.docker.internal:5432":
        private: true

      # HTTP/2, gRPC, WebSockets or certificate pinning: tunnel TLS unread.
      "grpc.example.com:443":
        no-inspect: true

      # Generic authentication: the HTTP header the proxy fills in, the
      # environment variable that supplies the secret, and optionally how the
      # secret is written ({value} stands for it). key/value must be set
      # together and the entry must stay inspected.
      "api.example.com:443":
        key: Authorization
        value: EXAMPLE_TOKEN
        format: Bearer {value}
    ```

    `hosts` is an allow-list: every key is a `host:port` (quote it when in
    doubt). The port may be `*` to cover every port on that host; a wildcard
    host is deliberately not allowed (use `-allow-public` on the command line
    instead). An exact `host:port` entry takes precedence over a matching
    `"host:*"` entry, including the entries rnes adds for the session's model
    server and configured providers; those exact entries keep their own marks.
    `private: true` lets that one endpoint resolve to RFC 1918 / IPv6 ULA as
    well as a public address; use `-allow-private` to reach any private address
    outside the list. `no-inspect: true` tunnels that endpoint's TLS untouched
    instead of decrypting it (needed for HTTP/2, gRPC, WebSockets or
    certificate-pinned endpoints). The two marks can be combined. `key`/`value`
    enable generic credential injection: `key` is the HTTP header the proxy
    fills in, `value` names the host environment variable holding the secret,
    and the optional `format` says how it is written (`Bearer {value}`, for
    example; `{value}` is replaced by the secret). Without `format` the header
    value is the secret alone. The file never contains the secret itself; the
    proxy overwrites that header only when forwarding to the listed host.
    Treat `.rnes/policy.yaml` as trusted input: a project-provided file can name
    any environment variable, so review it before running in a repository you
    don't trust. The captured `RNES_*` session credentials are the exception:
    each one can only be injected at the hosts rnes binds it to
    (`github.com`/`api.github.com`, `openrouter.ai`, `api.mistral.ai`), and a
    policy entry naming one for another host is rejected. Every `-sandbox` run
    shadows `<cwd>/.rnes` with a read-only tmpfs at `/workspace/.rnes`
    (rejecting symlinks) and, when the file exists, mounts
    `<cwd>/.rnes/policy.yaml` read-only inside it, so an agent inside the
    container can never create, edit or remove `.rnes/policy.yaml`. Sandbox
    runs do not create `.rnes`; `-generate-policy` is the way to add one. The
    tmpfs is deliberate: a nested bind mount of the host directory made Docker
    Desktop for Mac leave a `deny delete` ACL on it after an interrupted
    session, which then made `rm -rf` and `git worktree remove` fail. Without
    `-sandbox`, an existing `.rnes/policy.yaml` is ignored with a note. A
    legacy `.rnes/policy.txt` is rejected with a conversion error rather than
    silently ignored: move each line under `hosts:` as `"host:port": {}` (and
    turn old `private` / `no-inspect` prefixes into the fields above).
  - `-allow-public` opens the sandbox up to any public host over http(s)
    instead of just the allow-list. Every destination other than llama-server —
    whether listed in `policy.yaml` or opened by `-allow-public` — must still
    resolve to a public unicast IP when dialed, keeping your machine,
    `169.254.169.254` (cloud metadata), your LAN and VPN / CGNAT ranges blocked
    even against DNS rebinding.
  - `-allow-private` instead opens the sandbox up to private addresses outside
    the allow-list — RFC 1918 (`10/8`, `172.16/12`, `192.168/16`) and IPv6
    unique-local (`fc00::/7`) — whether reached by name or IP, including the
    host and the LAN. Hosts already on the allow-list keep their public-only
    address check, so an internal host is reachable through `-allow-private`
    when it is not listed in `.rnes/policy.yaml` (the session's model server is
    the exception: it is marked private automatically). To open one listed
    endpoint instead, give it `private: true` in `.rnes/policy.yaml`.
    Public egress stays limited to the allow-list. Loopback, link-local
    (including cloud metadata `169.254.169.254`), CGNAT / Tailscale and
    multicast stay blocked. Combine with `-allow-public` when a session needs
    public and private egress everywhere.
  - A blocked host shows up to the agent as a 403 response from `rnes-proxy`,
    and when the session ends `rnes` prints a summary of any blocked
    `host:port` attempts from the proxy log.
  - The proxy container itself reaches the host over the default Docker bridge,
    same as llama-server's own listen address above, so on Linux a host
    firewall (e.g. `ufw`) may still need to allow bridge-to-host traffic, and
    other containers on that bridge can still reach llama-server directly. The
    first `-sandbox` run also builds the proxy image (pulling the `golang` and
    `alpine` base images); both the network and the proxy container are torn
    down when the session ends.
- **TLS interception & audit log:** `$TMPDIR/rnes-proxy-<id>.log` (printed at
  startup and kept after the session) records every proxied or denied request.
  By default the proxy decrypts HTTPS to the hosts on its allow-list so it can
  log each request with the method, host, path, status and duration — and never
  the query string, headers or bodies. Plain `http://` forwards are logged the
  same way; blind `CONNECT` tunnels (such as hosts opened only by
  `-allow-public` or `-allow-private`, which stay tunnelled with their real
  certificates) log `CONNECT <host:port> -> tunnelled`; blocked attempts log
  `denied <host:port>`. `-no-inspect` turns TLS
  decryption off while keeping host-level `CONNECT`, `http://` and `denied`
  logging.
  - *The CA:* the proxy creates a certificate authority for each session. Its
    private key lives only in the proxy's memory: it is never written to disk
    and never reaches the sandbox. The CA is restricted (an x509 name
    constraint) to the allow-listed host names, and valid for 30 days.
  - *Trust:* the proxy writes a bundle (the system roots plus the session CA's
    certificate) to a per-session docker volume, which the sandbox mounts
    read-only at `/etc/rnes-ca`. `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
    `GIT_SSL_CAINFO`, `CURL_CA_BUNDLE` and `REQUESTS_CA_BUNDLE` point at it, so
    the agents, git, curl and most other tools trust the proxy. The volume is
    removed with the network when the session ends. Nothing about the CA is
    baked into an image.
  - *Upstream:* the proxy still verifies the real server's certificate itself.
  - *Limits:* HTTP/1.1 only, and WebSocket (`Upgrade`) requests are refused
    with a 501. A tool that ignores the variables above, or pins certificates,
    fails the TLS handshake; the reason is in the proxy log
    (`TLS handshake for <host> failed`).
- **GitHub credentials:** with `RNES_GITHUB_TOKEN` set, the proxy injects the
  token into requests to `github.com` (git over https) and `api.github.com`
  (`gh`), so the agent can clone private repos, push and open PRs without the
  token ever being inside the sandbox. GitHub stays a dedicated placeholder
  provider because `git` sends its credential as an HTTP Basic password, which
  a header template can't express; the plain-Bearer cloud providers are policy
  key/value entries instead.
  - *What the sandbox gets:* `GH_TOKEN` set to a random per-session placeholder
    and a git credential helper that answers with it. The proxy replaces the
    placeholder in the `Authorization` header (Bearer, token or Basic) and only
    for those two hosts. That is the only place the token is ever written: a
    placeholder anywhere else (path, query, other headers, bodies), or sent to
    any other host, is left as it is, so the upstream rejects it. A placeholder
    is worthless outside the session's proxy.
  - *How the token reaches the proxy:* `rnes` reads `RNES_GITHUB_TOKEN`, unsets it,
    and sends it to the proxy container on its stdin: never as an argument or
    environment variable (those show in `docker inspect`), never on disk. The
    proxy keeps it in memory only.
  - *Scope:* the token is the only limit on what the agent can do with it; the
    proxy doesn't restrict reads or writes. Use a fine-grained token scoped to
    what the agent needs. `export RNES_GITHUB_TOKEN=$(gh auth token)` reuses your
    `gh` login, whose scopes are broad.
  - Requires `-sandbox` (ignored with a note otherwise) and TLS interception:
    `rnes` refuses `-no-inspect` while the variable is set. If a custom
    `.rnes/policy.yaml` omits either host, requests to it stay blocked by the
    proxy.
- **OpenRouter models:** with `RNES_OPENROUTER_API_KEY` set, the sandbox can use
  OpenRouter models alongside the local llama-server, without the key ever being
  inside it. Local llama stays the default (`-model` is unchanged); switch to
  an OpenRouter model inside opencode, which ships `openrouter` as a built-in
  provider.
  - *Mechanism:* a policy key/value entry, not a special provider. With the key
    set, rnes adds `openrouter.ai:443` to the session policy with
    `key: Authorization`, `value: RNES_OPENROUTER_API_KEY` and
    `format: Bearer {value}`. `rnes` reads
    and unsets the variable, sends the key to the proxy on stdin, and gives the
    sandbox only `OPENROUTER_API_KEY=<placeholder>`. The proxy sets
    `Authorization: Bearer <key>` on requests to that host, so injection works
    even if a client sends no header at all. Streaming completions pass through.
    `rnes` prints which credentials are active at startup.
  - *Curating models:* add `providers.openrouter.models` to your host
    `opencode.jsonc`; the sandbox config starts from the v2 `providers` block
    (the legacy `provider` block is inert in opencode v2). Keep keys out of
    that file: `rnes` always removes every provider's literal `apiKey` from
    the sandbox's copy (with a warning), so a key that lives only in the
    host's global config never reaches the container. That strip covers only
    the literal `apiKey` field of the global config's copy: secrets in other
    config locations (`providers.<id>.headers`/`.body`, per-model
    `headers`/`.body`, MCP `environment`/`headers`) pass through untouched,
    and so do project-level `opencode.json(c)`/`.opencode/` configs in the
    workspace, which are the project's own files and ride in the mount as-is.
    With `RNES_OPENROUTER_API_KEY` set, the proxy injects the key instead.
  - *Scope:* the key is protected from theft, not from use: an agent in the
    sandbox can make OpenRouter calls through the proxy. **Use a dedicated key
    with a low credit limit.**
  - *Privacy:* prompts and code context leave your machine whenever an
    OpenRouter model is used. The proxy log shows requests by host.
  - Requires `-sandbox` (ignored with a note otherwise) and TLS interception:
    `rnes` refuses `-no-inspect` while the variable is set.
- **Mistral models:** with `RNES_MISTRAL_API_KEY` set, the sandbox can use
  Mistral models alongside the local llama-server, by the same mechanism as
  OpenRouter models above: rnes adds `api.mistral.ai:443` to the session policy
  with a `Bearer {value}` entry for `RNES_MISTRAL_API_KEY`, the sandbox only gets
  `MISTRAL_API_KEY=<placeholder>`, and the proxy fills in the `Authorization`
  header at that host. To reuse the key opencode stores (same
  `opencode auth login` sign-in as on the host):

  ```sh
  export RNES_MISTRAL_API_KEY=$(cat ~/.local/share/opencode/auth.json | jq -c -r '.mistral.key')
  ```

  A Vibe subscription key also works: on macOS after browser sign-in it sits
  in the keychain (service `ai.mistral.vibe`, account `MISTRAL_API_KEY`); on
  Linux, copy the key from
  `https://chat.mistral.ai/code/extensions` (Advanced section). With
  `-sandbox -harness vibe`, rnes reads that keychain item itself when
  `RNES_MISTRAL_API_KEY` is unset, so a signed-in user needs no export: it
  prints where the key came from, and `-no-keychain` turns the lookup off.
  Only Vibe gets this — opencode has its own sign-in and its own key, and agy
  has no Mistral provider. The same scope, privacy and `-no-inspect` caveats
  as OpenRouter apply; a subscription key is shared with your Vibe usage, so
  prefer a dedicated console.mistral.ai key for agents.
- Session data inside the container is discarded on exit (`--rm`), except a
  harness's own persisted auth volume (`agy`; see *Antigravity* below).

#### Skills

The sandbox has no access to your home directory, so none of your host
skills (`<name>/SKILL.md` directories) are visible in it by default. `-skill`
attaches the ones you choose; without it nothing is attached and no skills
folder is even read.

```sh
rnes -sandbox -skill git-release                 # one skill, by name
rnes -sandbox -skill git-release -skill review   # several
rnes -sandbox -skill ~/work/skills/review        # a path to one skill directory
rnes -sandbox -skill all                         # everything found
rnes -sandbox -skills-dir ~/team-skills -skill deploy
```

- **Values:** a `-skill` value containing a `/` or starting with `~` is a path
  to exactly that skill directory (relative paths are relative to the current
  directory). `all` attaches every skill found. Anything else is a skill name
  to look up, which must be a valid opencode skill name (lowercase letters and
  digits separated by single hyphens, at most 64 characters); a skill
  literally named `all` can only be attached by path.
- **Sources:** names and `all` are looked up, in this order, in each
  `-skills-dir` (in the order given), then the active harness's own global
  skills folder, then the other harnesses' folders in this order:
  `~/.config/opencode/skills`, `~/.gemini/config/skills`, `~/.vibe/skills`,
  `~/.agents/skills`, `~/.claude/skills`. So under `-harness agy`,
  `~/.gemini/config/skills` comes first and wins duplicate names for `all`;
  under `vibe`, `~/.vibe/skills` does; under `opencode`, its own folder does.
  Each folder's immediate subdirectories are its skills. Default folders that don't exist are skipped;
  a `-skills-dir` that doesn't exist is an error. `-skills-dir` on its own
  attaches nothing.
- **Conflicts:**
  - A name found in more than one folder is an error listing every candidate,
    and the session doesn't start: pick one with its path. (The same directory
    reached twice, e.g. through a symlink, counts once.)
  - `all` never fails on duplicates: the first folder wins, and each shadowed
    skill gets a warning naming both paths. A skill also given explicitly (by
    name or path) wins over `all`'s pick.
  - Asking for the same skill twice, or by name and through `all`, attaches it
    once. Two *different* directories requested explicitly under one name is
    an error.
  - An unknown name is an error naming the folders searched.
  - If the project already has a skill of the same name in `.opencode/skills`,
    `.opencode/skill`, `.agent/skills`, `.vibe/skills`, `.claude/skills` or
    `.agents/skills`, `rnes` warns; which one the active harness uses is up to
    it.
- **Validation:** a skill is a directory with a `SKILL.md` (a regular file
  inside it), whose folder name is a valid skill name. An explicitly requested
  skill that isn't valid is an error; under `all`, invalid entries are skipped
  with a warning (plain files and hidden entries such as `.git` are ignored).
- **In the sandbox:** each attached skill's directory is bind-mounted
  read-only where the active harness finds global skills:
  `~/.config/opencode/skills/<name>` for opencode,
  `~/.gemini/config/skills/<name>` for agy (a sibling of its auth volume,
  never inside it), and `~/.vibe/skills/<name>` for vibe (in the same staged
  home as its generated config). They are live (edits on the host show up in
  the session),
  and nothing else from your skills folders or home directory is visible. A
  symlink inside a skill that points outside its directory is dangling in the
  container. So that docker doesn't create the mount point's parents
  root-owned, the harness's skills parent (`~/.config` or `~/.gemini/config`)
  is a private, per-session directory under `$TMPDIR` that rnes removes when
  the session ends; anything an image puts there itself is hidden while
  skills are attached or skill commands are generated (the bundled images put
  nothing there). A session with neither keeps the image's own directory.
- **Skill commands:** so a skill can be invoked explicitly, like Claude's
  `/skill`, rnes generates a `/<name>` command per skill (opencode only; `agy`
  reads attached skills from its own skills folder directly and gets no
  synthetic commands) — on the host, for
  the project's own skills (the working directory and each parent up to the
  repository root) and the global ones; in the sandbox, for the attached
  skills and the project's (the working directory only, which is all the
  container mounts). The command loads the skill and follows it; arguments
  are appended to the prompt. Discovery reads the `skills` folders and
  opencode's legacy `skill` ones, and, being automatic, skips anything that
  isn't a valid skill folder without a word (`-skill all` reports those). It
  is narrower than opencode's: flat `skills/<name>.md` files, `SKILL.md`
  files nested deeper, and folder names that aren't valid skill names get no
  command, though opencode may still load them. On the host the commands
  travel in opencode's `OPENCODE_CONFIG_CONTENT` environment variable, merged
  over your own config, so nothing is ever written to your `~/.config` — and
  because a running opencode service only reads that variable at startup, a
  session with commands runs opencode against its own server (`--standalone`)
  instead of the shared background one. If you already export
  `OPENCODE_CONFIG_CONTENT`, rnes merges into it and your settings and
  commands win; if it isn't a plain JSON object (comments, say), rnes leaves
  it alone and generates no commands. In the sandbox the commands are files in
  the session's staged `~/.config/opencode/commands`, next to the mounted
  skills. A name opencode already has a command for is skipped with a
  warning, so the existing command keeps working: a built-in one (`/models`,
  `/undo`, …), one the project defines (`.opencode/commands/<name>.md`, a
  config `commands` entry, or the same in an ancestor directory, since
  opencode discovers those), or — on the host — one of your global commands
  (in `OPENCODE_CONFIG_DIR` when that is set) or in your own
  `OPENCODE_CONFIG_CONTENT`. The skill itself is unaffected: the agent can
  still load it by name.
- **Output:** `rnes` prints one line listing the attached skills and the folder
  each came from; warnings go to stderr as `rnes: warning: …`. More than 50
  skills (one mount each) prints a warning, as does `-skill all` finding
  nothing, and a skill that lives inside the project: the project is writable
  in the sandbox, so the agent can change that skill despite its read-only
  mount.
- **Security:** a skill's content is untrusted input to the agent, like any
  project file, and it reaches the model: every attached skill's name and
  description are in the agent's prompt, and its files whenever the agent reads
  them. With an OpenRouter or Mistral model, that means they leave your
  machine. Attaching
  skills adds no credentials or environment variables. Only attach skills you
  trust and are happy to send to the model.
- Like the other sandbox flags, `-skill` and `-skills-dir` require `-sandbox`,
  and `-sandbox-update` ignores them with a note.

### Antigravity (`-harness agy`)

`rnes -sandbox -harness agy` runs the Google Antigravity CLI inside the same
sandbox, using your Google subscription instead of a local model. It requires
`-sandbox` (there is no host mode) and never starts or contacts
`llama-server`:

- `-host` and `-port` are rejected.
- `-model` is forwarded as `agy --model` only when passed explicitly;
  otherwise agy keeps its own model choice.
- `RNES_OPENROUTER_API_KEY` and `RNES_MISTRAL_API_KEY` are ignored with a
  note (those providers are opencode features).
- Arguments after `--` are forwarded to `agy` (e.g.
  `rnes -sandbox -harness agy -- --version`).

The `agy` binary is installed in `images/base-agy.Dockerfile` and runs as your
uid/gid, like the rest of the sandbox.

**Auth and persistence.** Your host `~/.gemini` is never mounted into the
sandbox. Instead, each project gets its own Docker named volume,
`rnes-auth-agy-<sha256(realpath(cwd))[:12]>` (label `rnes.auth=agy`), mounted
read-write at agy's two credential locations, `/home/rnes/.gemini/antigravity-cli`
(`antigravity-oauth-token`, `settings.json`, `brain/` transcripts) and
`/home/rnes/.config/antigravity` (`credential-user-scope`). It is kept across
runs:

- **First session:** agy asks you to sign in. Choose *Google OAuth*, open the
  URL it prints in a browser on this machine, sign in, and paste the
  authorization code back into agy before its timeout. The login is stored in
  the volume, so later sessions in the same project start signed in — no
  browser round-trip again. (agy's redirect goes to
  `antigravity.google/oauth-callback` and you paste the code, so the flow
  works even though the container has no browser.)
- Different projects never see each other's session state or transcripts.
- `rnes` prints the volume name at startup.
- List them with `docker volume ls --filter label=rnes.auth`.
- Remove one with `docker volume rm rnes-auth-agy-<hash>`; this signs that
  project out and deletes its transcripts.

**Security trade-off.** Unlike the GitHub and cloud-provider keys (OpenRouter,
Mistral), agy's
live OAuth session is inside the sandbox, because the agent has to use it
directly. Anything that escapes the sandbox can use that session until it
expires or you remove the volume. The sandbox still drops all Linux
capabilities, enables `no-new-privileges`, has no direct host route apart from
the proxy, and mounts only the project plus a read-only `/workspace/.rnes` and
attached skills. `-allow-public` and `-allow-private` are especially risky here
— a live OAuth session plus arbitrary egress — so rnes prints an explicit
warning when they are combined.

**Network.** agy's Google OAuth and Antigravity endpoints are added to the
session's allow-list as `no-inspect` entries, so their HTTP/2 and gRPC traffic
is tunnelled untouched instead of decrypted (some use certificate pinning).
Hosts such as `github.com` stay TLS-intercepted, audited and
credential-injected. A custom `.rnes/policy.yaml` still applies; agy's own
endpoints are added on top of it. If that leaves no host to intercept, the
proxy writes the system roots alone to the sandbox's trust bundle, so real
upstream certificates still validate.

### Vibe (`-harness vibe`)

`rnes -harness vibe` runs the [Mistral Vibe CLI](https://github.com/mistralai/mistral-vibe)
against the local model, and `rnes -sandbox -harness vibe` runs it inside the
same sandbox as the other harnesses. Both need `llama-server`; `-host` and
`-port` apply as usual.

**On the host** rnes merges a provider block into your own
`$VIBE_HOME/config.toml` (`~/.vibe/config.toml` by default), between two
markers it owns:

```toml
# >>> rnes: local llama-server (managed by rnes; edits here are overwritten)
[[providers]]
name = "rnes-llamacpp"
api_base = "http://127.0.0.1:8080/v1"
api_key_env_var = ""

[[models]]
name = "unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M"
provider = "rnes-llamacpp"
alias = "unsloth/Qwen3.6-35B-A3B-GGUF:Q4_K_M"

# <<< rnes
```

Everything outside the block is preserved, including comments; rnes keeps the
block last in the file and normalises the blank lines around it. Your
`active_model` is never rewritten: the session's model is pinned through
`VIBE_ACTIVE_MODEL`, which Vibe's environment layer ranks above `config.toml`,
so nothing outlives the session. (Vibe's `--model` is headless-only, so it
can't do this in the interactive TUI.) The provider is named `rnes-llamacpp`
rather than `llamacpp` so it never shadows a local provider of your own.

rnes refuses to touch a `config.toml` it can't parse, rather than appending to
a file Vibe itself would reject, and it writes through a symlink instead of
replacing it, so a dotfiles-managed config stays managed.

**In the sandbox** rnes generates the whole `config.toml` instead, from
scratch, and puts it in a staged `~/.vibe` that is removed when the session
ends:

- The session starts on the model rnes serves (`-model`), and every cached
  model is selectable.
- Nothing is copied from your own `config.toml`. Vibe's config can hold
  literal secrets (an MCP server's static header, a provider's
  `extra_headers`), and none of them may reach the container. The trade-off is
  that the providers, models and MCP servers in your own `config.toml` are not
  available in a vibe sandbox session. A project `.vibe/config.toml` in the
  workspace still loads (it is a workspace file the agent can read anyway), so
  a repository can carry its own providers and MCP servers.
- `enable_telemetry`, `enable_update_checks`, `enable_auto_update`,
  `enable_connectors` and `[experiments] enable` are off: the sandbox's egress
  is allow-listed and audited, and none of them is needed to do the work.
  Connectors matter most: with `RNES_MISTRAL_API_KEY` set, they would otherwise
  hand the agent your own Mistral connectors (Drive, GitHub, Linear, Slack).
  Vibe merges this file over its built-in defaults and a project
  `.vibe/config.toml` can still override these switches, so the proxy, not the
  config, is what actually bounds the session.
- Vibe is started with `--trust`, so it doesn't stop at its trust prompt for
  the mounted workspace (the staged home has no trust database). The project's
  own `.vibe/config.toml` and `.vibe/hooks.toml` then load, inside the sandbox,
  and its hooks run without an approval prompt: only open a vibe sandbox
  session in a repository whose hooks you trust.
- With `RNES_MISTRAL_API_KEY` set — or, on macOS, read from the Keychain
  automatically when Vibe is signed in (see *Mistral models*) — the proxy
  injects the key at `api.mistral.ai` and the container only ever holds a
  placeholder, which Vibe's built-in Mistral provider reads from
  `MISTRAL_API_KEY`. Mistral models are then selectable with `/model`, and
  prompts leave your machine when you pick one.
- Signing in to Mistral from inside the sandbox is not supported. Vibe's
  browser sign-in is poll-based, not a paste-the-code flow: it opens
  `console.mistral.ai` in a browser, then polls that host until the sign-in
  completes. The container has no browser to open and `console.mistral.ai` is
  not in the allow-list, so the flow stops at its first step. Use
  `RNES_MISTRAL_API_KEY` (or the Keychain pickup) instead.
- With a Mistral key present, Vibe also calls its console host
  (`chat.mistral.ai`) for account and usage data. The proxy blocks it, so the
  session ends with a `proxy blocked: chat.mistral.ai:443` line; nothing in the
  session depends on it.
- Nothing persists between sessions: Vibe's logs, history and any sign-in done
  inside the sandbox live in the staged home and are deleted with it. There is
  no auth volume, unlike `agy`.

Attached skills (`-skill`) are mounted read-only at `~/.vibe/skills/<name>`,
and `~/.vibe/skills` is searched first for `-skill` names under this harness.
Arguments after `--` are forwarded to `vibe` (e.g.
`rnes -sandbox -harness vibe -- --continue`).

## Notes

- llama-server's own logs are written to
  `$TMPDIR/rnes-llama-server-<port>.log` rather than to the terminal, since
  the agent's full-screen UI shares the same tty and raw log output would
  corrupt it. The path is printed on startup.
- Three harnesses are implemented today: `opencode` (host or sandbox),
  `vibe` (host or sandbox) and `agy` (sandbox only). Adding another means
  registering a `sandboxProfile` in `internal/harness` (or implementing the
  small `Harness` interface for a host-mode agent) and adding its
  `images/base-<name>.Dockerfile`.
