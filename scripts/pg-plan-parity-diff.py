#!/usr/bin/env python3
"""pg-plan-parity-diff.py -- goopg-vs-PostgreSQL plan-parity diff (P0-06 instrument).

Compares goopg EXPLAIN output against vanilla-PostgreSQL EXPLAIN fixtures over
a query corpus, reporting per-query shape verdicts plus a corpus roll-up.
Report-only: exit status is always 0 (a mismatch budget is pinned by the
companion test, not by this tool).

Inputs
------
  GOOPG_PLANS : file with one `=== QN` section per query (goopg EXPLAIN,
      no ANALYZE), e.g. analysis/leftdeep-joins/a01ii-cut3-paired.plans.txt
  PG          : either a directory of per-query fixtures (QN.txt, each
      optionally starting with an `=== QN` header line), e.g.
      bench/tpch/plans-pg/, or a single file with `=== QN` sections.

Comparison model
----------------
Both plans are parsed into trees. Node identity is
(node kind incl. Parallel prefix, relation/index, join method+type,
sort/agg strategy, qual attachment) with child order significant.
costs/rows/widths (and any actual times) are parsed into a SEPARATE estimate
column: they never influence the verdict, they are printed alongside it.

Per-query verdicts: MATCH / SHAPE-DIFF / MISSING-NODE / ERROR / TIMEOUT.
Precedence: ERROR > TIMEOUT > MISSING-NODE > SHAPE-DIFF > MATCH.

Nine-category taxonomy (spec order): join-order, join-method, scan-type,
parameterisation, aggregation-strategy, sort-strategy, parallelism,
qual-placement, rendering. Categories are recorded per query; the corpus
roll-up counts queries exhibiting each category (the pinned mismatch budget).

Declared normalisation policy (applied before comparison, printed with
--verbose and summarised in the roll-up):
  N1 estimates stripped from shape: `(cost=.. rows=.. width=..)`, any
     `actual time=..`, `(returns $N)` suffixes. Kept in the estimate column.
  N2 PG standalone `Hash` nodes spliced out (executor structure, not a
     planning choice; goopg never renders one). A plan pair differing ONLY
     by Hash nodes is MATCH, never MISSING-NODE.
  N3 vacuous `Filter: (true)` / `Join Filter: (true)` dropped.
  N4 alias/suffix canonicalisation: scan target `nation nation_1` /
     `nation_1` / `nation` all read as `nation`; `rel.col` qualifiers are
     dropped when `rel` is one of the corpus base relations (collected from
     the scan nodes, so this generalises beyond TPC-H); `name_N`
     identifiers collapse to `name` for known base relations.
  N5 cast and operator rendering: `::type` (incl. multi-word `timestamp
     without time zone`, `double precision`, `character varying`, `[]`
     array forms) stripped; `~~`/`!~~`/`~~*`/`!~~*` read as
     LIKE/NOT LIKE/ILIKE/NOT ILIKE.
  N6 qual and key signatures: Filter/Index Cond/Join Filter/Hash Cond/Merge
     Cond/Recheck Cond compare by (referenced columns incl. SubPlan/InitPlan
     /$N markers, operator multiset), NOT by literal values or expression
     shape -- the same query carries the same predicates, so literal and
     expression-form differences (date literal vs date+interval, to_date()
     vs literal) are rendering, not placement. Sort Key / Group Key
     signature mismatches are `rendering` (verdict-neutral): same plan
     printed differently must NOT flag as shape.
  N7 outer-join direction canonicalised: `Hash Right [Semi|Anti] Join`
     swaps children and reads as Left (PG prints Right where goopg prints
     Left for the same preserved side; Q13 is the witness).

MISSING-NODE rule: a normalised PG tree containing a node kind goopg's
EXPLAIN renderer cannot emit -- `Materialize`, `Incremental Sort` (grounded:
no such arms in internal/executor/operators_explain.go) -- or any unknown
kind. `Hash` is explicitly NOT in this set (see N2).

Usage:
  pg-plan-parity-diff.py GOOPG_PLANS PG [--verbose] [--self-test]

  --verbose    print normalised trees for non-MATCH queries plus the first
               divergence paths and the applied normalisation list.
  --self-test  run the built-in unit checks over recorded plan pairs
               (hermetic: no corpus files needed). Exit nonzero on failure.
"""

import argparse
import difflib
import os
import re
import sys

CATEGORIES = (
    "join-order",
    "join-method",
    "scan-type",
    "parameterisation",
    "aggregation-strategy",
    "sort-strategy",
    "parallelism",
    "qual-placement",
    "rendering",
)

VERDICTS = ("MATCH", "SHAPE-DIFF", "MISSING-NODE", "ERROR", "TIMEOUT")

# Node kinds goopg's EXPLAIN renderer cannot emit (grounded in
# internal/executor/operators_explain.go, which has no such arms; goopg's
# executor does have a Materialize operator, but nothing renders it).
# A PG plan containing one forces MISSING-NODE. Standalone `Hash` is
# deliberately absent here: it is stripped by normalisation N2.
GOOPG_UNEMITTABLE = ("Materialize", "Incremental Sort")

SECTION_RE = re.compile(r"^===\s*(\S+)\s*$")
COST_RE = re.compile(r"\(cost=([0-9.]+)\.\.([0-9.]+)\s+rows=([0-9]+)\s+width=([0-9]+)\)")
ACTUAL_RE = re.compile(r"actual time=([0-9.]+)\.\.([0-9.]+)\s+rows=([0-9.]+)\s+loops=([0-9.]+)")
ARROW_RE = re.compile(r"^( *)->\s+(.*)$")
AUX_RE = re.compile(r"^(SubPlan|InitPlan) (\d+)\s*(?:\(returns \$\d+\))?\s*$")
CAST_PHRASE_RE = re.compile(
    r"::(?:timestamp\s+without\s+time\s+zone|timestamp\s+with\s+time\s+zone"
    r"|double\s+precision|character\s+varying)"
)
CAST_RE = re.compile(r"::[A-Za-z_]+(?:\[\])?")
QUALIFIER_RE = re.compile(r"\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_$]*)")
SUFFIX_RE = re.compile(r"\b([A-Za-z_][A-Za-z0-9_]*?)_(\d+)\b")
LITERAL_RE = re.compile(r"'(?:[^']|'')*'")
NUMBER_RE = re.compile(r"(?<![A-Za-z0-9_.$])(?:\d+\.\d+|\d+)(?![A-Za-z0-9_])")
OP_RE = re.compile(
    r"<>|!=|<=|>=|=|<|>|\b(?:AND|OR|NOT|LIKE|ILIKE|ANY|ALL|IN|IS|NULL|EXISTS|BETWEEN|CASE|WHEN|THEN|ELSE|END|EXTRACT|OVERLAPS|COLLATE)\b"
)
IDENT_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_$]*|\$[0-9]+")
KEYWORDS = frozenset(
    "AND OR NOT LIKE ILIKE ANY ALL IN IS NULL EXISTS BETWEEN CASE WHEN THEN ELSE END "
    "EXTRACT YEAR MONTH DAY COLLATE OVERLAPS TRUE FALSE INTERVAL DATE TIMESTAMP".split()
)
TIMEOUT_RES = (
    re.compile(r"statement timeout", re.I),
    re.compile(r"canceling statement", re.I),
    re.compile(r"cancelling statement", re.I),
    re.compile(r"\bTIMEOUT\b"),
    re.compile(r"terminated by timeout", re.I),
)
ERROR_RE = re.compile(r"\bERROR\b|\bFATAL\b|psql:.+ERROR")

