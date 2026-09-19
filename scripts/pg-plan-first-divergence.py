#!/usr/bin/env python3
"""pg-plan-first-divergence.py — first-divergence census (M0144-0002,
03-forward-plan §1).

Sibling of scripts/pg-plan-parity-diff.py (imported for parsing,
normalisation and the compare primitives). Where the differ tags a divergent
query with 4-7 of nine non-exclusive categories, this tool emits ONE
mutually-exclusive record per divergent query: the first node pair, in PG's
plan-order (pre-order DFS of the normalised main tree, stream children
before SubPlan/InitPlan aux blocks — the order PG's EXPLAIN prints), where
the two trees diverge:

    (parent kind, PG child kind, goopg child kind, plan depth, category)

"A subtree that diverges at node 3 cannot meaningfully be compared at node
5" — the walk stops at the first divergence instead of tallying downstream
consequences, so the output table ranks DECISION POINTS (what a fix targets)
rather than mechanism classes.

Alignment rules mirror cmp_trees/cmp_same so the record lands where the
differ would flag first:

  - children pair positionally; a 2-child join whose straight pairing fails
    children_match but whose swapped pairing matches is a swapped-children
    divergence (join-order) recorded AT the join and not descended
  - extra children on either side are a presence divergence
  - same-kind checks run in cmp_same's order: Parallel flag / Workers
    Planned, scan_key (scan-type) and parameterised-inner (scans), join
    type, join leaf-set (subplan involvement flips it to parameterisation),
    cond-prop signatures (key_category), Sort/Group Key text. KEY_PROPS
    ("rendering") diffs are verdict-neutral in the differ, so they do NOT
    count as a census divergence — a query whose only difference is Sort
    Key rendering is a census match.
  - aux (SubPlan/InitPlan) blocks pair by (kind, detail) name after
    reassign_aux; one-sided aux is a parameterisation presence divergence
  - error/timeout/unparsed/missing-side queries get a verdict-class record
    (category = the verdict name) instead of a tree record

Usage: pg-plan-first-divergence.py [--verbose] <goopg-capture> <pg-capture>
Both operands are ===/===== sections files (or a directory of QN.txt for the
PG side). Output: per-query first-divergence lines plus a ranked frequency
table. Exit 2 on usage error; 0 otherwise (a divergent corpus is not a
failure — the table is the product).
"""

import argparse
import importlib.util
import os
import re
import sys
from collections import Counter

_HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location(
    "pg_plan_parity_diff", os.path.join(_HERE, "pg-plan-parity-diff.py"))
d = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(d)

# Both header spellings exist: estimate-audit writes `=== Qn`, the SF0.25
# sweep writes `===== Qn =====`.
SECTION_RE = re.compile(r"^={3,}\s*([A-Za-z0-9_+.-]+?)\s*=*\s*$")


def parse_sections(path):
    """===/===== KEY ===== sections -> {key: [lines]} (headers excluded)."""
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


def load_side(path):
    if os.path.isdir(path):
        out = {}
        for name in sorted(os.listdir(path)):
            if name.endswith(".txt"):
                with open(os.path.join(path, name), encoding="utf-8",
                          errors="replace") as fh:
                    out[name[:-4]] = [ln.rstrip() for ln in fh]
        return out
    return parse_sections(path)


def rec(parent, pg_child, goopg_child, depth, category, detail=""):
    return {"parent": parent, "pg": pg_child, "goopg": goopg_child,
            "depth": depth, "cat": category, "detail": detail}


def _agg_base(k):
    return k.split(" ", 1)[1] if k.split(" ", 1)[0] in (
        "Partial", "Finalize") else k


def _extra_cat(n):
    """extra_category with the same phased-agg awareness as _mismatch_cat."""
    if _agg_base(n.kind) in d.AGG_KINDS:
        return "aggregation-strategy"
    return d.extra_category(n)


def _mismatch_cat(g, p):
    """mismatch_category with phased-agg awareness: the sibling's fallback
    reads `HashAggregate` vs `Finalize HashAggregate` as join-order because
    the phased kind is not in AGG_KINDS; for a decision-point census an
    aggregate-phase divergence is an aggregation-strategy record."""
    if _agg_base(g.kind) in d.AGG_KINDS and _agg_base(p.kind) in d.AGG_KINDS:
        return "aggregation-strategy"
    return d.mismatch_category(g, p)


def _join_leafset_cat(g, p, tables):
    sub = any(a.kind in ("SubPlan", "InitPlan") for a in d.walk(g)) or \
        any(a.kind in ("SubPlan", "InitPlan") for a in d.walk(p))
    return "parameterisation" if sub else "join-order"


