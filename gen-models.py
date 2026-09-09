#!/usr/bin/env python3
"""Generate models.json by scraping the vendors' public model docs.

    ./gen-models.py
    ./gen-models.py models_test.json   # -> to a different file (e.g. to diff)
    ./gen-models.py -q                 # only warnings + the final one-liner
    ./gen-models.py -n                 # dry run: parse + report, write nothing
    ./gen-models.py --all              # also include legacy / previous-generation
                                       #   models that both vendors still serve
    ./gen-models.py --allow-partial    # write even if a provider parsed nothing

A provider that parses to zero models is treated as a scrape failure: the run
exits non-zero and leaves the output file untouched rather than publishing a
registry with that provider's models silently dropped.

Progress is reported on stderr (every fetch, what each selector matched, a
summary table, and a field-level diff against the file being replaced), so a
run that silently mis-parses a restructured docs page is visible. stdout stays
empty: `./gen-models.py /dev/stdout` remains usable.

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

Sources: Anthropic's docs pages (and OpenAI's pricing and per-model pages) are
scraped as markdown -- their rendered HTML is client-side and carries no
tables, so every URL below is the page's ".md" twin. The Codex model list is
the exception: its markdown is a client component, so that one page is still
parsed as HTML.

Because it scrapes docs, it is inherently brittle: if a vendor restructures its
docs, the selectors here may need updating. It is a convenience regenerator, no guarantee.
"""

import html
import json
import os
import re
import sys
import time
import urllib.request

# Anthropic's docs are client-rendered: the served HTML carries no tables, so
# scrape the ".md" twin of each page (same URL + ".md") instead.
ANTHROPIC_OVERVIEW = "https://platform.claude.com/docs/en/models/overview.md"
ANTHROPIC_EFFORT = "https://platform.claude.com/docs/en/build-with-claude/effort.md"
ANTHROPIC_PRICING = "https://platform.claude.com/docs/en/about-claude/pricing.md"
OPENAI_MODELS = "https://learn.chatgpt.com/docs/models"
# The pricing page's HTML is client-rendered (older models live only in a JSON
# payload), so use its ".md" twin; the Codex model list below has no usable
# markdown twin (its cards and level picker are a client component).
OPENAI_PRICING = "https://developers.openai.com/api/docs/pricing.md"
# Per-model spec pages ("Context window" lives here; the Codex docs above
# carry no token figures). Codex-only models (e.g. gpt-5.3-codex-spark) have
# no page here -- their context_window is omitted.
OPENAI_MODEL_PAGE = "https://developers.openai.com/api/docs/models/{mid}.md"

DEFAULT_MAX_TOKENS = 8192  # wrapper injection when a client omits max_tokens

# Canonical ordering of effort levels (low -> high) and label normalization.
RANK = {"off": 0, "none": 0, "minimal": 1, "low": 2, "medium": 3,
        "high": 4, "xhigh": 5, "max": 6, "ultra": 7}
LABEL = {"none": "none", "minimal": "minimal", "low": "low", "medium": "medium",
         "high": "high", "extra high": "xhigh", "xhigh": "xhigh",
         "max": "max", "ultra": "ultra"}


# --------------------------------------------------------------------------
# Progress reporting (stderr only; stdout is reserved for `out_path=/dev/stdout`)
# --------------------------------------------------------------------------
QUIET = False
COLOR = sys.stderr.isatty() and os.environ.get("NO_COLOR") is None
WARNINGS = 0
_STARTED = time.monotonic()


def paint(code, s):
    return f"\033[{code}m{s}\033[0m" if COLOR else s


def step(msg):
    """A stage header."""
    if not QUIET:
        print(paint("1;36", f"\n== {msg}"), file=sys.stderr)


def info(msg):
    if not QUIET:
        print(f"   {msg}", file=sys.stderr)


def ok(msg):
    if not QUIET:
        print(f" {paint('32', 'ok')} {msg}", file=sys.stderr)


