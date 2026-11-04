# Internal base of the codex sandbox images (not selectable with -image):
# codex plus git, running as a non-root user. rnes starts it with --user
# <host uid>:<host gid> so files written to the mounted working directory keep
# the host user's ownership; HOME and ~/.codex (where codex caches its ChatGPT
# OAuth login, auth.json, and reads global skills) are therefore world-writable
# rather than owned by a specific uid.
FROM node:22-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates procps \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @openai/codex \
 && npm cache clean --force

RUN useradd --create-home --home-dir /home/rnes --shell /bin/bash rnes \
 && mkdir -p /home/rnes/.codex \
 && chmod 0777 /home/rnes /home/rnes/.codex

ENV HOME=/home/rnes
# Where the per-project auth volume is mounted and where codex keeps auth.json.
ENV CODEX_HOME=/home/rnes/.codex
USER rnes
WORKDIR /workspace
# codex's OAuth callback server binds the container's loopback, which the
# host's browser can't reach, so sign-in uses the device-code flow
# (codex login --device-auth): no inbound port, and the auth volume keeps the
# login across sessions.
ENTRYPOINT ["codex"]