JOIN_METHODS = ("Nested Loop", "Hash Join", "Merge Join")
SCAN_KINDS = (
    "Seq Scan",
    "Index Scan",
    "Index Only Scan",
    "Bitmap Heap Scan",
    "Bitmap Index Scan",
    "Tid Scan",
    "Sample Scan",
    "Function Scan",
    "Values Scan",
    "CTE Scan",
    "WorkTable Scan",
    "Foreign Scan",
    "Custom Scan",
    "Subquery Scan",
)
AGG_KINDS = ("Aggregate", "HashAggregate", "GroupAggregate", "MixedAggregate")
# goopg renders grouping-sets aggregates as "HashAggregate (N keys, M grouping
# sets)"; the suffix is a strategy attribute (kept in detail), not a kind.
AGG_SUFFIX_RE = re.compile(r"^(%s) (\(\d+ keys, \d+ grouping sets\))$" % "|".join(AGG_KINDS))
# Generous known-kind universe (PG EXPLAIN node types + goopg renderer arms).
# Anything outside this set triggers an unknown-kind warning.
KNOWN_KINDS = frozenset(
    list(SCAN_KINDS)
    + list(AGG_KINDS)
    + [
        "Nested Loop",
        "Hash Join",
        "Merge Join",
        "Sort",
        "Incremental Sort",
        "Unique",
        "Limit",
        "Append",
        "MergeAppend",
        "Recursive Union",
        "Result",
        "ProjectSet",
        "WindowAgg",
        "Group",
        "Memoize",
        "Materialize",
        "Hash",
        "HashSetOp",
        "SetOp",
        "LockRows",
        "Gather",
        "Gather Merge",
        "SubPlan",
        "InitPlan",
        "ModifyTable",
    ]
)


class Node:
    __slots__ = ("kind", "detail", "parallel", "props", "children", "aux",
                 "est", "indent", "raw")

    def __init__(self, kind, indent, detail="", parallel=False):
        self.kind = kind            # e.g. "Hash Join", "Seq Scan", "Sort"
        self.detail = detail        # join-type suffix / scan target / index
        self.parallel = parallel    # Parallel prefix flag
        self.props = {}             # Filter/Index Cond/... -> raw text
        self.children = []          # stream children (ordered)
        self.aux = []               # SubPlan/InitPlan children, by name order
        self.est = {}               # cost0/cost1/rows/width/actual*
        self.indent = indent
        self.raw = ""               # original line (debug)


def parse_sections(path):
    """file with === KEY sections -> {key: [lines]} (headers excluded)."""
    blocks, cur = {}, None
    with open(path, encoding="utf-8", errors="replace") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            m = SECTION_RE.match(line.strip())
            if m:
                cur = m.group(1)
                blocks[cur] = []
                continue
            if cur is None:
                continue
            blocks[cur].append(line.rstrip())
    for key, body in blocks.items():
        while body and not body[0].strip():
            body.pop(0)
        while body and not body[-1].strip():
            body.pop()
    return blocks


def load_pg(path):
    """PG side: directory of QN.txt files, or a single === sections file."""
    if os.path.isdir(path):
        out = {}
        for name in sorted(os.listdir(path)):
            if not name.endswith(".txt"):
                continue
            key = name[:-len(".txt")]
            with open(os.path.join(path, name), encoding="utf-8",
                      errors="replace") as fh:
                lines = [ln.rstrip("\n").rstrip() for ln in fh]
            while lines and (not lines[0].strip() or
                             SECTION_RE.match(lines[0].strip())):
                lines.pop(0)
            while lines and not lines[-1].strip():
                lines.pop()
            out[key] = lines
        return out
    return parse_sections(path)


def parse_plan(lines, warnings):
    """EXPLAIN lines -> (root Node or None, is_error, is_timeout)."""
    text = "\n".join(lines)
    is_error = bool(ERROR_RE.search(text))
    is_timeout = any(rx.search(text) for rx in TIMEOUT_RES)
    root, stack = None, []

    def attach(node):
        nonlocal root
        while stack and stack[-1].indent >= node.indent:
            stack.pop()
        if stack:
            parent = stack[-1]
            if node.kind in ("SubPlan", "InitPlan"):
                parent.aux.append(node)
            else:
                parent.children.append(node)
        else:
            root = node
        stack.append(node)

    for line in lines:
        stripped = line.strip()
        if not stripped:
            continue
        if stripped == "QUERY PLAN" or re.match(r"^-+\s*$", stripped) or \
                re.match(r"^\(\d+ rows?\)$", stripped):
            continue
        m = ARROW_RE.match(line)
        if m:
            indent = len(m.group(1))
            node = make_node(m.group(2), indent, warnings, line)
            attach(node)
            continue
        am = AUX_RE.match(stripped)
        if am:
            indent = len(line) - len(line.lstrip())
            node = Node(am.group(1), indent, detail=am.group(2))
            node.raw = line
            attach(node)
            continue
        indent = len(line) - len(line.lstrip())
        if ":" in (stripped.split("(")[0] if "(" in stripped else stripped):
            # property line -> nearest open node with smaller indent
            while stack and stack[-1].indent >= indent:
                stack.pop()
            if not stack:
                warnings.append("orphan property: %r" % stripped)
                continue
            key, _, val = stripped.partition(":")
            stack[-1].props.setdefault(key.strip(), []).append(val.strip())
            continue
        # bare node line (root without arrow, e.g. `Sort  (cost=...)`)
        node = make_node(stripped, indent, warnings, line)
        if node is None:
            warnings.append("unparsed line: %r" % stripped)
            continue
        attach(node)
    return root, is_error, is_timeout


