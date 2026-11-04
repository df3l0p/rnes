# The codex sandbox image: the base (codex, git, socat) as-is.
#
# A layer on base: rnes builds base first and passes its tag in as RNES_BASE (the
# value here is only a placeholder that keeps the FROM lint quiet).
ARG RNES_BASE=rnes-sandbox-base-codex
FROM ${RNES_BASE}

USER rnes
