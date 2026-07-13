# AI Subscription Gateway
## An OpenAI API wrapper for your AI subscription (Anthropic and OpenAI)

Want to use your **Claude** (Pro/Max/Enterprise) or **ChatGPT/Codex** (Plus/Pro/Enterprise) subscription in Open WebUI through a single OpenAI-compatible endpoint?

```bash
# Anthropic        (or: claude setup-token)
make anthropic-token
# OpenAI           (or: go run ./cmd/server login)
make openai-token

# Generate a random access key clients will present
openssl rand -hex 32

# Run the wrapper (configure whichever provider(s) you have)
docker run -d -p 8000:8000 \
  -e CLIENT_API_KEY=YOUR_ACCESS_KEY \
  -e ANTHROPIC_TOKEN=YOUR_CLAUDE_TOKEN \
  -e OPENAI_TOKEN=YOUR_CODEX_REFRESH_TOKEN \
  --name ai-subscription-gateway \
  ghcr.io/m600x/ai-subscription-gateway:latest

# Point Open WebUI at it
URL: http://localhost:8000/v1
Auth: (Bearer) YOUR_ACCESS_KEY
```

---
## Description

A tiny, fast OpenAI-compatible API in front of **Claude** (Anthropic Messages API) and **Codex** (ChatGPT Responses API), backed by your **subscription** rather than per-token API billing. It calls each upstream directly over HTTP using a subscription OAuth token, no CLI subprocess, no Python, no per-request cold start.

One key example of usage is in [Open WebUI](https://github.com/open-webui/open-webui).

The common approach wraps a vendor CLI as a subprocess, adding process-startup latency to every request. This project talks straight to the upstream HTTP APIs:

- **Single static Go binary** (zero external dependencies), tens of MiB RAM, instant startup.
- **Native streaming**, including reasoning/thinking surfaced as OpenAI `reasoning_content`.
- **Two providers, one endpoint** — Claude and Codex behind the same OpenAI schema.
- **Stateless** — no disk, no credential files; tokens come from env vars (the OpenAI access token is refreshed in-memory).

## Providers & the pivot

A provider is enabled **only if its credentials are present**, and requests are routed to a provider by the **model** they name:

| Provider | Enable with | Upstream | Auth |
| --- | --- | --- | --- |
| Anthropic (Claude) | `ANTHROPIC_TOKEN` | `api.anthropic.com/v1/messages` | 1-year static token (`claude setup-token`) |
| OpenAI (Codex) | `OPENAI_TOKEN` | `chatgpt.com/backend-api/codex/responses` | ChatGPT OAuth (short-lived access token auto-refreshed from a refresh token) |

- Only one configured → the other is silently disabled: its models don't appear in `/v1/models` and are rejected with a clear error.
- Neither configured → the server refuses to start.
- Both → models from both are served; the request's `model` selects the backend.

## How each provider authenticates

**Claude.** The subscription OAuth token (`sk-xxx-oat01-…`) is only honored for requests that identify as Claude Code. The wrapper injects an exact first system block — `You are Claude Code, Anthropic's official CLI for Claude.` — and appends any client system prompt as a **separate** block.

**Codex.** The ChatGPT backend takes a short-lived OAuth **access token** plus a `ChatGPT-Account-ID` header (a claim inside the id_token JWT), an `originator` header, and `OpenAI-Beta: responses=experimental`. Access tokens expire in ~1 hour, so the wrapper keeps a **refresh token** and refreshes the access token in-memory (on expiry and on a 401). Get the refresh token with `server login`.

**Subcriptions are individual and those tokens should not be shared. Do not use this wrapper to re-distribute your account, it's against ToS.**

## Model registry (`models.json`)

The advertised models and their supported reasoning efforts live in a root **`models.json`** — the single source of truth. Each entry declares its `provider`, the `upstream_id` sent upstream, optional `aliases`, and a `reasoning` block (`efforts`, `default`, and — for Anthropic — a thinking `mode`).

```json
{ "id": "gpt-5.6-sol", "provider": "openai", "upstream_id": "gpt-5.6-sol",
  "reasoning": { "efforts": ["low","medium","high","xhigh","max"], "default": "medium" } }
```

Add, remove, or retune a model by editing this file — no rebuild. Point elsewhere with `MODELS_CONFIG`. (Neither subscription backend exposes a reliable "list models + per-model efforts" endpoint, so the registry is declarative by design.)

**Override without a file** — set the whole registry inline via the `MODELS` env var (the JSON document, same shape as `models.json`). It takes priority over the file, so you can retune the bundled image without a rebuild or a mounted volume. The content is validated (must be valid JSON with at least one complete model for a real provider); if it's malformed the wrapper logs an error and falls back to the file rather than refusing to start.

Anthropic thinking `mode`:

| mode | meaning |
| --- | --- |
| `always-on` | thinking can't be disabled; `off` is ignored (Fable 5) |
| `default-on` | thinks by default; `off` sends an explicit disable (Sonnet 5) |
| `opt-in` | off unless an effort is requested (Opus 4.8) |

## Endpoints

| Method | Path | Notes |
| ------ | ---- | ----- |
| `POST` | `/v1/chat/completions` | OpenAI-compatible; streaming + non-streaming |
| `GET`  | `/v1/models` | models of the enabled provider(s), each with its `reasoning` ladder |
| `GET`  | `/health` | liveness; no auth |

Clients must send `Authorization: Bearer <CLIENT_API_KEY>` (except `/health`).

Each `/v1/models` entry carries a `reasoning` vendor extension mirroring `models.json` — the accepted `reasoning_effort` values, the default, and the mode. Standard OpenAI clients ignore the extra key:

```json
{
  "id": "claude-sonnet-5",
  "object": "model",
  "created": 1752384000,
  "owned_by": "anthropic",
  "reasoning": {
    "efforts": ["off", "low", "medium", "high", "xhigh", "max"],
    "default": "high",
    "mode": "default-on"
  }
}
```

## OpenAI-schema features

Both providers surface OpenAI-standard **token usage** (`usage`, including cached-prompt and reasoning-token breakdowns) and stream reasoning as `reasoning_content`. The Codex provider additionally supports:

- **Function/tool calling** — client `tools` are forwarded and `tool_calls` come back (streamed and non-streamed).
- **Image inputs** — `image_url` content parts are forwarded to the Responses API.
- **`reasoning_effort`** mapped onto the model's effort ladder (per `models.json`).

## Reasoning controls

Send the OpenAI-standard **`reasoning_effort`** (`low|medium|high|xhigh|max`, plus `minimal`/`off`). It's validated against the requested model's ladder in `models.json`:

- **Claude** — maps to adaptive thinking (`output_config.effort` + `thinking:{type:"adaptive"}`); `thinking.display` defaults to `summarized` so thinking streams as readable `reasoning_content`. When thinking is active, `temperature`/`top_p` are dropped and `max_tokens` is raised to leave headroom.
- **Codex** — maps to the Responses `reasoning.effort` (with `summary: auto`), clamped to the model's ladder (falling back to its default).

## The tokens: generate, rotate, TTLs

- **Claude** — `claude setup-token` prints `sk-xxx-oat01-…`, valid **~1 year, no auto-renew**. On expiry every request 401s; the wrapper logs a loud regenerate line. Set a reminder ~11 months out.
- **Codex** — `server login` runs a **headless device-code flow**: it prints a URL and a short code; open the URL on any device, enter the code, and once you approve it prints a **refresh token**. Set it as `OPENAI_TOKEN`; the wrapper refreshes the access token itself. If the refresh token is revoked, re-run `server login`. No local callback server, no browser on the same host — works in containers and over SSH.

### Generating tokens without a local toolchain

If you don't want Node or a Go build on your machine, generate either token in a throwaway Docker container (the container's entrypoint is the token command; `--rm` leaves nothing behind):

