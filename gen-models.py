#!/usr/bin/env python3
"""Generate models.json by scraping the vendors' public model docs.

    ./gen-models.py
    ./gen-models.py models_test.json   # -> to a different file (e.g. to diff)

Each model carries a "pricing" object: sticker price for standard processing,
with "currency"/"unit" ("USD" per million tokens) followed by "input",
"output", "cache_read" (cached input / cache hit), and "cache_write"
(Anthropic 5m write / OpenAI cache write, omitted when the vendor lists none).
Anthropic prices come from the docs pricing page; OpenAI prices from the API
pricing page.

Each model also carries "context_window" (tokens) when the vendor documents
it: Anthropic from the overview comparison table's "Context window" row;
OpenAI from each model's page on developers.openai.com. Models whose window
cannot be determined (e.g. Codex-only models with no API docs page) omit the
field -- downstream consumers treat "absent" as "unknown", never guess.

Because it scrapes HTML, it is inherently brittle: if a vendor restructures its
docs, the selectors here may need updating. It is a convenience regenerator, no guarantee.
"""

import html
import json
import os
import re
import sys
import urllib.request

ANTHROPIC_OVERVIEW = "https://platform.claude.com/docs/en/about-claude/models/overview"
ANTHROPIC_EFFORT = "https://platform.claude.com/docs/en/build-with-claude/effort"
ANTHROPIC_PRICING = "https://platform.claude.com/docs/en/about-claude/pricing"
OPENAI_MODELS = "https://learn.chatgpt.com/docs/models"
OPENAI_PRICING = "https://developers.openai.com/api/docs/pricing"
# Per-model spec pages ("Context window" lives here; the Codex docs above
# carry no token figures). Codex-only models (e.g. gpt-5.3-codex-spark) have
# no page here -- their context_window is omitted.
OPENAI_MODEL_PAGE = "https://developers.openai.com/api/docs/models/{mid}"

DEFAULT_MAX_TOKENS = 8192  # wrapper injection when a client omits max_tokens

# Canonical ordering of effort levels (low -> high) and label normalization.
RANK = {"off": 0, "none": 0, "minimal": 1, "low": 2, "medium": 3,
        "high": 4, "xhigh": 5, "max": 6, "ultra": 7}
LABEL = {"none": "none", "minimal": "minimal", "low": "low", "medium": "medium",
         "high": "high", "extra high": "xhigh", "xhigh": "xhigh",
         "max": "max", "ultra": "ultra"}


def fetch(url, env_key):
    """Return the page text: a local file if env_key is set, else the live URL."""
    local = os.environ.get(env_key)
    if local:
        with open(local, encoding="utf-8") as f:
            return f.read()
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read().decode("utf-8", "replace")


def strip_tags(s):
    return html.unescape(re.sub(r"<[^>]+>", " ", s))


def money(v):
    """Render a USD-per-MTok value as a JSON float (5.0, 2.5, 3.125)."""
    return repr(float(v))


def tokens(num, suffix):
    """Normalize a docs token figure ("1M", "200k", "1.05M") to an int count."""
    mult = {"k": 1_000, "m": 1_000_000}[suffix.lower()]
    return int(round(float(num) * mult))


# --------------------------------------------------------------------------
# Anthropic
# --------------------------------------------------------------------------
def anthropic_ladder(effort_html):
    """The adaptive-thinking effort ladder, ordered, from the effort docs."""
    tokens = set(re.findall(r"\b(none|minimal|low|medium|high|xhigh|max|ultra)\b",
                            effort_html.lower()))
    tokens.discard("none")  # "none" is the disable, represented as "off"
    return sorted(tokens, key=lambda t: RANK[t])


def anthropic_display_pattern(mid):
    """Regex matching a model id's display name: claude-opus-4-8 -> Claude Opus 4.8."""
    parts, words, i = mid.split("-")[1:], [], 0
    while i < len(parts):
        if parts[i].isdigit():
            nums = []
            while i < len(parts) and parts[i].isdigit():
                nums.append(parts[i]); i += 1
            words.append(r"\.".join(nums))
        else:
            words.append(re.escape(parts[i])); i += 1
    return r"Claude\s+" + r"\s+".join(words)