def make_node(body, indent, warnings, line):
    est = {}
    m = COST_RE.search(body)
    if m:
        est = {"cost0": float(m.group(1)), "cost1": float(m.group(2)),
               "rows": int(m.group(3)), "width": int(m.group(4))}
    a = ACTUAL_RE.search(body)
    if a:
        est.update({"at0": float(a.group(1)), "at1": float(a.group(2)),
                    "arows": a.group(3), "loops": a.group(4)})
    text = COST_RE.sub("", body)
    text = ACTUAL_RE.sub("", text).strip()
    parallel = False
    if text.startswith("Parallel "):
        parallel = True
        text = text[len("Parallel "):]
    w = re.match(r"^\(Workers Planned: (\d+)\)\s*", text)
    if w:
        est["workers"] = int(w.group(1))
        text = text[w.end():]
    text = re.sub(r"\s+", " ", text).strip()
    kind, detail = text, ""
    for sk in SCAN_KINDS:
        if text == sk or text.startswith(sk + " on ") or \
                text.startswith(sk + " using "):
            k, idx, tgt = split_scan_norm(text)
            kind = k
            detail = ((idx + " on " + tgt) if idx else ("on " + tgt))
            break
    else:
        jm, jt = split_join_norm(text)
        if jm:
            kind, detail = jm, jt
        elif text in ("Sort", "Unique", "Limit", "Append", "MergeAppend",
                      "Recursive Union", "Result", "ProjectSet", "WindowAgg",
                      "Group", "Memoize", "Materialize", "Hash", "HashSetOp",
                      "SetOp", "LockRows", "Gather", "Gather Merge",
                      "Aggregate", "HashAggregate", "GroupAggregate",
                      "MixedAggregate", "ModifyTable"):
            kind, detail = text, ""
        elif AGG_SUFFIX_RE.match(text):
            kind, detail = AGG_SUFFIX_RE.match(text).groups()
        else:
            kind = text
    if kind not in KNOWN_KINDS:
        warnings.append("unknown node kind: %r" % text)
    node = Node(kind, indent, detail=detail, parallel=parallel)
    node.est = est
    node.raw = line
    return node


def split_scan_norm(text):
    # two-word kinds first ("Index Only", "Bitmap Heap", "Bitmap Index"),
    # then plain "Index Scan using i on t".
    m2 = re.match(r"^((?:Index Only|Bitmap Heap|Bitmap Index|Index)\s+Scan) using (\S+) on (.*)$",
                  text)
    if m2:
        return m2.group(1), m2.group(2), m2.group(3)
    m3 = re.match(r"^((?:Index Only|Bitmap Heap|Bitmap Index|Index|Seq|Tid|Sample|Function|Values|CTE|WorkTable|Foreign|Custom|Subquery)\s+Scan)(?: on (.*))?$",
                  text)
    if m3:
        return m3.group(1), "", (m3.group(2) or "")
    return text, "", ""


def split_join_norm(text):
    """'Hash [Right] [Semi] Join' -> (method, jointype); else (None, '')."""
    if text in JOIN_METHODS:
        return text, "Inner"
    m = re.match(r"^(Nested Loop|Hash|Merge)\s+(.*)\s+Join$", text)
    if m:
        method = {"Nested Loop": "Nested Loop", "Hash": "Hash Join",
                  "Merge": "Merge Join"}[m.group(1)]
        return method, m.group(2).strip() or "Inner"
    return None, ""


# ---------------------------------------------------------------- normalisation

def reassign_aux(root):
    """Move aux SubPlan/InitPlan nodes to the deepest node in their current
    parent's subtree whose props reference them. Indentation alone
    mis-attaches (PG Q11 prints InitPlan at the Sort level while the
    HashAggregate Filter owns it); without this the two InitPlans never meet
    and each reports 'present only on ...'. Fixpoint; moves go strictly
    deeper toward the deepest referrer, so this terminates."""
    for _ in range(6):
        if not _reassign_once(root):
            break


def _reassign_once(node):
    moved = False
    for a in list(node.aux):
        ref = "%s %s" % (a.kind, a.detail)
        best, bestdepth = None, -1

        def find(n, depth):
            nonlocal best, bestdepth
            if n is not a and any(ref in v for vals in n.props.values()
                                  for v in vals):
                if depth > bestdepth:
                    best, bestdepth = n, depth
            for ch in n.children:
                find(ch, depth + 1)
            for x in n.aux:
                if x is not a:
                    find(x, depth + 1)

        find(node, 0)
        if best is not None and best is not node:
            node.aux.remove(a)
            best.aux.append(a)
            moved = True
    for ch in node.children + node.aux:
        if _reassign_once(ch):
            moved = True
    return moved


def collect_tables(root):
    """Base relations from scan targets (drives N4 qualifier stripping).

    Index-scan details read `idx on rel [alias]` (first token is the index,
    not the table); alias-only targets (`on partsupp_1`, goopg's P0-04
    dedup style) contribute their suffix-stripped base as well."""
    tables = set()
    if root is None:
        return tables
    for n in walk(root):
        if n.kind not in SCAN_KINDS or n.kind == "Bitmap Index Scan":
            continue
        det = n.detail
        rel = ""
        if det.startswith("on "):
            parts = det[3:].split()
            rel = parts[0] if parts else ""
        else:
            parts = det.split()
            if "on" in parts:
                i = parts.index("on")
                rel = parts[i + 1] if i + 1 < len(parts) else ""
            elif parts:
                rel = parts[-1]
        rel = rel.split(".")[-1]
        if rel:
            tables.add(rel.lower())
            m = re.match(r"^(.*)_\d+$", rel)
            if m:
                tables.add(m.group(1).lower())
    return tables


def strip_casts(text):
    text = CAST_PHRASE_RE.sub("", text)
    return CAST_RE.sub("", text)


def normalise_expr(text, tables):
    text = strip_casts(text)
    text = text.replace("!~~*", " NOT ILIKE ").replace("~~*", " ILIKE ")
    text = text.replace("!~~", " NOT LIKE ").replace("~~", " LIKE ")
    text = re.sub(r"\bSubPlan \d+\b", "SubPlan", text)
    text = re.sub(r"\bInitPlan \d+\b", "InitPlan", text)

    def qual(m):
        q, c = m.group(1), m.group(2)
        qb = re.sub(r"_\d+$", "", q)
        if qb.lower() in tables:
            return c
        return m.group(0)
    text = QUALIFIER_RE.sub(qual, text)

    def suf(m):
        base = m.group(1)
        if base.lower() in tables:
            return base
        return m.group(0)
    text = SUFFIX_RE.sub(suf, text)
    text = re.sub(r"\s+", " ", text).strip()
    return text


def is_vacuous(val):
    return val.strip().strip("()").strip().lower() == "true"


def qual_signature(text, tables):
    """(sorted columns incl SubPlan/InitPlan/$N, sorted ops) for cond props."""
    norm = normalise_expr(text, tables)
    no_lit = LITERAL_RE.sub("?", norm)
    no_lit = NUMBER_RE.sub("?", no_lit)
    ops = sorted(OP_RE.findall(no_lit.upper()))
    cols = sorted(
        t for t in IDENT_RE.findall(no_lit)
        if t.upper() not in KEYWORDS and t != "?" and not t.startswith("$")
    )
    params = sorted(set(re.findall(r"\$[0-9]+", no_lit)))
    structs = []
    for marker in ("SubPlan", "InitPlan"):
        structs += [marker] * len(re.findall(r"\b%s\b" % marker, no_lit))
    return (tuple(cols), tuple(ops), tuple(params), tuple(structs))


COND_PROPS = ("Filter", "Index Cond", "Join Filter", "Hash Cond", "Merge Cond",
              "Recheck Cond")
KEY_PROPS = ("Sort Key", "Group Key")