```bash
make anthropic-token   # runs `claude setup-token`: open the printed URL, paste the code back
make openai-token      # runs the device-code login: open the printed URL, enter the code
```

Copy the printed token into your deployment's env (`ANTHROPIC_TOKEN` / `OPENAI_TOKEN`).

## Configuration

### Primary

| Env | Default | Purpose |
| --- | --- | --- |
| `CLIENT_API_KEY` | *(required)* | key clients present to this wrapper |
| `ANTHROPIC_TOKEN` | — | enables Claude; `sk-xxx-oat01-…` from `claude setup-token` |
| `OPENAI_TOKEN` | — | enables Codex; from `server login` |

### Optional

| Env | Default | Purpose |
| --- | --- | --- |
| `MODELS` | — | inline registry JSON; overrides the file, falls back to it if invalid |
| `MODELS_CONFIG` | `models.json` | path to the model registry file |
| `DEFAULT_MODEL` | *(first enabled)* | used when a request omits the model |
| `DEFAULT_MAX_TOKENS` | `8192` | injected when the client omits `max_tokens` |
| `PORT` | `8000` | listen port |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Claude upstream base URL |
| `ANTHROPIC_VERSION` / `ANTHROPIC_BETA` | `2023-06-01` / `oauth-2025-04-20` | Claude headers |
| `SPOOF_SYSTEM_PROMPT` | `You are Claude Code, …` | exact first system block; the Claude auth gate |
| `USER_AGENT` | `claude-cli/1.0.0 (external, cli)` | Claude client UA |
| `ENABLE_WEB_SEARCH` | `false` | add Anthropic's server-side `web_search` tool |
| `THINKING_DISPLAY` | `summarized` | Claude `thinking.display` (`summarized` \| `omitted`) |
| `OPENAI_BASE_URL` | `https://chatgpt.com/backend-api/codex` | Codex upstream base URL |
| `OPENAI_AUTH_ISSUER` | `https://auth.openai.com` | Codex OAuth issuer |
| `OPENAI_CLIENT_ID` | `app_EMoamEEZ73f0CkXaXp7hrann` | Codex OAuth client id |
| `OPENAI_ORIGINATOR` | `codex_cli_rs` | `originator` header |
| `OPENAI_USER_AGENT` | `codex_cli_rs/0.1.0 (external; wrapper)` | Codex client UA |
| `OPENAI_BASE_INSTRUCTIONS` | *(empty)* | optional Responses `instructions` prefix |
| `OPENAI_ACCESS_TOKEN` / `OPENAI_ACCOUNT_ID` | — | advanced: static access token (won't auto-renew) |
| `REQUEST_TIMEOUT_SECONDS` | `600` | upstream request timeout |
| `MAX_RETRIES` | `2` | Claude retries on 429/5xx with backoff |
| `LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `STATELESS` | `true` | keep tokens in memory only; `false` persists them to `TOKENS_FILE` |
| `TOKENS_FILE` | `tokens.json` | where tokens are persisted when `STATELESS=false` (Docker image default: `/data/tokens.json`) |

## State: stateless vs persisted

By default (`STATELESS=true`) the wrapper holds nothing on disk: tokens are read
from the environment and the OpenAI access token is refreshed **in memory**.
Because OpenAI **rotates** the refresh token on every refresh, a restart falls
back to the env refresh token — which normally still works, but after a long
downtime it could be stale, requiring a fresh `server login`.

Set `STATELESS=false` to persist tokens to `TOKENS_FILE` (default `tokens.json`,
mode `0600`): the file holds the long-lived Anthropic token and the rotating
OpenAI refresh token, and it is rewritten on every rotation.

**Precedence on restart (non-stateless):** the file wins.

- **OpenAI refresh token** — the persisted (file) value takes priority over
  `OPENAI_TOKEN`. Over the gateway's life the token rotates and the env
  var goes obsolete, so on a pod/host restart the file's latest token is used,
  not the stale env one. The env value is kept only as a fallback (tried if the
  file token is rejected, e.g. after a deliberate re-login). If the file has a
  token, OpenAI is enabled even when the env var is unset.
- **Anthropic token** — the env wins (you rotate this long-lived token via the
  env once a year); the file is used only to backfill when the env is unset.

So a short restart survives without re-login, and a stale env token never
shadows the fresh one on disk.

> In Docker, tokens persist to `/data` — a non-root-writable directory the
> image provides, with `TOKENS_FILE=/data/tokens.json` set by default. So
> `-e STATELESS=false` works out of the box; mount a volume to keep the file
> across container recreation: `-e STATELESS=false -v asg-tokens:/data`.
> (`/app`, holding the binary and `models.json`, stays read-only on purpose.)

## Run

### Docker

```bash
docker build -t ai-subscription-gateway .
docker run -d -p 8000:8000 \
  -e CLIENT_API_KEY=your-client-key \
  -e ANTHROPIC_TOKEN=sk-xxx-oat01-... \
  -e OPENAI_TOKEN=... \
  --name ai-subscription-gateway \
  ai-subscription-gateway
```

Prebuilt image (published by CI): `ghcr.io/m600x/ai-subscription-gateway:latest`.

### Local

```bash
cp .env.example .env    # fill in CLIENT_API_KEY and at least one provider
set -a; . ./.env; set +a
go run ./cmd/server login   # (Codex only) if you need a refresh token
go run ./cmd/server         # serve
```

### Smoke test

```bash
curl -s localhost:8000/health
curl -s localhost:8000/v1/models -H "Authorization: Bearer $CLIENT_API_KEY"
curl -sN localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"say hi"}]}'
```

## Connect Open WebUI

Admin Panel → Settings → Connections → OpenAI API → add:

- **URL**: `http://<host>:8000/v1` (or the in-cluster Service URL)
- **Key**: your `CLIENT_API_KEY`

## Development

```bash
make install   # install deps + linters (golangci-lint, hadolint)
make lint      # gofmt + go vet + golangci-lint + hadolint
make test      # go test ./...
make build     # multi-arch docker image (linux/amd64 + linux/arm64)
make up/down   # build (native) + run / stop the container (needs .env)
make run       # run natively with `go run` (needs .env)
```

CI (`.github/workflows/ci.yml`) runs one sequential pipeline — **lint → tests → build → push** — publishing a multi-arch GHCR image on pushes to `main`.

## Limitations

- Text + images in, text/tool-calls out. Image inputs are forwarded on the Codex provider; the Claude provider is text-only.
- Single-user by design: a subscription OAuth token is for your own use under each vendor's terms. Do not put it in front of other people's traffic.

## Licence

MIT
