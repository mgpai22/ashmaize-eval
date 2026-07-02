# Responses <-> Chat Completions bridge for running Z.AI GLM models through Codex.
#
# Modern Codex CLI only speaks the OpenAI Responses API; Z.AI GLM only exposes
# Chat Completions. This sidecar (third-party, MIT) translates between them.
#
# Build: docker build -t ashmaize-zai-bridge -f container/zai-bridge.Dockerfile .
# Run:   docker run --rm --network <net> --name zai-bridge \
#          -e ZAI_API_KEY=<glm-key> ashmaize-zai-bridge
#
# The upstream GLM key is read from ZAI_API_KEY (see the tool's pickAuth: env
# takes priority over any forwarded Authorization header). HOST=0.0.0.0 so a
# sibling Codex container can reach it by name on a user-defined Docker network.

FROM node:20-bookworm-slim

RUN npm install -g @mmmbuto/zai-codex-bridge@0.4.9

# Patch the bridge to retry Z.AI 429/5xx/transport errors (see zai-bridge-patch.js)
# instead of surfacing them to Codex as a fatal stream disconnect. Keeps a Codex
# run alive (workspace state preserved) through rate-limit storms. Fails the build
# loudly if the pinned @0.4.9 source drifts.
COPY container/zai-bridge-patch.js /usr/local/share/zai-bridge-patch.js
RUN node /usr/local/share/zai-bridge-patch.js \
 && node --check /usr/local/lib/node_modules/@mmmbuto/zai-codex-bridge/src/server.js

ENV HOST=0.0.0.0 \
    PORT=31415 \
    ZAI_BASE_URL=https://api.z.ai/api/coding/paas/v4 \
    ALLOW_TOOLS=1 \
    ZAI_RETRY_MAX_MS=2400000 \
    ZAI_RETRY_BASE_MS=2000 \
    ZAI_RETRY_CAP_MS=20000

EXPOSE 31415
CMD ["zai-codex-bridge"]
