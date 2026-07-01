# Offline task container for the AshMaize Eval.
#
# The agent works INSIDE this image with NO network. That is the contamination defense:
# AshMaize is findable online (the upstream repo and ports), so the only robust way to
# test "implement from spec" is to deny retrieval. The upstream source is NOT in the image;
# only the prebuilt `oracle` binary (a black box) and `spec/` are.
#
# Build:  docker build -t ashmaize-eval -f container/Dockerfile .
# Run:    docker run --rm --network none -it ashmaize-eval
#
# `docker build` has network (to fetch crates + apt packages); `docker run --network none`
# does not. All toolchain/oracle installation therefore happens at build time.

# ---- Stage 1: build the reference oracle from the vendored AshMaize crate. ----
FROM rust:1-bookworm AS oracle-build
WORKDIR /build
# Only the oracle crate + vendored source (see .dockerignore: target/ and the symlink are skipped).
COPY oracle/ /build/oracle/
WORKDIR /build/oracle
RUN cargo build --release && install -m 0755 target/release/oracle /oracle-bin

# ---- Stage 2: shared offline runtime without the oracle. ----
FROM debian:bookworm-slim AS runtime-base

# Toolchains a senior engineer might reasonably choose. No network is used at runtime.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git bubblewrap build-essential pkg-config \
      python3 python3-pip \
      golang-go \
      rustc cargo \
    && chmod u+s /usr/bin/bwrap \
    && rm -rf /var/lib/apt/lists/*

# Codex CLI runs the model-attempt harness inside this runtime image. Auth is
# mounted at runtime via CODEX_HOME; no secrets or eval artifacts enter the image.
RUN mkdir -p /opt/codex \
    && curl -fsSL https://chatgpt.com/codex/install.sh \
      | CODEX_NON_INTERACTIVE=1 CODEX_INSTALL_DIR=/usr/local/bin CODEX_HOME=/opt/codex sh \
    && codex --version

WORKDIR /task
# The agent sees ONLY the spec. Not the oracle binary, oracle source, scenarios, or grader.
COPY spec/ /task/spec/
ENV ASHMAIZE_TASK=/task/spec/TASK.md

# ---- Stage 3a: generation image. No oracle exists in this target. ----
FROM runtime-base AS agent
CMD ["/bin/bash"]

# ---- Stage 3b: grading image. The oracle exists only in this target. ----
FROM runtime-base AS grader
COPY --from=oracle-build /oracle-bin /usr/local/bin/oracle

# Enforce offline at runtime via `--network none`. Verify grader with
# container/check_offline.sh and verify agent has no /usr/local/bin/oracle.
CMD ["/bin/bash"]
