# Status: `-harness agy` & multi-harness sandbox foundation

This branch implements Direction A from `implementation_plan.md`: running the
Google Antigravity CLI (`agy`) inside `rnes -sandbox` on the user's Google
OAuth subscription, on top of a generalized multi-harness sandbox layer that
`claude`/`codex` can plug into later.

## Completed

- **Proxy selective passthrough:** `no-inspect <host:port>` policy entries
  (accepted in both the built-in policy and `.rnes/policy.txt`) tunnel TLS
  untouched, are excluded from the session CA's name constraints, and are never
  used for credential injection. The proxy also handles an all-`no-inspect`
  policy by writing the system roots alone as the trust bundle.
- **Per-harness images:** `images/base.Dockerfile` was split into
  `base-opencode.Dockerfile` and `base-agy.Dockerfile`; `images.ReadBase` is
  keyed by harness, and `base-*` files are never selectable as `-image`.
- **Skills:** `~/.gemini/config/skills` is a default source, the active
  harness's own global folder comes first for `-skill all`, and `.agent/skills`
  is recognized as a project skills folder.
- **Harness layer:** `ModelServerUser`/`NeedsModelServer`, `AuthVolumer`,
  `Options.ModelSet`/`Options.Args`, `Names`, `PrepareAllHarnesses`, and an
  internal `sandboxProfile` shared by opencode and agy.
- **agy harness:** requires `-sandbox`; never starts `llama-server`; rejects
  explicit `-host`/`-port`; forwards `-model` as `--model` only when given;
  forwards `--` arguments; ignores `RNES_OPENROUTER_API_KEY` with a note.
  OAuth/session state lives in a per-project Docker volume
  `rnes-auth-agy-<sha256(realpath(cwd))[:12]>` labeled `rnes.auth=agy`, mounted
  at `/home/rnes/.gemini/antigravity-cli`. Attached skills are staged read-only
  under the sibling `~/.gemini/config/skills`.
- **CLI:** dynamic `-harness` help, `[-- harness-args...]` usage, startup
  auth-volume notice (plus a warning with `-all-net`), `(no-inspect)` markers in
  the policy startup line, and `-sandbox-update` rebuilding every harness when
  `-harness` is omitted or only the selected one otherwise.
- **Docs/tests:** README covers agy, `--`, the auth volume lifecycle, the
  security trade-off, and `no-inspect`; unit/contract tests were added or
  updated across `main`, `images`, `internal/proxy`, `internal/skills`, and
  `internal/harness`.

## Verification

- `go build ./...` — passes.
- `go vet ./...` — passes.
- `go test -race ./...` — all packages pass.
- End-to-end smoke test:
  `rnes -sandbox -harness agy -build -- --version` → builds `base-agy` +
  `default` + proxy, runs `agy 1.4.10` as `--user <uid>:<gid>`, starts no
  llama-server, and reports no blocked hosts.
- Verified `/usr/local/bin/agy` is root-owned `0755`, the auth mount is `0777`
  and writable by the host uid, and the volume carries `rnes.auth=agy`.

## Notes

- `implementation_plan.md` is intentionally left out of the commit as the
  working plan document.
- The smoke test created this project's `rnes-auth-agy-<hash>` volume. It holds
  no credentials until a real `rnes -sandbox -harness agy` session completes the
  Google OAuth flow; state then persists for this project across runs.
- List volumes with `docker volume ls --filter label=rnes.auth`; remove one
  (signing that project out and deleting its transcripts) with
  `docker volume rm rnes-auth-agy-<hash>`.

## Follow-ups

- Optional: complete an interactive OAuth login once to confirm the live
  Google/Antigravity endpoints end to end.
- Future harnesses: add `images/base-<name>.Dockerfile` and register a
  `sandboxProfile` in `internal/harness`.