def _prop_divergence(g, p, tables):
    """cmp_same's property checks in the same order; returns a category
    name or None. KEY_PROPS diffs are verdict-neutral 'rendering' in the
    differ and do not count as a census divergence."""
    if g.parallel != p.parallel:
        return "parallelism"
    if g.est.get("workers") != p.est.get("workers") and \
            (g.est.get("workers") or p.est.get("workers")):
        return "parallelism"
    if g.kind in d.SCAN_KINDS:
        if d.scan_key(g, tables) != d.scan_key(p, tables):
            return "scan-type"
        if d.is_parameterised(g, tables, d.leaves(g, tables)) != \
                d.is_parameterised(p, tables, d.leaves(p, tables)):
            return "parameterisation"
    if g.kind in d.JOIN_METHODS:
        if d.norm_jointype(g.detail) != d.norm_jointype(p.detail):
            return "join-method"
        if d.leaves(g, tables) != d.leaves(p, tables):
            return _join_leafset_cat(g, p, tables)
        pairs = [("JoinQual",
                  g.props.get("Filter", []) + g.props.get("Join Filter", []),
                  p.props.get("Filter", []) + p.props.get("Join Filter", []))]
        rest = [k for k in d.COND_PROPS if k not in ("Filter", "Join Filter")]
    else:
        pairs = []
        rest = list(d.COND_PROPS)
    for key in rest:
        pairs.append((key, g.props.get(key, []), p.props.get(key, [])))
    for key, gvs, pvs in pairs:
        if not gvs and not pvs:
            continue
        if not gvs or not pvs or \
                sorted(d.qual_signature(v, tables) for v in gvs) != \
                sorted(d.qual_signature(v, tables) for v in pvs):
            return d.key_category(key, " ".join(gvs + pvs), tables)
    return None


def first_divergence(g, p, parent_kind, depth, tables):
    """First divergence in PG's plan-order, or None. g/p are Node|None."""
    pk = parent_kind if parent_kind else "-"
    if g is None or p is None:
        present = g if g is not None else p
        side = "goopg" if g is not None else "PG"
        cat = _extra_cat(present) or "join-order"
        return rec(pk, d.describe(p) if p is not None else "-",
                   d.describe(g) if g is not None else "-", depth, cat,
                   "%s present only on %s side" % (d.describe(present), side))
    if g.kind != p.kind:
        return rec(pk, d.describe(p), d.describe(g), depth,
                   _mismatch_cat(g, p))
    cat = _prop_divergence(g, p, tables)
    if cat:
        return rec(pk, d.describe(p), d.describe(g), depth, cat)
    # children pairing: joins first get the swapped-children check, which is
    # itself the divergence (positional pairing below would be luck).
    gch, pch = g.children, p.children
    if g.kind in d.JOIN_METHODS and len(gch) == len(pch) == 2 and \
            not _children_match(gch, pch, tables) and \
            _children_match(gch, list(reversed(pch)), tables):
        return rec(g.kind, "<%s , %s>" % (pch[0].kind, pch[1].kind),
                   "<%s , %s>" % (gch[0].kind, gch[1].kind), depth + 1,
                   "join-order", "swapped children")
    for i in range(min(len(gch), len(pch))):
        r = first_divergence(gch[i], pch[i], g.kind, depth + 1, tables)
        if r:
            return r
    for j in range(min(len(gch), len(pch)), max(len(gch), len(pch))):
        extra = gch[j] if j < len(gch) else pch[j]
        side = "goopg" if j < len(gch) else "PG"
        cat = _extra_cat(extra) or "join-order"
        return rec(g.kind, d.describe(pch[j]) if j < len(pch) else "-",
                   d.describe(gch[j]) if j < len(gch) else "-", depth + 1,
                   cat, "%s present only on %s side" % (d.describe(extra),
                                                        side))
    # aux blocks pair by name; they print after stream children.
    ga = {(a.kind, a.detail): a for a in g.aux}
    pa = {(a.kind, a.detail): a for a in p.aux}
    for name in sorted(set(ga) | set(pa)):
        label = "%s%s" % (name[0], name[1])
        if name in ga and name in pa:
            r = first_divergence(ga[name], pa[name], g.kind, depth + 1,
                                 tables)
            if r:
                return r
        else:
            side = "goopg" if name in ga else "PG"
            return rec(g.kind, label if name in pa else "-",
                       label if name in ga else "-", depth + 1,
                       "parameterisation",
                       "%s present only on %s side" % (label, side))
    return None