def normalise_tree(root, tables, applied):
    """In-place: N2 Hash splice, N3 vacuous-drop, N7 Right-join canonicalise."""
    if root is None:
        return None
    # post-order so children are normalised first
    new_children = []
    for ch in root.children:
        rep = normalise_tree(ch, tables, applied)
        if rep is not None:
            new_children.append(rep)
    root.children = new_children
    root.aux = [a for a in (normalise_tree(a, tables, applied)
                            for a in root.aux) if a is not None]
    if root.kind == "Hash":
        applied.add("strip-pg-Hash")
        if len(root.children) == 1:
            return root.children[0]
        return root  # degenerate: keep, will warn downstream
    # N3
    for key in ("Filter", "Join Filter"):
        if key in root.props:
            kept = [v for v in root.props[key]
                    if not is_vacuous(normalise_expr(v, tables))]
            if len(kept) != len(root.props[key]):
                applied.add("drop-true-filter")
            if kept:
                root.props[key] = kept
            else:
                del root.props[key]
    # N7
    if root.kind in JOIN_METHODS and root.detail.startswith("Right"):
        root.detail = "Left" + root.detail[len("Right"):]
        root.children.reverse()
        applied.add("canonicalise-right-join")
    if root.kind in JOIN_METHODS and root.detail in ("Semi", "Anti"):
        root.detail = "Left " + root.detail
        applied.add("canonicalise-bare-semi-anti")
    return root


def walk(root):
    yield root
    for ch in root.children:
        yield from walk(ch)
    for a in root.aux:
        yield from walk(a)


def leaves(root, tables=None):
    """Multiset of base relations under root, excluding aux subplan levels."""
    out = []

    def rec(n):
        if n.kind in ("SubPlan", "InitPlan"):
            return  # separate query level
        if n.kind in SCAN_KINDS and n.kind != "Bitmap Index Scan":
            key = scan_key(n, tables or {})
            if key[-1]:
                out.append(key[-1].lower())
        for ch in n.children:
            rec(ch)
        for a in n.aux:
            rec(a)

    rec(root)
    return sorted(out)


def is_parameterised(node, tables, own_leaves):
    """Scan references outer rels / $N / SubPlan => parameterised inner."""
    own = set(own_leaves)
    for key in COND_PROPS:
        for val in node.props.get(key, []):
            norm = normalise_expr(val, tables)
            if re.search(r"\$[0-9]+", norm):
                return True
            if re.search(r"\b(SubPlan|InitPlan)\b", norm):
                return True
            for m in QUALIFIER_RE.finditer(norm):
                qb = re.sub(r"_\d+$", "", m.group(1)).lower()
                if qb in tables and qb not in own:
                    return True
        # unqualified col belonging to another known table? detectable via
        # prefix convention only for scans: skip (conservative).
    return False


# ---------------------------------------------------------------- comparison

class Ctx:
    def __init__(self, tables):
        self.cats = set()
        self.tables = tables
        self.divergences = []  # (path, category, detail)
        self.unknown = []
        self.groot = None
        self.proot = None
        self.aux_done = set()  # (id, id) pairs already cross-compared


def node_path(path):
    return "/" + "/".join(path)


def cmp_trees(g, p, ctx, path, reordered=False):
    """Positional recursive comparison. Records categories + divergences.

    reordered: an ancestor join pair had different leaf sets, so pairings
    below are positional luck -- scan/cond hits are skipped there (the
    ancestor already carries join-order/parameterisation; the kind-presence
    rule covers true one-sided access-path diffs)."""
    present = g if g is not None else p
    here = node_path(path + [present.kind if present is not None else "?"])
    if g is None or p is None:
        extra = p if g is None else g
        cat = extra_category(extra)
        if cat:
            ctx.cats.add(cat)
            ctx.divergences.append((here, cat, "present only on %s side: %s" %
                                    ("PG" if g is None else "goopg",
                                     describe(extra))))
        return
    if g.kind != p.kind:
        # luck-paired scans under a reordered parent: nothing comparable
        # (same-kind luck pairs are gated in cmp_same; the kind-presence
        # rule covers true one-sided access-path diffs).
        if reordered and g.kind in SCAN_KINDS and p.kind in SCAN_KINDS and \
                scan_key(g, ctx.tables)[-1] != scan_key(p, ctx.tables)[-1]:
            return
        cat = mismatch_category(g, p)
        ctx.cats.add(cat)
        detail = "%s vs %s" % (describe(g), describe(p))
        # Sort-vs-aggregate crossings carry two signals: the sort placement
        # AND the grouping implementation. Record aggregation-strategy too
        # when the groupings visible on either side differ (Q4/Q7 witness:
        # positional walk alone reports only sort-strategy).
        if ((g.kind in AGG_KINDS and p.kind == "Sort") or
                (p.kind in AGG_KINDS and g.kind == "Sort")):
            ga = g.kind if g.kind in AGG_KINDS else (
                g.children[0].kind if len(g.children) == 1 else None)
            pa = p.kind if p.kind in AGG_KINDS else (
                p.children[0].kind if len(p.children) == 1 else None)
            if ga in AGG_KINDS and pa in AGG_KINDS and ga != pa:
                ctx.cats.add("aggregation-strategy")
                ctx.divergences.append((here, "aggregation-strategy",
                                        "grouping %s vs %s" % (ga, pa)))
        # join-method swaps recurse positionally (outer<->outer);
        # check for swapped children first.
        if g.kind in JOIN_METHODS and p.kind in JOIN_METHODS:
            if swapped_match(g, p, ctx.tables):
                ctx.cats.add("join-order")
                ctx.divergences.append((here, "join-order",
                                        "swapped children: " + detail))
                ctx.cats.add("join-method")
                ctx.divergences.append((here, "join-method", detail))
                cmp_children(g.children, list(reversed(p.children)), ctx,
                             path + [(g.kind + " (swapped)")])
                cmp_aux(g, p, ctx, path)
                return
            ctx.divergences.append((here, cat, detail))
        else:
            ctx.divergences.append((here, cat, detail))
        # leaf-set signal even across kinds (bushiness vs scan etc.)
        leaf_signal(g, p, ctx, here)
        cmp_children(g.children, p.children, ctx, path + [g.kind],
                     reordered or len(g.children) != len(p.children))
        cmp_aux(g, p, ctx, path)
        return
    # same kind
    leaf_eq = True
    if g.kind in JOIN_METHODS:
        leaf_eq = leaves(g, ctx.tables) == leaves(p, ctx.tables)
    cmp_same(g, p, ctx, here, reordered=reordered,
             skip_conds=(g.kind in JOIN_METHODS and not leaf_eq))
    if g.kind in JOIN_METHODS:
        leaf_signal(g, p, ctx, here)
        gch, pch = g.children, p.children
        if len(gch) == len(pch) == 2 and not children_match(gch, pch, ctx) \
                and children_match(gch, list(reversed(pch)), ctx):
            ctx.cats.add("join-order")
            ctx.divergences.append((here, "join-order",
                                    "swapped children: %s" % describe(g)))
            pch = list(reversed(pch))
        cmp_children(gch, pch, ctx, path + [g.kind],
                     reordered or not leaf_eq)
    else:
        cmp_children(g.children, p.children, ctx, path + [g.kind],
                     reordered)
    cmp_aux(g, p, ctx, path)