def anthropic_prices(pricing_html, model_ids):
    """$/MTok per model from the pricing page's five-column "Model pricing"
    table: base input, 5m cache write, 1h cache write, cache read, output.
    The last matching row wins, so Sonnet's time-boxed introductory-pricing
    row is skipped in favor of the standard one."""
    start = pricing_html.find("Base Input Tokens")
    end = pricing_html.find("Cloud platform pricing")
    table = strip_tags(pricing_html[start:end if end > start else None])
    dollar = r"\$([0-9.]+)\s*/\s*MTok"
    prices = {}
    for mid in model_ids:
        pat = anthropic_display_pattern(mid) + r"[^$]*" + r"\s*".join([dollar] * 5)
        rows = re.findall(pat, table, re.I)
        if rows:
            base, w5m, _w1h, read, out = (float(v) for v in rows[-1])
            prices[mid] = {"input": base, "output": out, "cache_read": read,
                           "cache_write": w5m}
    return prices


def parse_anthropic(overview_html, effort_html, pricing_html):
    # Only the current lineup: everything before the "Legacy models" accordion.
    cut = overview_html.find("Legacy models")
    cur = overview_html[:cut] if cut > 0 else overview_html

    # The "Claude API alias" table row lists the current model IDs, in column
    # order, up to the next row ("AWS Bedrock ID").
    alias_row = cur[cur.find("Claude API alias"):]
    alias_row = alias_row[:alias_row.find("AWS Bedrock")]
    ids = re.findall(r"claude-[a-z]+-[0-9-]+", strip_tags(alias_row))
    # de-dup, preserve order
    seen, model_ids = set(), []
    for m in ids:
        m = m.rstrip("-")
        if m not in seen:
            seen.add(m); model_ids.append(m)

    # The "Adaptive thinking" row: one value per model, same column order.
    adapt_row = cur[cur.find("Adaptive thinking"):]
    adapt_row = adapt_row[:adapt_row.find("Comparative latency")]
    adapt = re.findall(r"Yes \(always on\)|Yes|No", strip_tags(adapt_row))

    # The "Context window" row: "1M tokens" / "200k tokens" per model, same
    # column order, up to the next row ("Max output"). Cell tooltips mention
    # words/characters, so anchor on the literal "tokens" unit.
    ctx_row = cur[cur.find("Context window"):]
    ctx_row = ctx_row[:ctx_row.find("Max output")]
    ctx_vals = re.findall(r"([0-9]+(?:\.[0-9]+)?)\s*([kKM])\s*tokens", ctx_row)
    if len(ctx_vals) != len(model_ids):
        print(f"warning: anthropic context-window row has {len(ctx_vals)} values "
              f"for {len(model_ids)} models; omitting context_window",
              file=sys.stderr)
        ctx_vals = []
    windows = [tokens(n, s) for n, s in ctx_vals]

    prices = anthropic_prices(pricing_html, model_ids)

    ladder = anthropic_ladder(effort_html)
    models = []
    for i, (mid, a) in enumerate(zip(model_ids, adapt)):
        if a == "No":
            # Adaptive thinking unsupported -> serve as a plain model.
            m = {
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "comment": "Adaptive thinking is not supported on this model, "
                           "so no reasoning efforts are advertised.",
                "reasoning": {"efforts": [], "default": "", "mode": "opt-in"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            }
        elif a == "Yes (always on)":
            m = {
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "reasoning": {"efforts": ladder, "default": "high", "mode": "always-on"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            }
        else:  # "Yes" -> effort defaults to high, i.e. thinks unless disabled.
            m = {
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "reasoning": {"efforts": ["off"] + ladder, "default": "high", "mode": "default-on"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            }
        if windows:
            m["context_window"] = windows[i]
        if mid in prices:
            m["pricing"] = prices[mid]
        models.append(m)
    return models


# --------------------------------------------------------------------------
# OpenAI (ChatGPT / Codex sign-in)
# --------------------------------------------------------------------------
def openai_context_window(mid):
    """The model's context window from its API docs page, or None.

    Codex-only models (e.g. gpt-5.3-codex-spark) have no page there -- the URL
    serves a generic shell with no specs -- so a miss is expected: warn and
    omit rather than guess a number.
    """
    env_key = "SRC_OPENAI_MODEL_" + re.sub(r"[^A-Z0-9]", "_", mid.upper())
    try:
        page = fetch(OPENAI_MODEL_PAGE.format(mid=mid), env_key)
    except Exception as e:  # noqa: BLE001 - any fetch failure means "unknown"
        print(f"warning: {mid}: model page fetch failed ({e}); "
              "omitting context_window", file=sys.stderr)
        return None
    clean = re.sub(r"\s+", " ", strip_tags(page))
    # Spec formats seen in the wild: "1,050,000 context window" (plain count
    # before the label), "Context window 1.05M", "1.05M context".
    m = re.search(r"([0-9][0-9,]{3,})\s*context window", clean, re.I)
    if m:
        return int(m.group(1).replace(",", ""))
    m = (re.search(r"Context window\s*([0-9]+(?:\.[0-9]+)?)\s*([kKM])", clean)
         or re.search(r"([0-9]+(?:\.[0-9]+)?)\s*([kKM])\s*(?:tokens?\s*)?context",
                      clean, re.I))
    if not m:
        print(f"warning: {mid}: no context window on its model page; "
              "omitting context_window", file=sys.stderr)
        return None
    return tokens(m.group(1), m.group(2))
def openai_ladder(clean):
    """Shared reasoning ladder + default from the (single) level picker block.

    The docs state every Codex model exposes the same reasoning spectrum, and
    only one picker is rendered (for the default model), e.g.:
        1 . Low  2 . Medium (default)  3 . High  4 . Extra high  5 . Max  6 . Ultra
    """
    blk = clean[clean.find("Select Reasoning Level for"):]
    end = blk.find("Press enter")
    blk = blk[:end] if end > 0 else blk[:800]
    levels = re.findall(
        r"\d+\s*\.\s*(Extra high|Minimal|None|Low|Medium|High|Max|Ultra)\s*(\(default\))?",
        blk, re.I)
    efforts, default = [], ""
    for label, is_def in levels:
        key = LABEL[label.lower()]
        if key not in efforts:
            efforts.append(key)
        if is_def:
            default = key
    return efforts, (default or "medium")


def openai_prices(pricing_html, model_ids):
    """Standard-tier $/MTok per model from the API pricing page.

    Each pricing table row reads "<id> $in $cached $writes $out" once tags are
    stripped ("-" where a column doesn't apply). The first occurrence of a
    model is its Standard-processing row (Batch/Flex/Priority come later).
    """
    clean = re.sub(r"\s+", " ", strip_tags(pricing_html))
    prices = {}
    for mid in model_ids:
        m = re.search(
            re.escape(mid)
            + r"\s+\$([0-9.]+)\s+(?:\$([0-9.]+)|-)\s+(?:\$([0-9.]+)|-)\s+\$([0-9.]+)",
            clean)
        if m:
            cost = {"input": float(m.group(1)), "output": float(m.group(4))}
            if m.group(2):
                cost["cache_read"] = float(m.group(2))
            if m.group(3):
                cost["cache_write"] = float(m.group(3))
            prices[mid] = cost
    return prices


def parse_openai(models_html, pricing_html):
    clean = re.sub(r"[ \t]+", " ", strip_tags(models_html))
    # Current models are the cards before the "Deprecated" section; each card
    # carries a `codex -m <id>` command that names the model.
    dep = clean.find("Deprecated")
    current = clean[:dep] if dep > 0 else clean
    ids = re.findall(r"codex -m (gpt-5\.[0-9a-z\-]+)", current)
    seen, model_ids = set(), []
    for m in ids:
        if m not in seen:
            seen.add(m); model_ids.append(m)

    efforts, default = openai_ladder(clean)
    prices = openai_prices(pricing_html, model_ids)
    models = []
    for mid in model_ids:
        m = {
            "id": mid, "provider": "openai", "upstream_id": mid,
            "aliases": [mid.replace("gpt-", "gpt")],  # deterministic dotless alias
            "reasoning": {"efforts": efforts, "default": default},
        }
        ctx = openai_context_window(mid)
        if ctx:
            m["context_window"] = ctx
        if mid in prices:
            m["pricing"] = prices[mid]
        models.append(m)
    return models


# --------------------------------------------------------------------------
# Emit (matches models.json formatting: 2-space indent, inline reasoning)
# --------------------------------------------------------------------------
def qlist(items):
    return ", ".join(f'"{x}"' for x in items)


def render(models):
    out = ["{", '  "models": [']
    for idx, m in enumerate(models):
        comma = "," if idx < len(models) - 1 else ""
        lines = [
            "    {",
            f'      "id": "{m["id"]}",',
            f'      "provider": "{m["provider"]}",',
            f'      "upstream_id": "{m["upstream_id"]}",',
        ]
        if "comment" in m:
            lines.append(f'      "comment": "{m["comment"]}",')
        if "aliases" in m:
            lines.append(f'      "aliases": [{qlist(m["aliases"])}],')
        r = m["reasoning"]
        cost = m.get("pricing")
        cost_line = None
        if cost:
            fields = ", ".join(
                f'"{k}": {money(cost[k])}'
                for k in ("input", "output", "cache_read", "cache_write")
                if k in cost)
            cost_line = (f'      "pricing": {{ "currency": "USD", '
                         f'"unit": "per_million_tokens", {fields} }}')
        ctx_line = None
        if "context_window" in m:
            ctx_line = f'      "context_window": {m["context_window"]}'
        if m["provider"] == "anthropic":
            lines.append(
                f'      "reasoning": {{ "efforts": [{qlist(r["efforts"])}], '
                f'"default": "{r["default"]}", "mode": "{r["mode"]}" }},')
            if cost_line:
                lines.append(cost_line + ",")
            if ctx_line:
                lines.append(ctx_line + ",")
            lines.append(f'      "default_max_tokens": {m["default_max_tokens"]}')
        else:
            tail = [l for l in (cost_line, ctx_line) if l]
            lines.append(
                f'      "reasoning": {{ "efforts": [{qlist(r["efforts"])}], '
                f'"default": "{r["default"]}" }}' + ("," if tail else ""))
            for i, l in enumerate(tail):
                lines.append(l + ("," if i < len(tail) - 1 else ""))
        lines.append("    }" + comma)
        out.extend(lines)
    out.append("  ]")
    out.append("}")
    return "\n".join(out) + "\n"


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "models.json"
    anthro = fetch(ANTHROPIC_OVERVIEW, "SRC_ANTHROPIC")
    effort = fetch(ANTHROPIC_EFFORT, "SRC_EFFORT")
    a_pricing = fetch(ANTHROPIC_PRICING, "SRC_ANTHROPIC_PRICING")
    openai = fetch(OPENAI_MODELS, "SRC_OPENAI")
    o_pricing = fetch(OPENAI_PRICING, "SRC_OPENAI_PRICING")

    models = parse_anthropic(anthro, effort, a_pricing) + parse_openai(openai, o_pricing)
    if not models:
        sys.exit("error: parsed zero models (docs layout may have changed)")

    doc = render(models)
    json.loads(doc)  # sanity: must be valid JSON
    with open(out_path, "w", encoding="utf-8") as f:
        f.write(doc)
    n_a = sum(1 for m in models if m["provider"] == "anthropic")
    n_o = len(models) - n_a
    print(f"wrote {out_path} ({n_a} anthropic + {n_o} openai models)", file=sys.stderr)


if __name__ == "__main__":
    main()
