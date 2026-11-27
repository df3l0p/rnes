# Internal base of the vibe sandbox images (not selectable with -image): the
# Mistral Vibe CLI plus git, running as a non-root user. rnes starts it with
# --user <host uid>:<host gid> so files written to the mounted working
# directory keep the host user's ownership; HOME and ~/.vibe (where Vibe keeps
# its config, logs and global skills) are therefore world-writable rather than
# owned by a specific uid.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

# Vibe is a Python package that needs 3.12+ (bookworm ships 3.11), so uv
# installs it with its own managed interpreter. The tool and that interpreter
# both live under /opt: /root is unreadable to the sandbox user, so a venv
# there would leave the vibe binary unusable. 3.13 is pinned because some of
# Vibe's dependencies (tree-sitter) ship no wheels for newer interpreters and
# would need a compiler to build.
ENV UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin \
    UV_PYTHON_INSTALL_DIR=/opt/uv/python
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh \
 && uv tool install --python 3.13 mistral-vibe \
 && rm -rf /root/.cache

RUN useradd --create-home --home-dir /home/rnes --shell /bin/bash rnes \
 && mkdir -p /home/rnes/.vibe \
 && chmod 0777 /home/rnes /home/rnes/.vibe

ENV HOME=/home/rnes
USER rnes
WORKDIR /workspace
ENTRYPOINT ["vibe"]
