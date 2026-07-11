# ai-substation
## One OpenAI-compatible endpoint in front of your Claude & Codex subscriptions

Want to use your **Claude** (Pro/Max) or **ChatGPT/Codex** (Plus/Pro) subscription in Open WebUI — through a single OpenAI-compatible endpoint, billed against your subscription, not per-token API keys?

```bash
# Claude: generate a token (valid ~1 year)
claude setup-token
# ChatGPT/Codex: log in and get a refresh token (opens a browser; run on your machine)
go run ./cmd/server login

# Generate a random access key clients will present
openssl rand -hex 32

# Run the wrapper (configure whichever provider(s) you have)
docker run -d -p 8000:8000 \
  -e CLIENT_API_KEY=YOUR_ACCESS_KEY \
  -e ANTHROPIC_OAUTH_TOKEN=YOUR_CLAUDE_TOKEN \
  -e OPENAI_REFRESH_TOKEN=YOUR_CODEX_REFRESH_TOKEN \
  --name ai-substation \
  ghcr.io/m600x/ai-substation:latest

# Point Open WebUI at it
URL: http://localhost:8000/v1
Auth: (Bearer) YOUR_ACCESS_KEY
```

---
## Description

A tiny, fast OpenAI-compatible API in front of **Claude** (Anthropic Messages API) and **Codex** (ChatGPT Responses API), backed by your **subscription** rather than per-token API billing. It calls each upstream **directly** over HTTP using a subscription OAuth token — no CLI subprocess, no Python, no per-request cold start.

Built for serving [Open WebUI](https://github.com/open-webui/open-webui) an internal endpoint that bills against your subscription(s).

## Why this exists

The common approach wraps a vendor CLI as a subprocess, adding process-startup latency to every request. This project talks straight to the upstream HTTP APIs:

- **Single static Go binary** (zero external dependencies), tens of MiB RAM, instant startup.
- **Native streaming**, including reasoning/thinking surfaced as OpenAI `reasoning_content`.
- **Two providers, one endpoint** — Claude and Codex behind the same OpenAI schema.
- **Stateless** — no disk, no credential files; tokens come from env vars (the OpenAI access token is refreshed in-memory).

## Providers & the pivot

A provider is enabled **only if its credentials are present**, and requests are routed to a provider by the **model** they name:

| Provider | Enable with | Upstream | Auth |
| --- | --- | --- | --- |
| Anthropic (Claude) | `ANTHROPIC_OAUTH_TOKEN` | `api.anthropic.com/v1/messages` | 1-year static token (`claude setup-token`) |
| OpenAI (Codex) | `OPENAI_REFRESH_TOKEN` | `chatgpt.com/backend-api/codex/responses` | ChatGPT OAuth (short-lived access token auto-refreshed from a refresh token) |

- Only one configured → the other is silently disabled: its models don't appear in `/v1/models` and are rejected with a clear error.
- Neither configured → the server refuses to start.
- Both → models from both are served; the request's `model` selects the backend.

## How each provider authenticates

**Claude.** The subscription OAuth token (`sk-xxx-oat01-…`) is only honored for requests that identify as Claude Code. The wrapper injects an exact first system block — `You are Claude Code, Anthropic's official CLI for Claude.` — and appends any client system prompt as a **separate** block.

**Codex.** The ChatGPT backend takes a short-lived OAuth **access token** plus a `ChatGPT-Account-ID` header (a claim inside the id_token JWT), an `originator` header, and `OpenAI-Beta: responses=experimental`. Access tokens expire in ~1 hour, so the wrapper keeps a **refresh token** and refreshes the access token in-memory (on expiry and on a 401). Get the refresh token with `server login`.

```
Open WebUI ─OpenAI /v1/chat/completions─▶ wrapper ─┬─ Bearer + spoof ─▶ api.anthropic.com/v1/messages
                                                   └─ Bearer + account ▶ chatgpt.com/backend-api/codex/responses
```

## Model registry (`models.json`)

The advertised models and their supported reasoning efforts live in a root **`models.json`** — the single source of truth, not buried in code. Each entry declares its `provider`, the `upstream_id` sent upstream, optional `aliases`, and a `reasoning` block (`efforts`, `default`, and — for Anthropic — a thinking `mode`).

```json
{ "id": "gpt-5.6-sol", "provider": "openai", "upstream_id": "gpt-5.6-sol",
  "reasoning": { "efforts": ["low","medium","high","xhigh","max"], "default": "medium" } }
```

Add, remove, or retune a model by editing this file — no rebuild. Point elsewhere with `MODELS_CONFIG`. (Neither subscription backend exposes a reliable "list models + per-model efforts" endpoint, so the registry is declarative by design.)

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
| `GET`  | `/v1/models` | models of the enabled provider(s) |
| `GET`  | `/health` | liveness; no auth |

Clients must send `Authorization: Bearer <CLIENT_API_KEY>` (except `/health`).

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
- **Codex** — `server login` runs a browser OAuth (PKCE) flow and prints a **refresh token**. Set it as `OPENAI_REFRESH_TOKEN`; the wrapper refreshes the access token itself. If the refresh token is revoked, re-run `server login`.

## Configuration

| Env | Default | Purpose |
| --- | --- | --- |
| `CLIENT_API_KEY` | *(required)* | key clients present to this wrapper |
| `ANTHROPIC_OAUTH_TOKEN` | — | enables Claude; `sk-xxx-oat01-…` from `claude setup-token` |
| `OPENAI_REFRESH_TOKEN` | — | enables Codex; from `server login` |
| `MODELS_CONFIG` | `models.json` | path to the model registry |
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

## Run

### Docker

```bash
docker build -t ai-substation .
docker run -d -p 8000:8000 \
  -e CLIENT_API_KEY=your-client-key \
  -e ANTHROPIC_OAUTH_TOKEN=sk-xxx-oat01-... \
  -e OPENAI_REFRESH_TOKEN=... \
  --name ai-substation \
  ai-substation
```

Prebuilt image (published by CI): `ghcr.io/m600x/ai-substation:latest`.

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