def children_match(gch, pch, ctx):
    if len(gch) != len(pch):
        return False
    for a, b in zip(gch, pch):
        if a.kind != b.kind:
            return False
        if a.kind in SCAN_KINDS and scan_key(a, ctx.tables) != scan_key(b, ctx.tables):
            return False
    return True


def swapped_match(g, p, tables):
    if len(g.children) != len(p.children) or len(g.children) != 2:
        return False
    return leaves(g.children[0], tables) == leaves(p.children[1], tables) and \
        leaves(g.children[1], tables) == leaves(p.children[0], tables)


def cmp_children(gch, pch, ctx, path, reordered=False):
    for i, (a, b) in enumerate(zip(gch, pch)):
        cmp_trees(a, b, ctx, path + ["c%d" % i], reordered)
    if len(gch) > len(pch):
        for a in gch[len(pch):]:
            cmp_trees(a, None, ctx, path + ["c-extra-goopg"], reordered)
    elif len(pch) > len(gch):
        for b in pch[len(gch):]:
            cmp_trees(None, b, ctx, path + ["c-extra-pg"], reordered)


def find_aux(root, name):
    for n in walk(root):
        for a in n.aux:
            if (a.kind, a.detail) == name:
                return a
    return None


def cmp_aux(g, p, ctx, path):
    ga = {(a.kind, a.detail): a for a in g.aux}
    pa = {(a.kind, a.detail): a for a in p.aux}
    for name in sorted(set(ga) | set(pa)):
        label = "%s%s" % (name[0], name[1])
        if name in ga and name in pa:
            cmp_trees(ga[name], pa[name], ctx, path + [label])
        else:
            ctx.cats.add("parameterisation")
            present, other = (ga[name], ctx.proot) if name in ga else \
                (pa[name], ctx.groot)
            counterpart = find_aux(other, name) if other is not None else None
            # symmetric key: whichever side is "present" here, the pair is
            # the same (Q20 meets its SubPlan from both levels).
            key = tuple(sorted((id(present), id(counterpart)))) \
                if counterpart is not None else None
            if counterpart is not None and key not in ctx.aux_done:
                ctx.aux_done.add(key)
                ctx.divergences.append(
                    (node_path(path + [label]), "parameterisation",
                     "SubPlan/InitPlan at different levels (%s side here)" %
                     ("goopg" if name in ga else "PG")))
                cmp_trees(present, counterpart, ctx, path + [label])
            elif counterpart is not None:
                ctx.divergences.append(
                    (node_path(path + [label]), "parameterisation",
                     "SubPlan/InitPlan already compared at another level"))
            else:
                ctx.divergences.append(
                    (node_path(path + [label]), "parameterisation",
                     "SubPlan/InitPlan present only on %s side" %
                     ("goopg" if name in ga else "PG")))


def cmp_same(g, p, ctx, here, reordered=False, skip_conds=False):
    tables = ctx.tables
    if g.parallel != p.parallel:
        ctx.cats.add("parallelism")
        ctx.divergences.append((here, "parallelism", "Parallel flag differs"))
    if g.est.get("workers") != p.est.get("workers") and \
            (g.est.get("workers") or p.est.get("workers")):
        ctx.cats.add("parallelism")
        ctx.divergences.append((here, "parallelism", "Workers Planned differs"))
    if g.kind in SCAN_KINDS:
        gk, pk = scan_key(g, tables), scan_key(p, tables)
        if reordered and gk[-1] != pk[-1]:
            return  # different relations paired by luck under a reordered
            # parent: nothing comparable here (the ancestor carries the
            # order signal, presence the access-path signal). Scans are
            # leaves, so returning skips type/param/cond compares alike.
        if gk != pk:
            ctx.cats.add("scan-type")
            ctx.divergences.append((here, "scan-type",
                                    "%s vs %s" % (describe(g), describe(p))))
        gl = leaves(g, tables)
        if is_parameterised(g, tables, gl) != is_parameterised(
                p, tables, leaves(p, tables)):
            ctx.cats.add("parameterisation")
            ctx.divergences.append((here, "parameterisation",
                                    "parameterised inner differs"))
    if g.kind in JOIN_METHODS:
        if norm_jointype(g.detail) != norm_jointype(p.detail):
            ctx.cats.add("join-method")
            ctx.divergences.append((here, "join-method",
                                    "join type %s vs %s" % (g.detail, p.detail)))
    # cond props: signature compare over multisets (a node may carry
    # several Filter lines). On join nodes Filter+Join Filter merge into one
    # bucket: PG prints join quals as Join Filter where goopg prints Filter,
    # a label choice, not placement.
    pairs = []
    if g.kind in JOIN_METHODS:
        pairs.append(("JoinQual",
                      g.props.get("Filter", []) + g.props.get("Join Filter", []),
                      p.props.get("Filter", []) + p.props.get("Join Filter", [])))
        rest = [k for k in COND_PROPS if k not in ("Filter", "Join Filter")]
    else:
        rest = list(COND_PROPS)
    for key in rest:
        pairs.append((key, g.props.get(key, []), p.props.get(key, [])))
    for key, gvs, pvs in pairs:
        if not gvs and not pvs:
            continue
        if skip_conds:
            continue  # leaf sets differ: conds trivially differ; the
            # join-order/parameterisation signal at this node covers it.
        if not gvs or not pvs:
            cat = key_category(key, " ".join(gvs + pvs), tables)
            ctx.cats.add(cat)
            ctx.divergences.append((here, cat, "%s present only on %s side" %
                                    (key, "goopg" if not pvs else "PG")))
            continue
        if sorted(qual_signature(v, tables) for v in gvs) != \
                sorted(qual_signature(v, tables) for v in pvs):
            cat = key_category(key, " ".join(gvs + pvs), tables)
            ctx.cats.add(cat)
            ctx.divergences.append((here, cat, "%s signature differs" % key))
    # sort/group keys: verdict-neutral rendering
    for key in KEY_PROPS:
        gvs, pvs = g.props.get(key, []), p.props.get(key, [])
        if not gvs and not pvs:
            continue
        if not gvs or not pvs or \
                sorted(qual_signature(v, tables) for v in gvs) != \
                sorted(qual_signature(v, tables) for v in pvs):
            ctx.cats.add("rendering")
            ctx.divergences.append((here, "rendering",
                                    "%s text differs" % key))


def key_category(key, text, tables):
    # Hash/Merge Cond define which keys pair: a mismatch is join-order
    # (different key pairing), unless a subplan level is involved.
    if key in ("Hash Cond", "Merge Cond"):
        if cond_category(text, tables) == "parameterisation":
            return "parameterisation"
        return "join-order"
    return cond_category(text, tables)


def cond_category(text, tables):
    norm = normalise_expr(text or "", tables)
    if re.search(r"\$[0-9]+|\b(SubPlan|InitPlan)\b", norm):
        return "parameterisation"
    return "qual-placement"