def _children_match(gch, pch, tables):
    """children_match without the Ctx (census does not need categories)."""
    if len(gch) != len(pch):
        return False
    for a, b in zip(gch, pch):
        if a.kind != b.kind:
            return False
        if a.kind in d.SCAN_KINDS and \
                d.scan_key(a, tables) != d.scan_key(b, tables):
            return False
    return True


def census_query(key, glines, plines):
    """One record per divergent query; None for a census match."""
    warnings = []
    if glines is None or plines is None:
        side = "goopg" if glines is None else "PG"
        return rec("-", "-", "-", 0, "error",
                   "missing %s section" % side)
    groots, gerr, gto = d.parse_plan(glines, warnings)
    proots, perr, pto = d.parse_plan(plines, warnings)
    unknowns = sorted({w for w in warnings if w.startswith("unknown node")})
    if gerr or perr or gto or pto:
        verdict = "timeout" if (gto or pto) else "error"
        return rec("-", "-", "-", 0, verdict,
                   "%s marker in %s block" % (verdict,
                                              "both" if (gerr or gto) and
                                              (perr or pto) else
                                              ("goopg" if gerr or gto
                                               else "PG")))
    if groots is None or proots is None:
        return rec("-", "-", "-", 0, "error",
                   "unparseable %s plan" %
                   ("goopg" if groots is None else "PG"))
    if unknowns:
        return rec("-", "-", "-", 0, "unparsed",
                   "unknown node kinds: %s" % "; ".join(unknowns))
    tables = d.collect_tables(groots) | d.collect_tables(proots)
    for r in (groots, proots):
        d.reassign_aux(r)
    applied = set()
    groots = d.normalise_tree(groots, tables, applied)
    proots = d.normalise_tree(proots, tables, applied)
    r = first_divergence(groots, proots, None, 0, tables)
    pg_kinds = {n.kind for n in d.walk(proots)}
    missing = sorted(pg_kinds & set(d.GOOPG_UNEMITTABLE))
    if missing and r is None:
        # cannot happen for a real divergence (presence catches it), but a
        # PG-only kind inside a fully-aligned aux position is reported
        # verbatim rather than lost.
        return rec("-", ",".join(missing), "-", 0, "missing-node",
                   "PG-only kinds: %s" % ",".join(missing))
    return r


def run_census(goopg_path, pg_path):
    goopg, pg = load_side(goopg_path), load_side(pg_path)
    out = {}
    for key in sorted(set(goopg) | set(pg),
                      key=lambda k: (int(re.sub(r"\D", "", k) or 0), k)):
        out[key] = census_query(key, goopg.get(key), pg.get(key))
    return out


def _kind(child):
    """Bucket key for the table: bare kind (detail stripped), aux label kept."""
    return child.split(" (")[0]


def print_report(results, verbose=False):
    table = Counter()
    catroll = Counter()
    ndiv = 0
    for key in sorted(results,
                      key=lambda k: (int(re.sub(r"\D", "", k) or 0), k)):
        r = results[key]
        if r is None:
            print("%s MATCH (no structural divergence)" % key)
            continue
        ndiv += 1
        print("%s depth=%d [%s] under %s: PG %s | goopg %s%s" % (
            key, r["depth"], r["cat"], r["parent"], r["pg"], r["goopg"],
            ("  -- " + r["detail"]) if r["detail"] else ""))
        table[(r["cat"], r["parent"], _kind(r["pg"]), _kind(r["goopg"]),
               r["depth"])] += 1
        catroll[r["cat"]] += 1
    print("FIRST-DIVERGENCE: queries=%d divergent=%d match=%d" % (
        len(results), ndiv, len(results) - ndiv))
    print("CATEGORY-ROLLUP: %s" % " ".join(
        "%s=%d" % kv for kv in catroll.most_common()))
    print("FIRST-DIVERGENCE TABLE (category | parent | PG child | goopg child "
          "| depth -> count):")
    for (cat, par, pc, gc, depth), n in table.most_common():
        print("  %3d  %-16s under %-16s PG %-28s goopg %-28s depth=%d" % (
            n, cat, par, pc, gc, depth))
    return table, catroll


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("goopg", help="goopg plans file (=== or ===== sections)")
    ap.add_argument("pg", help="PG plans file or directory of QN.txt")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)
    print_report(run_census(args.goopg, args.pg), verbose=args.verbose)
    return 0


if __name__ == "__main__":
    sys.exit(main())