def warn(msg):
    global WARNINGS
    WARNINGS += 1
    print(f" {paint('33', 'warning')} {msg}", file=sys.stderr)


def preview(items, limit=6):
    """A compact, bounded rendering of a list for a log line."""
    items = list(items)
    head = ", ".join(str(x) for x in items[:limit])
    return head + (f", +{len(items) - limit} more" if len(items) > limit else "")


def fmt_tokens(n):
    if n >= 1_000_000:
        return f"{n / 1_000_000:g}M"
    if n >= 1_000:
        return f"{n / 1_000:g}k"
    return str(n)


def fetch(url, env_key, label):
    """Return the page text: a local file if env_key is set, else the live URL."""
    local = os.environ.get(env_key)
    if local:
        with open(local, encoding="utf-8") as f:
            body = f.read()
        ok(f"{label}: {len(body):,} chars from {local} (via ${env_key})")
        return body
    info(f"{label}: GET {url}")
    t0 = time.monotonic()
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read()
        status = getattr(r, "status", None) or r.getcode()
    body = raw.decode("utf-8", "replace")
    ok(f"{label}: HTTP {status}, {len(raw):,} bytes in "
       f"{time.monotonic() - t0:.2f}s (override with ${env_key})")
    return body


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
# Markdown helpers (Anthropic docs are scraped as markdown)
# --------------------------------------------------------------------------
def unlink(cell):
    """A markdown cell as plain text: [t](url) -> t, backticks dropped."""
    return re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", cell).replace("`", "").strip()


def md_tables(text):
    """Every pipe table in the document, as a list of rows of raw cells."""
    tables, cur = [], []
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("|") and line.endswith("|") and len(line) > 1:
            cells = [c.strip() for c in line[1:-1].split("|")]
            if not all(re.fullmatch(r":?-{2,}:?", c) for c in cells):
                cur.append(cells)
        elif cur:
            tables.append(cur); cur = []
    if cur:
        tables.append(cur)
    return tables


def md_table(text, *needles):
    """The first table whose header row mentions every needle, or None."""
    for t in md_tables(text):
        header = " | ".join(t[0]).lower()
        if all(n.lower() in header for n in needles):
            return t
    return None


def name_re(display_name):
    """Match a model's display name but not a longer sibling: "Claude Fable 5"
    must not match "Claude Fable 5.1"."""
    return re.compile(re.escape(display_name) + r"(?![.\d])", re.I)


# --------------------------------------------------------------------------
# Anthropic
# --------------------------------------------------------------------------
def anthropic_effort_matrix(effort_md):
    """The effort ladder as [(level, available_on)], low -> high.

    The effort docs render one row per level; rows that restrict the level name
    the models ("Available on Claude Opus 5, Claude Sonnet 5, and ..."), rows
    without that clause apply to every model (available_on is None).
    """
    table = md_table(effort_md, "Level")
    if not table:
        warn('effort docs: no "Level" table found; no efforts will be advertised')
        return []
    matrix = []
    for row in table[1:]:
        level = LABEL.get(unlink(row[0]).lower())
        if level is None or RANK[level] == 0:
            continue  # unknown label, or "none" (the disable, emitted as "off")
        # Stop at a sentence-ending period only: model names contain dots ("5.1").
        clause = re.search(r"Available on (.*?)\.(?=\s|$)", " ".join(row[1:]))
        names = None
        if clause:
            names = [unlink(n) for n in re.split(r",|\band\b", clause.group(1))]
            names = [n for n in names if n.lower().startswith("claude")]
        matrix.append((level, names))
        info(f"effort {level}: " +
             (f"{len(names)} models ({preview(names, 4)})" if names else "all models"))
    matrix.sort(key=lambda lv: RANK[lv[0]])
    return matrix


def anthropic_efforts(display_name, matrix):
    """The ladder for one model, from the effort matrix."""
    return [lvl for lvl, names in matrix
            if names is None or any(name_re(n).fullmatch(display_name) for n in names)]


def anthropic_prices(pricing_md, models):
    """$/MTok per model from the pricing page's "Model pricing" table.

    Columns are located by header label (base input, 5m/1h cache writes, cache
    hits, output), so a reordered table still parses; only the 5m write is
    kept, matching the "cache_write" field.
    """
    table = md_table(pricing_md, "Base Input Tokens")
    if not table:
        warn('pricing page: no "Base Input Tokens" table found; omitting pricing')
        return {}
    header = [unlink(c).lower() for c in table[0]]

    def column(*needles):
        for i, h in enumerate(header):
            if all(n in h for n in needles):
                return i
        return -1

    cols = {"input": column("base", "input"), "cache_write": column("5m", "cache"),
            "cache_read": column("cache", "hits"), "output": column("output")}
    missing = [k for k, i in cols.items() if i < 0]
    if missing:
        warn(f"pricing table has no column for {preview(missing)}; omitting pricing")
        return {}
    info(f"pricing table: {len(table) - 1} rows, columns "
         + ", ".join(f"{k}={i}" for k, i in cols.items()))

    prices = {}
    for m in models:
        pat = name_re(m["display_name"])
        row = next((r for r in table[1:] if pat.match(unlink(r[0]))), None)
        if row is None:
            continue
        cost = {}
        for field, i in cols.items():
            hit = re.search(r"\$([0-9.]+)\s*/\s*MTok", row[i]) if i < len(row) else None
            if hit:
                cost[field] = float(hit.group(1))
        if "input" in cost and "output" in cost:
            prices[m["id"]] = cost
            info(f"{m['id']}: " + " / ".join(f"{k} {v}" for k, v in cost.items()))
        else:
            warn(f"{m['id']}: pricing row found but unparsable ({unlink(row[0])!r})")
    absent = [m["id"] for m in models if m["id"] not in prices]
    if absent:
        warn(f"no pricing row for: {preview(absent)}; omitting pricing")
    return prices


def anthropic_legacy_links(overview_md):
    """[(display_name, page_url)] from the "Legacy models (still available)" line.

    These models are absent from the comparison table but remain usable, so the
    registry must keep serving them; each has its own overview page.
    """
    line = re.search(r"Legacy models \(still available\):(.*)", overview_md)
    if not line:
        warn('no "Legacy models (still available)" line; legacy models omitted')
        return []
    links = re.findall(r"\[([^\]]+)\]\(([^)]+)\)", line.group(1))
    ok(f"legacy models (still available): {len(links)}: "
       + preview(n for n, _ in links))
    return links


def anthropic_legacy_model(name, url):
    """One legacy model's fields, from its own overview page, or None.

    The page carries a "Platform / Model ID" table (the Claude API row is the
    id) and a comparison table whose own row is tagged "(this model)".
    """
    env_key = "SRC_ANTHROPIC_MODEL_" + re.sub(r"[^A-Z0-9]", "_", name.upper())
    try:
        page = fetch(url + ".md", env_key, f"{name} page")
    except Exception as e:  # noqa: BLE001 - a missing page means "skip it"
        warn(f"{name}: overview page fetch failed ({e}); skipping")
        return None

    # Prefer the alias over the dated snapshot, as the current lineup does.
    ids = md_table(page, "Platform", "Model ID")
    mid = ""
    for label in ("Claude API alias", "Claude API"):
        row = next((r for r in ids[1:] if unlink(r[0]) == label), None) if ids else None
        if row and len(row) > 1:
            mid = unlink(row[1])
            break
    if not re.fullmatch(r"claude-[a-z0-9.\-]+", mid):
        warn(f"{name}: no Claude API model ID on its page; skipping")
        return None

    table = md_table(page, "Model", "Thinking")
    if not table:
        warn(f"{mid}: no comparison table on its page; skipping")
        return None
    header = [unlink(c).lower() for c in table[0]]

    def col(row, *needles):
        for i, h in enumerate(header):
            if all(n in h for n in needles) and i < len(row):
                return unlink(row[i])
        return ""

    self_row = next((r for r in table[1:] if "(this model)" in unlink(r[0])), None)
    if self_row is None:
        warn(f"{mid}: its own row is not marked \"(this model)\"; skipping")
        return None
    context = col(self_row, "context")
    if not re.search(r"[0-9]", context):
        # Fall back to the page's summary line ("Context window: 1M tokens").
        hit = re.search(r"Context window:\s*([0-9.]+\s*[kKmM])", page)
        context = hit.group(1) if hit else ""
    return {"id": mid, "display_name": name, "legacy": True,
            "thinking": col(self_row, "thinking"),
            "effort": col(self_row, "default effort"),
            "context": context}


def parse_anthropic(overview_md, effort_md, pricing_md, include_legacy=False):
    step("Anthropic: parse overview table")
    # Only the current lineup: everything before the "Legacy models" line.
    cut = overview_md.find("Legacy models")
    cur = overview_md[:cut] if cut > 0 else overview_md
    if cut > 0:
        info(f'cut at "Legacy models" (kept {cut:,} of {len(overview_md):,} chars)')
    else:
        warn('no "Legacy models" marker; legacy models may leak into the output')

    # The comparison table is transposed: one column per model, one row per
    # feature ("Claude API alias", "Thinking", "Default effort", ...).
    table = md_table(cur, "Feature")
    if not table:
        sys.exit("error: anthropic overview has no \"Feature\" comparison table "
                 "(docs layout changed; see the URLs at the top of this script)")
    display_names = [unlink(c) for c in table[0][1:]]
    rows = {unlink(r[0]).lower(): [unlink(c) for c in r[1:]] for r in table[1:]}
    ok(f"comparison table: {len(display_names)} model columns, {len(rows)} feature "
       f"rows: {preview(display_names)}")

    def cell(label, i):
        vals = rows.get(label)
        if vals is None:
            warn(f'overview table has no "{label}" row')
            return ""
        return vals[i] if i < len(vals) else ""

    models = []
    for i, name in enumerate(display_names):
        alias = cell("claude api alias", i) or cell("claude api id", i)
        if not re.fullmatch(r"claude-[a-z0-9.\-]+", alias):
            warn(f"{name}: no usable model id (alias cell {alias!r}); skipping")
            continue
        models.append({"id": alias, "display_name": name,
                       "thinking": cell("thinking", i),
                       "effort": cell("default effort", i),
                       "context": cell("context window", i)})
    if models:
        ok("model ids: " + preview(f"{m['display_name']} -> {m['id']}" for m in models))

    # Current lineup first: registry.First picks the default model from the top.
    step("Anthropic: legacy models (still available)")
    if not include_legacy:
        info("skipped: pass --all to include them")
    else:
        for name, url in anthropic_legacy_links(overview_md):
            legacy = anthropic_legacy_model(name, url)
            if legacy:
                info(f"{legacy['id']}: thinking={legacy['thinking']!r}, "
                     f"effort={legacy['effort'] or '-'}, ctx={legacy['context'] or '?'}")
                models.append(legacy)

    step("Anthropic: parse effort docs")
    matrix = anthropic_effort_matrix(effort_md)

    step("Anthropic: parse pricing page")
    prices = anthropic_prices(pricing_md, models)

    step("Anthropic: build model entries")
    out = []
    for m in models:
        mid, name = m["id"], m["display_name"]
        thinking, default = m["thinking"], m["effort"].lower()
        adaptive = thinking.lower().startswith("adaptive")
        supported = adaptive and "not supported" not in default
        efforts = anthropic_efforts(name, matrix) if supported else []

        entry = {"id": mid, "provider": "anthropic", "upstream_id": mid}
        if m.get("legacy"):
            entry["comment"] = ("Legacy model: superseded but still available "
                                "per Anthropic's model overview.")
        if not efforts:
            # Adaptive thinking (and with it effort) unsupported -> plain model.
            note = ("Adaptive thinking is not supported on this model, "
                    "so no reasoning efforts are advertised.")
            entry["comment"] = f"{entry['comment']} {note}" if "comment" in entry else note
            entry["reasoning"] = {"efforts": [], "default": "", "mode": "opt-in"}
        else:
            if default not in efforts:
                if default:
                    warn(f'{mid}: default effort {default!r} is not in its ladder; '
                         "using \"high\"")
                default = "high" if "high" in efforts else efforts[-1]
            if "always on" in thinking.lower():
                # Thinking cannot be turned off: no "off" rung.
                entry["reasoning"] = {"efforts": efforts, "default": default,
                                      "mode": "always-on"}
            else:
                entry["reasoning"] = {"efforts": ["off"] + efforts, "default": default,
                                      "mode": "default-on"}
        window = re.search(r"([0-9]+(?:\.[0-9]+)?)\s*([kKmM])", m["context"])
        if window:
            entry["context_window"] = tokens(window.group(1), window.group(2))
        elif m["context"]:
            warn(f"{mid}: unparsable context window {m['context']!r}; omitting")
        if mid in prices:
            entry["pricing"] = prices[mid]
        entry["default_max_tokens"] = DEFAULT_MAX_TOKENS
        info(f"{mid}: thinking={thinking!r} -> mode={entry['reasoning']['mode']}, "
             f"efforts=[{preview(entry['reasoning']['efforts'], 8)}], "
             f"default={entry['reasoning']['default'] or '-'}")
        out.append(entry)
    report = ok if out else warn
    report(f"{len(out)} anthropic models" + ("" if out else
           " -- the docs layout for this provider has likely changed"))
    return out


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
        page = fetch(OPENAI_MODEL_PAGE.format(mid=mid), env_key, f"{mid} model page")
    except Exception as e:  # noqa: BLE001 - any fetch failure means "unknown"
        warn(f"{mid}: model page fetch failed ({e}); omitting context_window")
        return None
    clean = re.sub(r"\s+", " ", strip_tags(page))
    # Spec formats seen in the wild: "1,050,000 context window" (plain count
    # before the label), "Context window 1.05M", "1.05M context".
    m = re.search(r"([0-9][0-9,]{3,})\s*context window", clean, re.I)
    if m:
        n = int(m.group(1).replace(",", ""))
        info(f"{mid}: context window {fmt_tokens(n)} ({n:,}) from a plain count")
        return n
    m = (re.search(r"Context window\s*([0-9]+(?:\.[0-9]+)?)\s*([kKM])", clean)
         or re.search(r"([0-9]+(?:\.[0-9]+)?)\s*([kKM])\s*(?:tokens?\s*)?context",
                      clean, re.I))
    if not m:
        warn(f"{mid}: no context window on its model page; omitting context_window")
        return None
    n = tokens(m.group(1), m.group(2))
    info(f"{mid}: context window {fmt_tokens(n)} ({n:,}) from "
         f'"{m.group(1)}{m.group(2)}"')
    return n


def openai_ladder(clean):
    """Shared reasoning ladder + default from the (single) level picker block.

    The docs state every Codex model exposes the same reasoning spectrum, and
    only one picker is rendered (for the default model), e.g.:
        1 . Low  2 . Medium (default)  3 . High  4 . Extra high  5 . Max  6 . Ultra
    """
    if "Select Reasoning Level for" not in clean:
        warn('codex docs: no "Select Reasoning Level for" picker found; '
             "the effort ladder will be empty")
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
    if not default:
        warn('no level marked "(default)" in the picker; falling back to "medium"')
    ok(f"effort ladder: [{preview(efforts, 8)}], default={default or 'medium'}")
    return efforts, (default or "medium")


def openai_prices(pricing_md, model_ids):
    """Standard-tier $/MTok per model from the API pricing page's markdown.

    The "Standard pricing data" table carries a short-context and a
    long-context set of columns; short context is the sticker price the wrapper
    reports. Columns are located by header label, and a "-" cell means the
    vendor lists no such price (e.g. no cache writes on older models). Model
    cells may carry a qualifier ("gpt-5.5 (<272K context length)"), so only the
    leading token is the id.
    """
    table = md_table(pricing_md, "Model", "Short context input")
    if not table:
        warn('openai pricing: no "Short context input" table found; omitting pricing')
        return {}
    header = [unlink(c).lower() for c in table[0]]
    labels = {"input": "short context input",
              "cache_read": "short context cached input",
              "cache_write": "short context cache writes",
              "output": "short context output"}
    cols = {f: header.index(l) if l in header else -1 for f, l in labels.items()}
    missing = [f for f, i in cols.items() if i < 0]
    if missing:
        warn(f"openai pricing table has no column for {preview(missing)}; "
             "omitting pricing")
        return {}
    info(f"standard pricing table: {len(table) - 1} rows, columns "
         + ", ".join(f"{f}={i}" for f, i in cols.items()))

    rows = {}
    for r in table[1:]:
        cell = unlink(r[0]).split()
        if cell:
            rows.setdefault(cell[0], r)  # first row wins; later ones are variants

    prices = {}
    for mid in model_ids:
        row = rows.get(mid)
        if row is None:
            continue
        cost = {}
        for field in ("input", "output", "cache_read", "cache_write"):
            i = cols[field]
            hit = re.search(r"\$([0-9.]+)", row[i]) if i < len(row) else None
            if hit:
                cost[field] = float(hit.group(1))
        if "input" in cost and "output" in cost:
            prices[mid] = cost
            info(f"{mid}: " + " / ".join(f"{k} {v}" for k, v in cost.items()))
        else:
            warn(f"{mid}: pricing row found but unparsable; omitting pricing")
    absent = [m for m in model_ids if m not in prices]
    if absent:
        warn(f"no pricing row for: {preview(absent)}; omitting pricing")
    return prices


def openai_deprecated(clean):
    """Ids the "Deprecated Codex models" section retires, or an empty set.

    That section names both the doomed models and their replacements ("Replace
    gpt-5.4 with gpt-5.6-terra"), so only ids that are the *subject* of a
    retirement sentence count -- never the replacement named after "with".
    """
    start = clean.find("Deprecated Codex models")
    if start < 0:
        info('no "Deprecated Codex models" section; nothing excluded')
        return set()
    section = clean[start:start + 2000]
    ids = set()
    for subject in re.findall(
            r"The ((?:gpt-[0-9][0-9a-z.\-]*(?:,\s*|\s+and\s+)?)+)\s*models?\s+"
            r"(?:retire|are already deprecated|is deprecated|are deprecated)",
            section):
        ids.update(re.findall(r"gpt-[0-9][0-9a-z.\-]*", subject))
    if ids:
        ok(f"deprecated in Codex: {preview(sorted(ids))}")
    else:
        warn('"Deprecated Codex models" section found but no model named as '
             "retiring; its wording may have changed")
    return ids


def parse_openai(models_html, pricing_md, include_legacy=False):
    step("OpenAI: parse Codex model docs")
    clean = re.sub(r"[ \t]+", " ", strip_tags(models_html))
    # Three tiers on this page, in order: the primary model cards, then the
    # models folded behind "View other models" (previous-generation but still
    # usable -- the equivalent of Anthropic's "Legacy models (still
    # available)"), then a "Deprecated Codex models" section of prose.
    fold = clean.find("View other models")
    dep = clean.find("Deprecated")
    end = dep if dep > 0 else len(clean)
    if fold < 0:
        warn('no "View other models" divider; previous-generation models may '
             "be reported as current")
        fold = end
    if dep < 0:
        warn('no "Deprecated" marker; deprecated models may leak into the output')

    def cards(chunk):
        # Each card carries a `codex -m <id>` command that names the model.
        seen, out = set(), []
        for m in re.findall(r"codex -m (gpt-[0-9][0-9a-z.\-]*)", chunk):
            if m not in seen:
                seen.add(m); out.append(m)
        return out

    current, legacy = cards(clean[:fold]), cards(clean[fold:end])
    ok(f"current models: {len(current)}: {preview(current)}")
    ok(f"previous-generation (behind \"View other models\"): {len(legacy)}: "
       + (preview(legacy) or "none"))

    retired = openai_deprecated(clean)
    model_ids = current + (legacy if include_legacy else [])
    if not include_legacy and legacy:
        info(f"skipped {len(legacy)} previous-generation models: "
             "pass --all to include them")
    dropped = [m for m in model_ids if m in retired]
    if dropped:
        warn(f"excluding {preview(dropped)}: retired from Codex with ChatGPT "
             "sign-in per the deprecation section")
    model_ids = [m for m in model_ids if m not in retired]
    if not model_ids:
        warn("no `codex -m <id>` commands matched; no openai models will be emitted")

    efforts, default = openai_ladder(clean)

    step("OpenAI: parse pricing page")
    prices = openai_prices(pricing_md, model_ids)

    step("OpenAI: fetch per-model context windows")
    models = []
    for mid in model_ids:
        m = {
            "id": mid, "provider": "openai", "upstream_id": mid,
            "aliases": [mid.replace("gpt-", "gpt")],  # deterministic dotless alias
            "reasoning": {"efforts": efforts, "default": default},
        }
        if mid in legacy:
            m["comment"] = ("Previous-generation model: still selectable in "
                            "Codex but no longer part of the current lineup.")
        ctx = openai_context_window(mid)
        if ctx:
            m["context_window"] = ctx
        if mid in prices:
            m["pricing"] = prices[mid]
        models.append(m)
    report = ok if models else warn
    report(f"{len(models)} openai models" + ("" if models else
           " -- the docs layout for this provider has likely changed"))
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


# --------------------------------------------------------------------------
# Reporting
# --------------------------------------------------------------------------
def summary_table(models):
    """One aligned row per model: what actually made it into the document."""
    step("Summary")
    rows = []
    for m in models:
        r = m["reasoning"]
        cost = m.get("pricing") or {}
        rows.append((
            m["provider"],
            m["id"],
            (r["default"] or "-") + f" of {len(r['efforts'])}" if r["efforts"] else "-",
            r.get("mode", "-"),
            fmt_tokens(m["context_window"]) if "context_window" in m else "?",
            f"{cost['input']}/{cost['output']}" if cost else "?",
        ))
    head = ("provider", "model", "effort", "mode", "ctx", "$in/$out")
    width = [max(len(str(r[i])) for r in rows + [head]) for i in range(len(head))]
    fmt = "   " + "  ".join(f"{{:<{w}}}" for w in width)
    if not QUIET:
        print(paint("1", fmt.format(*head)), file=sys.stderr)
        for r in rows:
            missing = "?" in (r[4], r[5])
            line = fmt.format(*r)
            print(paint("33", line) if missing else line, file=sys.stderr)


def diff_report(path, doc):
    """Field-level diff of the new document against the file it replaces."""
    step(f"Diff vs {path}")
    if not os.path.exists(path):
        info("file does not exist yet: everything is new")
        return
    with open(path, encoding="utf-8") as f:
        old_raw = f.read()
    if old_raw == doc:
        ok("byte-identical to the existing file (nothing to change)")
        return
    try:
        old = {m["id"]: m for m in json.loads(old_raw)["models"]}
    except Exception as e:  # noqa: BLE001 - an unparsable old file just means no diff
        warn(f"existing file is not a parsable registry ({e}); skipping diff")
        return
    new = {m["id"]: m for m in json.loads(doc)["models"]}
    for mid in new:
        if mid not in old:
            info(paint("32", f"+ {mid} (new model)"))
    for mid in old:
        if mid not in new:
            info(paint("31", f"- {mid} (gone from the docs)"))
    for mid, m in new.items():
        if mid not in old or old[mid] == m:
            continue
        info(f"~ {mid}")
        for k in sorted(set(old[mid]) | set(m)):
            a, b = old[mid].get(k), m.get(k)
            if a != b:
                info(f"    {k}: {json.dumps(a)} -> {json.dumps(b)}")
    if all(old[mid] == new[mid] for mid in new if mid in old) and set(old) == set(new):
        info("only formatting differs (same parsed models)")


def main():
    global QUIET
    argv, dry_run, allow_partial, all_models, out_path = (
        sys.argv[1:], False, False, False, None)
    for a in argv:
        if a in ("-q", "--quiet"):
            QUIET = True
        elif a in ("-n", "--dry-run"):
            dry_run = True
        elif a == "--allow-partial":
            allow_partial = True
        elif a == "--all":
            all_models = True
        elif a in ("-h", "--help"):
            sys.exit(__doc__)
        elif a.startswith("-"):
            sys.exit(f"error: unknown flag {a} (see --help)")
        elif out_path is None:
            out_path = a
        else:
            sys.exit("error: at most one output path")
    out_path = out_path or "models.json"

    step("Fetch sources")  # legacy model pages are fetched later, only with --all
    anthro = fetch(ANTHROPIC_OVERVIEW, "SRC_ANTHROPIC", "anthropic overview")
    effort = fetch(ANTHROPIC_EFFORT, "SRC_EFFORT", "anthropic effort")
    a_pricing = fetch(ANTHROPIC_PRICING, "SRC_ANTHROPIC_PRICING", "anthropic pricing")
    openai = fetch(OPENAI_MODELS, "SRC_OPENAI", "codex models")
    o_pricing = fetch(OPENAI_PRICING, "SRC_OPENAI_PRICING", "openai pricing")

    models = (parse_anthropic(anthro, effort, a_pricing, include_legacy=all_models)
              + parse_openai(openai, o_pricing, include_legacy=all_models))
    n_a = sum(1 for m in models if m["provider"] == "anthropic")
    n_o = len(models) - n_a
    empty = [p for p, n in (("anthropic", n_a), ("openai", n_o)) if not n]
    if empty and not allow_partial:
        summary_table(models)
        sys.exit(f"error: parsed zero {' and zero '.join(empty)} models "
                 f"(docs layout probably changed); {out_path} left untouched. "
                 "Pass --allow-partial to write anyway.")

    summary_table(models)

    step("Render")
    doc = render(models)
    json.loads(doc)  # sanity: must be valid JSON
    ok(f"{len(doc.splitlines())} lines, {len(doc):,} bytes, valid JSON")

    diff_report(out_path, doc)

    elapsed = time.monotonic() - _STARTED
    if dry_run:
        step("Dry run")
        print(f" {paint('33', 'dry run')} {out_path} left untouched "
              f"({n_a} anthropic + {n_o} openai models parsed)", file=sys.stderr)
        return
    with open(out_path, "w", encoding="utf-8") as f:
        f.write(doc)
    step("Done")
    tail = f", {WARNINGS} warning(s)" if WARNINGS else ""
    print(f" {paint('32', 'ok')} wrote {out_path} "
          f"({n_a} anthropic + {n_o} openai models) in {elapsed:.1f}s{tail}",
          file=sys.stderr)


if __name__ == "__main__":
    main()