def norm_jointype(detail):
    return detail.strip() if detail.strip() else "Inner"


def scan_key(n, tables):
    """(kind, index, relation) identity for scan nodes, normalised (N4)."""
    if n.kind == "Bitmap Index Scan":
        return (n.kind, normalise_expr(n.detail, tables))
    d = n.detail
    idx, rest = "", d
    if " on " in d and not d.startswith("on "):
        # "idx on rel [alias]"
        idx, _, rest = d.partition(" on ")
    elif d.startswith("on "):
        rest = d[3:]
    # rest: "rel [alias]"
    rel = (rest.split() or [""])[0].split(".")[-1]
    base = re.sub(r"_\d+$", "", rel)
    if base.lower() in tables:
        rel = base
    return (n.kind, idx.strip(), rel)


def leaf_signal(g, p, ctx, here):
    """Join leaf-set comparison: join-order, or parameterisation when a
    subplan level is involved."""
    if g.kind not in JOIN_METHODS or p.kind not in JOIN_METHODS:
        return
    gl, pl = leaves(g, ctx.tables), leaves(p, ctx.tables)
    if gl == pl:
        return
    sub = any(a.kind in ("SubPlan", "InitPlan") for a in walk(g)) or \
        any(a.kind in ("SubPlan", "InitPlan") for a in walk(p))
    cat = "parameterisation" if sub else "join-order"
    ctx.cats.add(cat)
    ctx.divergences.append((here, cat, "leaf sets differ: %s vs %s" %
                            (" ".join(gl) or "-", " ".join(pl) or "-")))


def mismatch_category(g, p):
    kinds = {g.kind, p.kind}
    if kinds <= set(JOIN_METHODS) or (
            g.kind in JOIN_METHODS and p.kind in JOIN_METHODS):
        return "join-method"
    if kinds <= set(SCAN_KINDS) or (
            g.kind in SCAN_KINDS and p.kind in SCAN_KINDS):
        return "scan-type"
    if kinds <= set(AGG_KINDS) or (
            g.kind in AGG_KINDS and p.kind in AGG_KINDS):
        return "aggregation-strategy"
    if kinds == {"Sort", "Incremental Sort"}:
        return "sort-strategy"
    if "Sort" in kinds or "Incremental Sort" in kinds:
        return "sort-strategy"
    if kinds <= {"Aggregate", "HashAggregate", "GroupAggregate", "Unique",
                 "Group"} or "Unique" in kinds or "Group" in kinds:
        return "aggregation-strategy"
    if kinds & {"Gather", "Gather Merge"}:
        return "parallelism"
    if "Memoize" in kinds:
        return "parameterisation"
    if "Limit" in kinds:
        return "sort-strategy"
    if kinds & set(JOIN_METHODS):
        return "join-order"
    if (g.kind in SCAN_KINDS) != (p.kind in SCAN_KINDS):
        return "scan-type"
    return "join-order"


def extra_category(n):
    if n.kind == "Sort":
        return "sort-strategy"
    if n.kind == "Limit":
        return "sort-strategy"
    if n.kind in ("SubPlan", "InitPlan"):
        return "parameterisation"
    if n.kind == "Memoize":
        return "parameterisation"
    if n.kind == "Unique":
        return "aggregation-strategy"
    if n.kind in AGG_KINDS:
        return "aggregation-strategy"
    if n.kind in ("Materialize",):
        return None  # covered by the MISSING-NODE verdict
    if n.kind in ("Gather", "Gather Merge"):
        return "parallelism"
    return "join-order"


def walk_main(root):
    yield root
    for ch in root.children:
        yield from walk_main(ch)


def apply_presence(groots, proots, ctx, key):
    """Kind-presence complement: a node kind used on only one side never
    meets its counterpart in the positional walk (Q9's Nested Loop).
    Main tree only: aux subplan levels compare via cmp_aux."""
    gkinds = {n.kind for n in walk_main(groots)}
    pkinds = {n.kind for n in walk_main(proots)}
    for kind in sorted(gkinds ^ pkinds):
        cat = presence_category(kind)
        if cat and cat not in ctx.cats:
            ctx.divergences.append((key, cat, "%s present only on %s side" %
                                    (kind, "goopg" if kind in gkinds
                                     else "PG")))
        if cat:
            ctx.cats.add(cat)


def presence_category(kind):
    """Category for a node kind used on only one side of a query pair."""
    if kind in SCAN_KINDS:
        return "scan-type"
    if kind in JOIN_METHODS:
        return "join-method"
    if kind in AGG_KINDS or kind in ("Unique", "Group"):
        return "aggregation-strategy"
    if kind == "Sort":
        return "sort-strategy"
    if kind == "Limit":
        return "sort-strategy"
    if kind in ("SubPlan", "InitPlan", "Memoize"):
        return "parameterisation"
    if kind in ("Gather", "Gather Merge"):
        return "parallelism"
    return None  # Materialize/Incremental Sort (verdict covers), Hash
    # (stripped), Result/Append/... (positional walk covers)


def describe(n):
    d = n.kind
    if n.parallel:
        d = "Parallel " + d
    if n.detail:
        d += " " + n.detail
    return d


def fmt_est(est):
    if not est or "cost0" not in est:
        return "n/a"
    s = "cost=%.2f..%.2f rows=%d width=%d" % (
        est["cost0"], est["cost1"], est["rows"], est["width"])
    if "at0" in est:
        s += " actual=%.3f..%.3f rows=%s loops=%s" % (
            est["at0"], est["at1"], est["arows"], est["loops"])
    if "workers" in est:
        s += " workers=%d" % est["workers"]
    return s


def render_tree(n, tables, depth=0):
    lines = ["  " * depth + describe(n) +
             ("  (%s)" % fmt_est(n.est) if n.est else "")]
    for key in sorted(n.props):
        for val in n.props[key]:
            lines.append("  " * depth + "  %s: %s" % (key, normalise_expr(
                val, tables)))
    for ch in n.children:
        lines += render_tree(ch, tables, depth + 1)
    for a in n.aux:
        lines.append("  " * depth + "aux:")
        lines += render_tree(a, tables, depth + 1)
    return lines


