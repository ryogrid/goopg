#!/usr/bin/env python3
"""Distill PostgreSQL debug_plan_candidates (PLANCAND) trace lines into a
compact per-rel candidate table.

Input: a server-log slice containing PLANCAND records emitted by the
M0144-0005-instrumented pathnode.c (add_path / add_partial_path / their
prechecks / set_cheapest).

Record forms:
  PLANCAND add|padd   rel=(b ...) cand={...}
  PLANCAND ok|pok     rel=(b ...) cand={...} removed=N
  PLANCAND rej|prej   rel=(b ...) cand={...} by={...} via=X cmp=...
  PLANCAND preskip|ppreskip rel=(b ...) dis=N cost=S..T pk=N par=... by={...}
  PLANCAND win        rel=(b ...) npath=N npart=N total={...} startup={...} param={...}

Output per rel (in trace order): a header with the set_cheapest winner and
survivor counts, then one line per candidate with its verdict — accepted
(removed=N) or rejected (dominating incumbent + comparator vector). A footer
summarizes totals and the rejection-reason histogram. This is the PG-side
counterpart of goopg's DPPATH records (GOOPG_PGSHAPED_DP_TRACE): the pair
separates candidate-generation gaps (candidate absent) from costing gaps
(candidate present, different cost) and comparator/tie-break gaps (same
costs, different verdict).

Usage: pg-plancand-distill.py <log-slice> [more slices...]
"""

import re
import sys
from collections import Counter, OrderedDict

PATHTYPE = {
    331: "Result", 332: "ProjectSet", 333: "ModifyTable", 334: "Append",
    335: "MergeAppend", 336: "RecursiveUnion", 339: "SeqScan",
    340: "SampleScan", 341: "IndexScan", 342: "IndexOnlyScan",
    343: "BitmapIndexScan", 344: "BitmapHeapScan", 345: "TidScan",
    346: "TidRangeScan", 348: "FunctionScan", 349: "ValuesScan",
    350: "TableFuncScan", 351: "CteScan", 352: "NamedTuplestoreScan",
    353: "WorkTableScan", 354: "ForeignScan", 355: "CustomScan",
    356: "NestLoop", 358: "MergeJoin", 359: "HashJoin", 360: "Material",
    361: "Memoize", 362: "Sort", 363: "IncrementalSort", 364: "Group",
    365: "Agg", 366: "WindowAgg", 367: "Unique", 368: "Gather",
    369: "GatherMerge", 370: "Hash", 371: "SetOp", 372: "LockRows",
    373: "Limit",
}

JOINTYPE = {0: "INNER", 1: "LEFT", 2: "FULL", 3: "RIGHT", 4: "SEMI",
            5: "ANTI", 6: "RSEMI", 7: "RANTI"}

PATH_RE = re.compile(r"\{([^}]*)\}")
FIELD_RE = re.compile(r"(\w+)=(\([^)]*\)|[^\s]+)")


def parse_path(s):
    """Parse the inside of a {k=... ...} descriptor into a dict."""
    d = {}
    for k, v in FIELD_RE.findall(s):
        d[k] = v
    if "k" in d:
        d["k"] = PATHTYPE.get(int(d["k"]), f"pt{d['k']}")
    if "jt" in d:
        d["jt"] = JOINTYPE.get(int(d["jt"]), f"jt{d['jt']}")
    return d


def fmt_path(d, brief=False):
    if not d:
        return "-"
    base = (f"{d.get('k', '?')} rows={d.get('rows', '?')} "
            f"cost={d.get('cost', '?')}")
    if brief:
        return base
    extras = []
    if d.get("par", "-") != "-":
        extras.append(f"par={d['par']}")
    if d.get("pk", "0") != "0":
        extras.append(f"pk={d['pk']}")
    if d.get("pw", "0") != "0":
        extras.append(f"pw={d['pw']}")
    if d.get("psafe", "1") != "1":
        extras.append(f"psafe={d['psafe']}")
    if "jt" in d:
        extras.append(f"jt={d['jt']} out={d.get('out', '-')} in={d.get('in', '-')}")
    if d.get("dis", "0") != "0":
        extras.append(f"dis={d['dis']}")
    return base + (" " + " ".join(extras) if extras else "")


def paths_in(line):
    return [parse_path(m.group(1)) for m in PATH_RE.finditer(line)]


def distill(path):
    rels = OrderedDict()   # rel -> {"win": line, "events": [str]}
    totals = Counter()
    via_hist = Counter()

    with open(path) as f:
        for line in f:
            m = re.search(r"PLANCAND (\w+) rel=(\(b[^)]*\)|\(\)) (.*)", line)
            if not m:
                continue
            verb, rel, rest = m.group(1), m.group(2), m.group(3)
            e = rels.setdefault(rel, {"win": None, "events": []})
            totals[verb] += 1

            if verb == "win":
                ps = paths_in(rest)
                hdr = re.search(r"npath=(\d+) npart=(\d+)", rest)
                e["win"] = (f"npath={hdr.group(1)} npart={hdr.group(2)} "
                            f"total=[{fmt_path(ps[0])}] "
                            f"startup=[{fmt_path(ps[1], brief=True)}] "
                            f"param=[{fmt_path(ps[2], brief=True)}]"
                            if hdr and len(ps) >= 3 else rest.strip())
                continue

            if verb in ("preskip", "ppreskip"):
                ps = paths_in(rest)
                head = rest.split("by=")[0].strip()
                e["events"].append(
                    f"    {verb:9s} {head} by=[{fmt_path(ps[0], brief=True) if ps else '-'}]")
                via_hist["preskip"] += 1
                continue

            ps = paths_in(rest)
            cand = fmt_path(ps[0]) if ps else "?"
            if verb in ("add", "padd"):
                e["events"].append(f"    {verb:9s} {cand}")
            elif verb in ("ok", "pok"):
                rm = re.search(r"removed=(\d+)", rest)
                e["events"].append(
                    f"    {verb:9s} {cand} removed={rm.group(1) if rm else '?'}")
            elif verb in ("rej", "prej"):
                dom = fmt_path(ps[1], brief=True) if len(ps) > 1 else "?"
                tail = re.search(r"via=\S+ cmp=\S+( \S+)*", rest)
                e["events"].append(
                    f"    {verb:9s} {cand} by=[{dom}] {tail.group(0) if tail else ''}")
                vm = re.search(r"via=(\S+)", rest)
                via_hist[vm.group(1) if vm else "?"] += 1

    print(f"# {path}")
    for rel, e in rels.items():
        print(f"=== rel={rel} {e['win'] or '(no win line)'}")
        for ev in e["events"]:
            print(ev)
    print(f"--- totals: {dict(totals)}")
    print(f"--- reject-via: {dict(via_hist)}")
    print()


for p in sys.argv[1:]:
    distill(p)
