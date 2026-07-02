# AshMaize Eval

An RL **environment / eval** inspired by [GBA Eval](https://gbaeval.com)

A frontier coding agent is given a written spec and must implement the **AshMaize** memory-hard proof-of-work VM from
scratch. A deterministic grader runs a fixed corpus of inputs through the agent's implementation
**and a reference oracle**, and scores them by **exact output match**.

[Understand the AshMaize algo](/docs/ashmaize-explained.md)

> **Attribution** AshMaize is **Input Output's (IOHK)** algorithm
> ([`input-output-hk/ce-ashmaize`](https://github.com/input-output-hk/ce-ashmaize))

## Why this is a good benchmark

- **Low contamination.** AshMaize is obscure, so models can't reproduce it from memory, it must
  follow the spec. The agent runs in an offline container, so it can't retrieve the upstream source.
- **Deterministic.** No entropy: same inputs → same output, so grading is exact and reproducible.
- **Multiple sources of difficulty.** Argon2-`hprime`-seeded ROM generation, per-loop program
  regeneration, exact op semantics (`MulH`, `ISqrt`, rotates, the `Mod`/div-by-zero), and
  memory-hard exact-match.

## Layout

```
spec/        ASHMAIZE.md (the algorithm) · ABI.md (the JSON-over-stdio interface) · TASK.md (agent prompt)
oracle/      reference oracle: src/main.rs (ABI wrapper) + vendor/ce-ashmaize (pinned upstream fork)
grader/      Go grader: runs candidate vs oracle over the corpus, exact-match, weighted score
scenarios/   replay/ procedural/ adversarial/ — the 100-case graded corpus (JSON)
examples/    mock_impl.py — a PLACEHOLDER impl (validates the ABI, fake outputs) to smoke-test wiring
container/   offline Dockerfile (no network) + check_offline.sh (affordance verification)
harness/     Go Codex runner + auth helpers; root config.yaml selects harness settings
scorecards/  committed per-model scorecards (runs/ + transcripts/ are gitignored)
index.html   client-side scorecard viewer — leaderboard, compare, and charts (open in a browser)
```

## Build

```bash
# grader
cd grader && go build -o ../bin/grader . && cd ..
# oracle (needs network once, to fetch crates; then runs offline)
cd oracle && cargo build --release && ln -sf target/release/oracle oracle && cd ..
```

## Smoke-test the harness (mock as both sides → ~100%)

The mock is **not** AshMaize; it's a deterministic ABI-faithful stand-in to verify the grader itself.

```bash
./bin/grader \
  -oracle-bin python3 -oracle-arg examples/mock_impl.py \
  -agent-bin  python3 -agent-arg  examples/mock_impl.py \
  -scenarios scenarios -out scorecard.mock.json
```

Confirm the real oracle reproduces itself across the corpus (every case must pass):

```bash
./bin/grader -oracle-bin ./oracle/oracle -agent-bin ./oracle/oracle -scenarios scenarios
```

## Grade a candidate

```bash
./bin/grader \
  -oracle-bin ./oracle/oracle \
  -agent-bin  <candidate-exe> [-agent-arg <arg> …] \
  -scenarios scenarios -out scorecards/<model>.json
```

The grader takes an explicit executable + repeatable args for each side (`-oracle-bin`/`-oracle-arg`,
`-agent-bin`/`-agent-arg`), so paths with spaces need no quoting. Score = weighted average of
`replay` (65%), `procedural` (25%), `adversarial` (10%).

## Offline container

```bash
docker build -t ashmaize-eval-agent -f container/Dockerfile --target agent .
docker build -t ashmaize-eval-grader -f container/Dockerfile --target grader .
container/check_offline.sh ashmaize-eval-grader        # oracle works, spec present, source absent, net denied
docker run --rm --network none -it ashmaize-eval-agent
```

The generation image ships `spec/` and toolchains, but no `/usr/local/bin/oracle`. The grading image
adds the prebuilt oracle binary for score calculation only; the AshMaize source is never in either
runtime image. `docker build` has network (to compile the oracle and install tools);
`docker run --network none` does not.

## Run Codex through the container harness

The Codex attempt runs inside the configured generation image with only configured
`workspace.files` mounted into a temporary workspace. The ignored eval auth home lives at
`.codex-eval/codex-home` and persists only `auth.json`; temporary Codex runtime state is generated
per command and deleted after use. `runs/` and `transcripts/` stay local, while
`scorecards/<slug>.json` is the durable result.

```bash
# build harness
cd harness && go build -o ../bin/run-codex . && cd ..

# auth setup: choose the flow that matches your local Codex credentials
./bin/run-codex auth status -harness codex
printf '%s' "$CODEX_API_KEY" | ./bin/run-codex auth login-api-key -harness codex
./bin/run-codex auth login-device -harness codex
./bin/run-codex auth import-host -harness codex

# prove freeze + grading without Codex
./bin/run-codex run -harness codex -slug mock-codex-fixture -fixture-agent examples/mock_impl.py -overwrite

# real single-attempt run
./bin/run-codex run -harness codex -slug codex-gpt-5-4-mini
```

The root `config.yaml` uses a compact named-harness shape: `default` selects a key under
`harnesses`, `driver` selects the implementation, and generic fields hold the prompt, auth home,
model, and timeouts. The checked-in `codex` harness uses `model: gpt-5.4-mini` with
`cli.reasoning_effort: xhigh`. Available OpenAI Codex models are `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`,
`gpt-5.3-codex`, and `gpt-5.2`; each supports a `cli.reasoning_effort` of `low`, `medium`, `high`,
or `xhigh` (default `medium`). Run `docker run --rm ashmaize-eval-agent codex debug models` to
confirm the live catalog.
`images.agent` is the generation image and must not contain the oracle; `images.grader` is the
grading image and contains `/usr/local/bin/oracle`. Workspace inputs are explicit under
`workspace.files`; each entry copies a repo file to a target path under `/workspace` such as
`/workspace/spec/TASK.md` or `/workspace/AGENTS.md`. Docker-only flags live under `docker.flags`;
Codex CLI settings live under `cli`. The generation container keeps network enabled so Codex can
reach OpenAI, but model-spawned commands remain subject to
`cli.workspace_write.network_access: false` in the generated temporary Codex config.

The candidate's `/workspace/agent.sh` must be relocatable: it is frozen into `runs/<slug>/` and
graded from a different mount point, so it must resolve helper files relative to its own directory
(e.g. `DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)`) rather than hardcoding `/workspace/...`.
The harness rejects any `agent.sh` containing a hardcoded `/workspace/` path before grading.

## Benchmark GLM (Z.AI) through the bridge

Modern Codex only speaks the OpenAI **Responses API**; Z.AI GLM only exposes **Chat Completions**.
The `glm` harness bridges that gap with a translation sidecar
([`@mmmbuto/zai-codex-bridge`](https://github.com/Virtual0ps/zai-codex-bridge), MIT) built from
`container/zai-bridge.Dockerfile`. When a harness has a `provider` block, the runner starts the
bridge on a per-run private Docker network, points Codex at it (`model_provider` + a
`[model_providers.<id>]` block with `wire_api = "responses"`), and tears it down afterward. The
bridge forwards to `provider.bridge.upstream_base_url` using the GLM key.

```bash
# build the bridge image (once)
docker build -t ashmaize-zai-bridge -f container/zai-bridge.Dockerfile .

# provide the GLM Coding Plan key via the env var named by provider.key_env (ZAI_API_KEY)
export ZAI_API_KEY=your-zai-coding-plan-key

# run GLM through the harness
./bin/run-codex run -harness glm -slug glm-5-2-max
```

GLM auth is API-key only, so the `auth` subcommands do not apply to the `glm` harness. The checked-in
`glm` harness uses `model: glm-5.2` with `cli.reasoning_effort: max` (GLM-5.2's deepest mode); other
Coding Plan models include `glm-4.7` and `glm-5-turbo`, and `glm-5.2[1m]` enables the 1M context.
`cli.reasoning_effort` is forwarded to GLM as `reasoning_effort` (GLM-5.2 accepts `max`; thinking is
enabled by default). Only Codex's own API traffic leaves the generation container (now via the
bridge); model-spawned commands stay offline under `cli.workspace_write.network_access: false`.

## Benchmark Claude through CLIProxyAPI

Modern Codex only speaks the OpenAI **Responses API**; Claude Code accounts authenticate over
**OAuth**. The `claude` harness bridges that gap with a managed [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
sidecar built from `container/cliproxy.Dockerfile`, which exposes an OpenAI Responses endpoint and
translates it to Claude using stored Claude Code OAuth tokens. When the `claude` harness runs, the
runner renders `container/cliproxy-config.yaml` (substituting the local bearer key), starts the
sidecar on a per-run private Docker network with the OAuth token directory mounted, points Codex at
it (`model_provider` + a `[model_providers.<id>]` block with `wire_api = "responses"`), waits for
`GET /healthz`, and tears it down afterward.

```bash
docker build -t ashmaize-cliproxy -f container/cliproxy.Dockerfile .
export CLIPROXY_API_KEY="$(openssl rand -hex 24)"
./bin/run-codex auth claude-login -harness claude
./bin/run-codex auth status -harness claude
./bin/run-codex run -harness claude -slug claude-opus-4-5
```

`CLIPROXY_API_KEY` is the **local bearer key** Codex uses to call the private sidecar — it is not an
Anthropic key and never leaves your machine. `auth claude-login` runs the sidecar interactively so you
can open the printed Claude OAuth URL and complete browser login; the resulting Claude OAuth tokens
are stored under the gitignored `.codex-eval/cliproxy-auth` directory (`auth status` reports only the
token-file count, never token contents). The sidecar is attached to a per-run private Docker network,
and the candidate's model-spawned commands still run with `cli.workspace_write.network_access: false`.
The checked-in `claude` harness uses exact model `claude-opus-4-5-20251101` with
`cli.reasoning_effort: high` and `cli.model_supports_reasoning_summaries: true`.

### Claude extended thinking

Codex only sends reasoning parameters for models whose family advertises reasoning support, and it
doesn't recognize provider models, so it defaults that off. The harness sets
`cli.model_supports_reasoning_summaries: true` to force Codex to emit `reasoning.effort`; CLIProxyAPI
maps that effort onto Claude's `thinking` config, so extended thinking runs end to end. This is
verified: an identical request with `reasoning_effort: high` returns Claude `thinking` blocks and
~5x the `output_tokens` of one with reasoning disabled. Valid Codex efforts are `minimal`, `low`,
`medium`, `high`, and `xhigh` (not `max`); `high` maps to a 24576-token Claude thinking budget, which
CLIProxyAPI clamps below the request's `max_tokens`, so higher efforts can starve the response on
this model — `high` is the safe deepest setting here.

Because CLIProxyAPI's Claude->Responses translation does not split reasoning tokens back out, the
scorecard reports `reasoning_tokens: 0` even while thinking is active. The thinking tokens are still
counted inside `output_tokens`, so `total_tokens` and cost stay accurate.

## Scorecard metrics

Each harness run adds a `metrics` block to `scorecards/<slug>.json`, parsed from the Codex
`--json` event stream (`turn.completed` usage is cumulative, so the last one holds run totals) plus
wall-clock timing measured by the runner:

```json
"metrics": {
  "input_tokens": 0,
  "cached_input_tokens": 0,
  "output_tokens": 0,
  "reasoning_tokens": 0,
  "total_tokens": 0,
  "turns": 0,
  "item_count": 0,
  "generation_seconds": 0.0,
  "grading_seconds": 0.0
}
```

`cached_input_tokens` is the prompt-cache subset of `input_tokens`; `reasoning_tokens` is the subset
of `output_tokens`; `total_tokens = input_tokens + output_tokens`. Token fields are omitted for
`-fixture-agent` runs (no model call), and the GLM bridge reports `cached_input_tokens` /
`reasoning_tokens` as `0` unless GLM returns them.

## Scorecard viewer

`index.html` is a single-file, client-side viewer for `scorecards/<slug>.json` — no build step and
no bundled dependencies (Tailwind via CDN), so it runs offline. Three views:

- **Leaderboard** — overall + per-section scores, ranked, with model search/sort and a light/dark toggle.
- **Compare** — two or more models side by side, per-case, with a "disagreements only" filter.
- **Charts** — grouped section-score bars, a quality-vs-cost scatter (overall score vs. total tokens
  on a log scale; models without token metrics are noted as hidden), and a per-case pass/fail matrix
  with a "failures only" filter.

Serve the repo root over HTTP so the viewer can auto-discover `scorecards/*.json` (via directory
listing, falling back to `scorecards/manifest.json`):

```bash
python3 -m http.server 8099
# then open http://localhost:8099/
```

Opening `index.html` directly over `file://` also works, but browsers block directory listing there —
use the **Add** button or drag `.json` scorecards onto the window to load them.