def compare_query(key, glines, plines):
    """(verdict, categories, divergences, goopg_est, pg_est, notes, unknowns)."""
    warnings, notes = [], []
    groots, gerr, gto = parse_plan(glines or [], warnings)
    proots, perr, pto = parse_plan(plines if plines is not None else [],
                                   warnings)
    unknowns = sorted({w for w in warnings if w.startswith("unknown node")})
    if glines is None or plines is None:
        missing = "goopg" if glines is None else "PG"
        return ("ERROR", set(), [], {}, {}, ["missing %s section" % missing],
                unknowns)
    gest = groots.est if groots is not None else {}
    pest = proots.est if proots is not None else {}
    if gerr or perr:
        if gto or pto:
            return ("TIMEOUT", set(), [], gest, pest,
                    ["timeout marker in %s block" %
                     ("both" if (gto and pto) else
                      ("goopg" if gto else "PG"))], unknowns)
        return ("ERROR", set(), [], gest, pest,
                ["ERROR marker in %s block" %
                 ("both" if (gerr and perr) else
                  ("goopg" if gerr else "PG"))], unknowns)
    if gto or pto:
        return ("TIMEOUT", set(), [], gest, pest,
                ["timeout marker in %s block" %
                 ("both" if (gto and pto) else
                  ("goopg" if gto else "PG"))], unknowns)
    if groots is None or proots is None:
        return ("ERROR", set(), [], gest, pest,
                ["unparseable %s plan" %
                 ("goopg" if groots is None else "PG")], unknowns)
    tables = collect_tables(groots) | collect_tables(proots)
    for r in (groots, proots):
        if r is not None:
            reassign_aux(r)
    applied = set()
    groots = normalise_tree(groots, tables, applied)
    proots = normalise_tree(proots, tables, applied)
    notes = ["normalisation: %s" % ",".join(sorted(applied))] if applied else []
    pg_kinds = {n.kind for n in walk(proots)}
    if unknowns or (pg_kinds & set(GOOPG_UNEMITTABLE)):
        missing_kinds = sorted(pg_kinds & set(GOOPG_UNEMITTABLE))
        ctx = Ctx(tables)
        ctx.groot, ctx.proot = groots, proots
        cmp_trees(groots, proots, ctx, [key])
        apply_presence(groots, proots, ctx, key)
        if unknowns:
            notes.append("unknown node kinds: %s" % "; ".join(unknowns))
        if missing_kinds:
            notes.append("PG-only node kinds: %s" % ",".join(missing_kinds))
            ctx.divergences.append((key, "MISSING-NODE",
                                    "PG-only kinds: %s" % ",".join(
                                        missing_kinds)))
        return ("MISSING-NODE", set(sorted(
            ctx.cats, key=CATEGORIES.index)), ctx.divergences, gest, pest,
            notes, unknowns)
    ctx = Ctx(tables)
    ctx.groot, ctx.proot = groots, proots
    cmp_trees(groots, proots, ctx, [key])
    apply_presence(groots, proots, ctx, key)
    cats = set(sorted(ctx.cats, key=CATEGORIES.index))
    if unknowns:
        notes.append("unknown node kinds: %s" % "; ".join(unknowns))
    verdict = "MATCH" if not (cats - {"rendering"}) else "SHAPE-DIFF"
    return verdict, cats, ctx.divergences, gest, pest, notes, unknowns


def run_corpus(goopg_path, pg_path):
    goopg = parse_sections(goopg_path)
    pg = load_pg(pg_path)
    results = {}
    for key in sorted(set(goopg) | set(pg)):
        results[key] = compare_query(key, goopg.get(key), pg.get(key))
    return results


def print_report(results, verbose=False):
    counts = {v: 0 for v in VERDICTS}
    catcounts = {c: 0 for c in CATEGORIES}
    for key in sorted(results):
        verdict, cats, divs, gest, pest, notes, unknowns = results[key]
        counts[verdict] += 1
        for c in cats:
            catcounts[c] += 1
        print("%s %s [%s] estimates(goopg %s | pg %s)" % (
            key, verdict, ",".join(sorted(cats, key=CATEGORIES.index)),
            fmt_est(gest), fmt_est(pest)))
        for n in notes:
            print("    note: %s" % n)
        if verbose and (verdict != "MATCH" or unknowns):
            for path, cat, detail in divs[:20]:
                print("    %s %s: %s" % (path, cat, detail))
            if len(divs) > 20:
                print("    ... and %d more divergences" % (len(divs) - 20))
    n = len(results)
    print("PLAN-PARITY: queries=%d %s" % (
        n, " ".join("%s=%d" % (v.lower().replace("-", ""), counts[v])
                    for v in VERDICTS)))
    print("CATEGORIES: %s" % " ".join("%s=%d" % (c, catcounts[c])
                                      for c in CATEGORIES))
    print("NORMALISATION: N1 estimates-to-side-column, N2 strip-PG-Hash, "
          "N3 drop-true-filter, N4 alias/suffix-canonicalisation, "
          "N5 cast/operator-rendering, N6 qual/key-signatures, "
          "N7 right-join-canonicalisation")
    return counts, catcounts


# ---------------------------------------------------------------- self-test

