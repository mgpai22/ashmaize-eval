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

ENV HOST=0.0.0.0 \
    PORT=31415 \
    ZAI_BASE_URL=https://api.z.ai/api/coding/paas/v4 \
    ALLOW_TOOLS=1

EXPOSE 31415
CMD ["zai-codex-bridge"]
