#!/usr/bin/env python3
"""Generate models.json by scraping the vendors' public model docs.

    ./gen-models.py
    ./gen-models.py models_test.json   # -> to a different file (e.g. to diff)

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
OPENAI_MODELS = "https://learn.chatgpt.com/docs/models"

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


# --------------------------------------------------------------------------
# Anthropic
# --------------------------------------------------------------------------
def anthropic_ladder(effort_html):
    """The adaptive-thinking effort ladder, ordered, from the effort docs."""
    tokens = set(re.findall(r"\b(none|minimal|low|medium|high|xhigh|max|ultra)\b",
                            effort_html.lower()))
    tokens.discard("none")  # "none" is the disable, represented as "off"
    return sorted(tokens, key=lambda t: RANK[t])


def parse_anthropic(overview_html, effort_html):
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

    ladder = anthropic_ladder(effort_html)
    models = []
    for mid, a in zip(model_ids, adapt):
        if a == "No":
            # Adaptive thinking unsupported -> serve as a plain model.
            models.append({
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "comment": "Adaptive thinking is not supported on this model, "
                           "so no reasoning efforts are advertised.",
                "reasoning": {"efforts": [], "default": "", "mode": "opt-in"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            })
        elif a == "Yes (always on)":
            models.append({
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "reasoning": {"efforts": ladder, "default": "high", "mode": "always-on"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            })
        else:  # "Yes" -> effort defaults to high, i.e. thinks unless disabled.
            models.append({
                "id": mid, "provider": "anthropic", "upstream_id": mid,
                "reasoning": {"efforts": ["off"] + ladder, "default": "high", "mode": "default-on"},
                "default_max_tokens": DEFAULT_MAX_TOKENS,
            })
    return models


# --------------------------------------------------------------------------
# OpenAI (ChatGPT / Codex sign-in)
# --------------------------------------------------------------------------
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


def parse_openai(models_html):
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
    models = []
    for mid in model_ids:
        models.append({
            "id": mid, "provider": "openai", "upstream_id": mid,
            "aliases": [mid.replace("gpt-", "gpt")],  # deterministic dotless alias
            "reasoning": {"efforts": efforts, "default": default},
        })
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
        if m["provider"] == "anthropic":
            lines.append(
                f'      "reasoning": {{ "efforts": [{qlist(r["efforts"])}], '
                f'"default": "{r["default"]}", "mode": "{r["mode"]}" }},')
            lines.append(f'      "default_max_tokens": {m["default_max_tokens"]}')
        else:
            lines.append(
                f'      "reasoning": {{ "efforts": [{qlist(r["efforts"])}], '
                f'"default": "{r["default"]}" }}')
        lines.append("    }" + comma)
        out.extend(lines)
    out.append("  ]")
    out.append("}")
    return "\n".join(out) + "\n"


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "models.json"
    anthro = fetch(ANTHROPIC_OVERVIEW, "SRC_ANTHROPIC")
    effort = fetch(ANTHROPIC_EFFORT, "SRC_EFFORT")
    openai = fetch(OPENAI_MODELS, "SRC_OPENAI")

    models = parse_anthropic(anthro, effort) + parse_openai(openai)
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
