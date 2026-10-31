# Internal base of the agy sandbox images (not selectable with -image): agy
# plus git, running as a non-root user. rnes starts it with --user <host
# uid>:<host gid> so files written to the mounted working directory keep the
# host user's ownership; HOME and the two directories rnes persists agy's
# credentials in (~/.gemini/antigravity-cli and ~/.config/antigravity, copied
# into a fresh per-project auth volume on first mount) are therefore
# world-writable rather than owned by a specific uid.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl tar \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL https://antigravity.google/cli/install.sh | AGY_CLI_DISABLE_AUTO_UPDATE=true bash -s -- --dir /usr/local/bin \
 && chmod 0755 /usr/local/bin/agy \
 && rm -rf /root/.cache /root/.gemini /root/.local

RUN useradd --create-home --home-dir /home/rnes --shell /bin/bash rnes \
 && mkdir -p /home/rnes/.gemini/antigravity-cli /home/rnes/.config/antigravity \
 && chmod 0777 /home/rnes /home/rnes/.gemini /home/rnes/.gemini/antigravity-cli \
 && chmod 0777 /home/rnes/.config /home/rnes/.config/antigravity

ENV HOME=/home/rnes
# Image updates go through rnes -sandbox-update / -build, and /usr/local/bin/agy
# is root-owned anyway.
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
USER rnes
WORKDIR /workspace
ENTRYPOINT ["agy"]