SELF_TESTS = [
    {
        "name": "MATCH identical",
        "goopg": [
            "Sort  (cost=10.00..10.10 rows=7 width=60)",
            "  Sort Key: l_shipmode",
            "  ->  Seq Scan on lineitem  (cost=0.00..5.00 rows=100 width=550)",
            "        Filter: (l_shipdate < '1995-01-01'::date)",
        ],
        "pg": [
            "Sort  (cost=99.00..99.10 rows=7 width=12)",
            "  Sort Key: l_shipmode",
            "  ->  Seq Scan on lineitem  (cost=0.00..50.00 rows=900 width=12)",
            "        Filter: (l_shipdate < '1995-01-01 00:00:00'::timestamp without time zone)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
    {
        "name": "SHAPE-DIFF join-order (swapped children)",
        "goopg": [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
        ],
        "pg": [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (b.x = a.x)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"join-order"},
    },
    {
        "name": "SHAPE-DIFF scan-type",
        "goopg": [
            "Seq Scan on lineitem  (cost=0.00..5.00 rows=100 width=550)",
        ],
        "pg": [
            "Index Scan using idx_lineitem_orderkey_fkidx on lineitem  (cost=0.00..5.00 rows=100 width=18)",
            "  Index Cond: (l_orderkey = 5)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"scan-type"},
    },
    {
        "name": "PG standalone Hash stripped: MATCH not MISSING-NODE",
        "goopg": [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
        ],
        "pg": [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Hash  (cost=5.00..5.00 rows=5 width=5)",
            "        ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
    {
        "name": "rendering-only alias/suffix: MATCH not SHAPE-DIFF",
        "goopg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  Filter: (n_nationkey = s_nationkey)",
            "  ->  Seq Scan on nation_1  (cost=0.00..1.00 rows=25 width=10)",
            "  ->  Seq Scan on supplier  (cost=0.00..1.00 rows=10 width=10)",
        ],
        "pg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  Join Filter: (nation_1.n_nationkey = supplier.s_nationkey)",
            "  ->  Seq Scan on nation nation_1  (cost=0.00..1.00 rows=25 width=10)",
            "  ->  Seq Scan on supplier  (cost=0.00..1.00 rows=10 width=10)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
    {
        "name": "SHAPE-DIFF join-method",
        "goopg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  ->  Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "pg": [
            "Hash Join  (cost=1.00..5.00 rows=2 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"join-method"},
    },
    {
        "name": "SHAPE-DIFF aggregation-strategy",
        "goopg": [
            "HashAggregate  (cost=10.00..11.00 rows=5 width=10)",
            "  Group Key: n_name",
            "  ->  Seq Scan on nation  (cost=0.00..1.00 rows=25 width=10)",
        ],
        "pg": [
            "GroupAggregate  (cost=10.00..11.00 rows=5 width=10)",
            "  Group Key: n_name",
            "  ->  Sort  (cost=9.00..9.50 rows=25 width=10)",
            "        Sort Key: n_name",
            "        ->  Seq Scan on nation  (cost=0.00..1.00 rows=25 width=10)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"aggregation-strategy", "sort-strategy"},
    },
    {
        "name": "SHAPE-DIFF parameterisation (SubPlan kept vs decorrelated)",
        "goopg": [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
        ],
        "pg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  Join Filter: (b.y > (SubPlan 1))",
            "  ->  Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..1.00 rows=5 width=5)",
            "  SubPlan 1",
            "    ->  Aggregate  (cost=0.50..0.51 rows=1 width=4)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"join-method", "parameterisation"},
    },
    {
        "name": "MISSING-NODE PG Materialize",
        "goopg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  Join Filter: (n_regionkey = r_regionkey)",
            "  ->  Seq Scan on nation  (cost=0.00..1.00 rows=25 width=10)",
            "  ->  Seq Scan on region  (cost=0.00..1.00 rows=5 width=10)",
        ],
        "pg": [
            "Nested Loop  (cost=1.00..5.00 rows=2 width=10)",
            "  Join Filter: (n_regionkey = r_regionkey)",
            "  ->  Seq Scan on nation  (cost=0.00..1.00 rows=25 width=10)",
            "  ->  Materialize  (cost=0.00..1.00 rows=5 width=10)",
            "        ->  Seq Scan on region  (cost=0.00..1.00 rows=5 width=10)",
        ],
        "verdict": "MISSING-NODE",
        "cats": set(),
    },
    {
        "name": "ERROR block",
        "goopg": ["ERROR: relation does not exist"],
        "pg": [
            "Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "verdict": "ERROR",
        "cats": set(),
    },
    {
        "name": "TIMEOUT block",
        "goopg": [
            "Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "pg": ["ERROR: canceling statement due to statement timeout"],
        "verdict": "TIMEOUT",
        "cats": set(),
    },
    {
        "name": "missing goopg section is ERROR",
        "goopg": None,
        "pg": [
            "Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "verdict": "ERROR",
        "cats": set(),
    },
    {
        "name": "missing PG section is ERROR",
        "goopg": [
            "Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "pg": None,
        "verdict": "ERROR",
        "cats": set(),
    },
    {
        "name": "estimates never affect verdict (same shape, wild costs)",
        "goopg": [
            "Aggregate  (cost=60042.74..60042.75 rows=1 width=32)",
            "  ->  Seq Scan on lineitem  (cost=0.00..60036.70 rows=2415 width=550)",
        ],
        "pg": [
            "Aggregate  (cost=264897.63..264897.64 rows=1 width=32)",
            "  ->  Seq Scan on lineitem  (cost=0.00..264331.51 rows=113224 width=12)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
    {
        "name": "Right-join canonicalisation (Q13 witness)",
        "goopg": [
            "Hash Left Join  (cost=0.25..10.00 rows=100 width=10)",
            "  Hash Cond: (customer.c_custkey = orders.o_custkey)",
            "  ->  Index Only Scan using customer_pk on customer  (cost=0.25..3.00 rows=150 width=6)",
            "  ->  Seq Scan on orders  (cost=0.00..5.00 rows=900 width=12)",
        ],
        "pg": [
            "Hash Right Join  (cost=0.25..10.00 rows=100 width=10)",
            "  Hash Cond: (orders.o_custkey = customer.c_custkey)",
            "  ->  Seq Scan on orders  (cost=0.00..5.00 rows=900 width=12)",
            "  ->  Hash  (cost=3.00..3.00 rows=150 width=6)",
            "        ->  Index Only Scan using customer_pk on customer  (cost=0.25..3.00 rows=150 width=6)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
    {
        "name": "SHAPE-DIFF aggregation-strategy (grouping sets: goopg HashAggregate vs PG MixedAggregate)",
        "goopg": [
            "HashAggregate (2 keys, 3 grouping sets)  (cost=1.00..2.00 rows=3 width=8)",
            "  ->  Seq Scan on t  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "pg": [
            "MixedAggregate  (cost=1.00..2.00 rows=3 width=8)",
            "  Hash Key: t.a, t.b",
            "  Group Key: ()",
            "  ->  Seq Scan on t  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "verdict": "SHAPE-DIFF",
        "cats": {"aggregation-strategy"},
    },
    {
        "name": "MATCH grouping sets: goopg HashAggregate suffix vs PG HashAggregate",
        "goopg": [
            "HashAggregate (2 keys, 3 grouping sets)  (cost=1.00..2.00 rows=3 width=8)",
            "  ->  Seq Scan on t  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "pg": [
            "HashAggregate  (cost=1.00..2.00 rows=3 width=8)",
            "  Hash Key: t.a, t.b",
            "  Hash Key: t.a",
            "  Hash Key: ()",
            "  ->  Seq Scan on t  (cost=0.00..1.00 rows=5 width=5)",
        ],
        "verdict": "MATCH",
        "cats": set(),
    },
]


def run_self_test():
    failures = []
    for t in SELF_TESTS:
        verdict, cats, divs, gest, pest, notes, unknowns = compare_query(
            "T", t["goopg"], t["pg"])
        want_cats = set(t["cats"])
        if verdict != t["verdict"]:
            failures.append("%s: verdict %s, want %s (%s)" % (
                t["name"], verdict, t["verdict"],
                "; ".join("%s: %s" % (c, d) for _, c, d in divs)))
        elif not want_cats <= cats:
            failures.append("%s: cats %s missing %s" % (
                t["name"], sorted(cats), sorted(want_cats - cats)))
        elif verdict == "MATCH" and (cats - {"rendering"}):
            failures.append("%s: MATCH with shape cats %s" % (
                t["name"], sorted(cats)))
    return failures


def main(argv=None):
    ap = argparse.ArgumentParser(
        description="goopg-vs-PostgreSQL plan-parity diff (report-only).")
    ap.add_argument("goopg", nargs="?", help="goopg plans file (=== sections)")
    ap.add_argument("pg", nargs="?", help="PG fixtures dir or sections file")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--self-test", action="store_true",
                    help="run built-in unit checks, exit nonzero on failure")
    args = ap.parse_args(argv)
    if args.self_test:
        failures = run_self_test()
        for f in failures:
            print("FAIL: %s" % f)
        print("self-test: %d/%d passed" % (
            len(SELF_TESTS) - len(failures), len(SELF_TESTS)))
        return 1 if failures else 0
    if not args.goopg or not args.pg:
        ap.error("GOOPG_PLANS and PG are required (unless --self-test)")
    try:
        results = run_corpus(args.goopg, args.pg)
    except OSError as exc:
        print("# plan-parity: unavailable (%s)" % exc)
        return 0
    print("# plan-parity: %s -> %s" % (args.goopg, args.pg))
    print_report(results, verbose=args.verbose)
    return 0


if __name__ == "__main__":
    sys.exit(main())