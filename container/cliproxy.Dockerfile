# CLIProxyAPI sidecar — LOCAL FORK of router-for-me/CLIProxyAPI @ v7.2.48 (commit 956ce7c).
#
# Patch (container/cliproxy-redacted-thinking.patch) fixes redacted_thinking round-tripping on
# the Claude adaptive-thinking + tool-use path. Upstream's Responses<->Claude translator has no
# case for `redacted_thinking` blocks, so it silently drops them; on the next tool-loop turn the
# replayed assistant message is missing a block Anthropic requires verbatim, and the request
# fails with: "`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be
# modified." The patch forwards redacted_thinking `data` through the reasoning item's
# encrypted_content (sentinel-tagged) and reconstructs the block on the way back.
#
# Build: docker build -t ashmaize-cliproxy -f container/cliproxy.Dockerfile .
FROM golang:1.26-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN git clone --depth 1 --branch v7.2.48 https://github.com/router-for-me/CLIProxyAPI.git .
COPY container/cliproxy-redacted-thinking.patch /tmp/redacted-thinking.patch
RUN git apply -v /tmp/redacted-thinking.patch
RUN go mod download
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*
RUN mkdir /CLIProxyAPI
COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
WORKDIR /CLIProxyAPI
EXPOSE 8317
CMD ["./CLIProxyAPI"]
