# claude-subscription-openai-wrapper

A tiny, fast OpenAI-compatible API in front of **Claude**, backed by a **Pro/Max subscription** (not per-token API billing). It calls the Anthropic Messages API **directly** over HTTP using a subscription OAuth token — no Claude Code CLI subprocess, no Python, no per-request cold start.

Built for serving [Open WebUI](https://github.com/open-webui/open-webui) an internal endpoint that bills against your Claude subscription.

## Why this exists

The common approach wraps the `claude` CLI as a subprocess (Python + Node), which adds ~2s of process startup to every request, can't stream thinking, and needs permission hacks. This project talks straight to `https://api.anthropic.com/v1/messages`:

- **Single static Go binary**, tens of MiB RAM, instant startup.
- **Native streaming**, including thinking (surfaced as OpenAI `reasoning_content`).
- **Server-side web search** via Anthropic's built-in `web_search` tool (optional).
- **Stateless** — no disk, no credential files; the token comes from an env var.

## How it works

A Claude subscription can be used programmatically via an OAuth token from `claude setup-token` (prefix `sk-ant-oat01-`). Requests authenticate with `Authorization: Bearer <token>`.

The one non-obvious requirement: Anthropic only honors that token for requests that identify as Claude Code. Concretely, **the `system` prompt's first block must be exactly**:

```
You are Claude Code, Anthropic's official CLI for Claude.
```

The wrapper injects that block automatically and appends any system prompt the client sent as a **separate** block, so your own instructions still apply. (Merging your text into the same block is rejected by the API — hence the array.)

```
Open WebUI ──OpenAI /v1/chat/completions──▶ wrapper ──Bearer + spoof──▶ api.anthropic.com/v1/messages
```

## Endpoints

| Method | Path | Notes |
| ------ | ---- | ----- |
| `POST` | `/v1/chat/completions` | OpenAI-compatible; streaming + non-streaming |
| `GET`  | `/v1/models` | static configured list |
| `GET`  | `/health` | liveness; no auth |

Clients must send `Authorization: Bearer <CLIENT_API_KEY>` (except `/health`).

## Reasoning controls (thinking, effort, max)

Anthropic exposes reasoning as a single `thinking.budget_tokens` value; there is no "effort" or "max" concept upstream. Since Open WebUI's OpenAI-connection UI has no thinking/max toggle, the wrapper surfaces reasoning two ways:

1. **Model aliases** in `/v1/models`, so the model dropdown becomes the control. For each model in `THINKING_MODELS` you get three ids:
   - `claude-sonnet-5` — no extended thinking (fast; the default)
   - `claude-sonnet-5-thinking` — extended thinking at the *medium* budget
   - `claude-sonnet-5-max` — thinking at the *high* budget **and** `max_tokens` lifted to `MAX_OUTPUT_TOKENS`
2. **`reasoning_effort`** (`low|medium|high`, also `minimal`) — if the client sends it (Open WebUI's per-model advanced param), it maps to a budget and **overrides** the alias default. `minimal` disables thinking even on a `-thinking`/`-max` alias.

Models **not** in `THINKING_MODELS` (e.g. Fable, whose thinking is silent server-side) are advertised as-is and ignore these signals.

> ⚠️ On the subscription OAuth path, extended thinking is **not streamed as readable deltas** — you get the quality benefit but no visible reasoning panel, plus significant added latency. Keep the plain model for everyday chat; reach for `-thinking`/`-max` on genuinely hard problems.

## Configuration

| Env | Default | Purpose |
| --- | --- | --- |
| `CLIENT_API_KEY` | *(required)* | key clients must present to this wrapper |
| `ANTHROPIC_OAUTH_TOKEN` | *(required)* | `sk-ant-oat01-…` from `claude setup-token` |
| `PORT` | `8000` | listen port |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | upstream base URL |
| `ANTHROPIC_VERSION` | `2023-06-01` | `anthropic-version` header |
| `ANTHROPIC_BETA` | `oauth-2025-04-20` | `anthropic-beta` header (empty disables) |
| `SPOOF_SYSTEM_PROMPT` | `You are Claude Code, Anthropic's official CLI for Claude.` | exact first system block; the auth gate |
| `USER_AGENT` | `claude-cli/1.0.0 (external, cli)` | mirrors the official client |
| `MODELS` | `claude-fable-5,claude-opus-4-8,claude-sonnet-5` | advertised model list (comma-separated) |
| `DEFAULT_MODEL` | `claude-sonnet-5` | used when a request omits the model |
| `DEFAULT_MAX_TOKENS` | `8192` | injected when the client omits `max_tokens` |
| `ENABLE_WEB_SEARCH` | `false` | add Anthropic's server-side `web_search` tool to every request |
| `MAX_THINKING_TOKENS` | `0` | default thinking budget for **plain** (un-suffixed) model ids; `0` = off |
| `THINKING_MODELS` | `claude-opus-4-8,claude-sonnet-5` | base models that accept an explicit thinking budget (get `-thinking`/`-max` aliases and honor `reasoning_effort`) |
| `THINKING_BUDGET_LOW` / `_MEDIUM` / `_HIGH` | `2048` / `8192` / `16384` | `reasoning_effort` → thinking budget mapping |
| `MAX_OUTPUT_TOKENS` | `32000` | output-token ceiling for a `-max` model variant |
| `REQUEST_TIMEOUT_SECONDS` | `600` | upstream request timeout |
| `MAX_RETRIES` | `2` | retries on 429 / 5xx with backoff |
| `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |

## The token: generate, rotate, and its 1-year TTL

> ⚠️ **The `setup-token` token is valid for 1 year and does NOT auto-renew.** When it expires, every request returns HTTP 401 and chat stops until you replace it. Set a calendar reminder ~11 months out. On a 401 the wrapper logs a loud line telling you to regenerate.

**Generate** (on any machine with your subscription):

```bash
npm i -g @anthropic-ai/claude-code   # if needed
claude setup-token                   # browser OAuth flow -> prints sk-ant-oat01-...
```

Use the printed value as `ANTHROPIC_OAUTH_TOKEN`.

**Rotate / replace** (yearly, or if revoked): regenerate with `claude setup-token`, update the secret in your deployment, and restart the process (it reads the token at startup).

## Run

### Docker

```bash
docker build -t claude-subscription-openai-wrapper .
docker run -d -p 8000:8000 \
  -e CLIENT_API_KEY=your-client-key \
  -e ANTHROPIC_OAUTH_TOKEN=sk-ant-oat01-... \
  --name claude-sub-wrapper \
  claude-subscription-openai-wrapper
```

Prebuilt image (published by CI): `ghcr.io/m600x/claude-subscription-openai-wrapper:latest`.

### Local

```bash
cp .env.example .env   # fill in CLIENT_API_KEY and ANTHROPIC_OAUTH_TOKEN
set -a; . ./.env; set +a
go run ./cmd/server
```

### Smoke test

```bash
curl -s localhost:8000/health
curl -s localhost:8000/v1/models -H "Authorization: Bearer $CLIENT_API_KEY"
curl -sN localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"say hi"}]}'
```

## Connect Open WebUI

Admin Panel → Settings → Connections → OpenAI API → add:

- **URL**: `http://<host>:8000/v1` (or the in-cluster Service URL)
- **Key**: your `CLIENT_API_KEY`

## Development

A `Makefile` wraps the common tasks:

```bash
make install   # install deps + linters (golangci-lint, hadolint) for local dev
make lint      # gofmt + go vet + golangci-lint + hadolint (Dockerfile)
make test      # go test ./...
make build     # multi-arch docker image (linux/amd64 + linux/arm64)
make up        # build (native) + run the container in the background (needs .env)
make down      # stop and remove the container
make run       # run natively with `go run` (needs .env)
```

Push a multi-arch image with `make build PUSH=--push`. Override defaults via
`IMAGE`, `TAG`, `PORT`, `CONTAINER`, `PLATFORMS`.

CI (`.github/workflows/ci.yml`) runs a single sequential pipeline — **lint → tests → build → push** — with a per-job summary. On pushes to `main` the built multi-arch image is published to GHCR; pull requests run everything except the push.

## Limitations

- Text in / text out. Image inputs are dropped (v1).
- No function/tool calling passthrough beyond Anthropic's server-side `web_search`.
- Single-user by design: a subscription OAuth token is for your own use under Anthropic's terms. Do not put it in front of other people's traffic.

## Licence

MIT
