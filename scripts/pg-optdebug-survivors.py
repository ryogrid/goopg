#!/usr/bin/env python3
"""Distill PostgreSQL OPTIMIZER_DEBUG pprint output into a per-rel survivor
pathlist table.

Input: a server-log slice containing nodeToString dumps ({RELOPTINFO ...}
blocks) produced by an -DOPTIMIZER_DEBUG build's pprint(rel) calls
(allpaths.c set_rel_pathlist/set_cheapest, join-level and appendrel dumps).

Output: for each RelOptInfo, one header line plus one line per surviving
pathlist/partial_pathlist entry (plan-node type, required_outer, rows,
disabled_nodes, parallel_workers, startup..total cost, pathkeys presence),
with the cheapest_total_path marked. Join paths nest their subpaths; fields
are extracted only at the entry's own sexp depth so nested nodes do not
pollute. nodeToString namespaces path fields (path., jpath.path., or bare
for plain Path nodes) — field names are matched by last dotted component.

Usage: pg-optdebug-survivors.py <log-slice> [more slices...]
"""

import re
import sys

NODE_OPEN = re.compile(r"\{([A-Z_]+)")
FIELD = re.compile(r":([a-z_.]+)(?:\s+(.*))?$")
STRING = re.compile(r'"([^"\\]|\\.)*"')
BITSET = re.compile(r"\(b[^)]*\)")

# Plan-node tags the pathtype field can carry (src/include/nodes/nodetags.h,
# PG 18.3). Only tags a planner Path can bear are listed.
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


def scan_tokens(line):
    """Structural tokens in line order, ignoring quoted strings."""
    line = STRING.sub('""', line)
    return [m.group(0) for m in re.finditer(r"\{[A-Z_]+|[{}()]", line)]


def bitset(val):
    m = BITSET.search(val)
    if m:
        return m.group(0)
    return val.split()[0] if val else val


def distill(path):
    print(f"=== {path} ===")
    depth = 0
    rel = None
    mode = None           # 'pathlist' | 'partial' | 'cheapest'
    mode_depth = 0        # start-of-line depth at which list children appear
    cur = None            # current path entry
    rel_n = 0

    def flush_rel():
        nonlocal rel, rel_n
        if rel is None:
            return
        rel_n += 1
        if rel.get("cheapest"):
            c = rel["cheapest"]
            for p in rel["pathlist"]:
                if (p["type"] == c["type"] and p.get("total") == c.get("total")
                        and p.get("startup") == c.get("startup")
                        and p.get("rows") == c.get("rows")):
                    p["cheapest"] = True
                    break
        print(f"REL {rel_n} relids={rel.get('relids','?')} rows={rel.get('rows','?')}"
              f" consider_parallel={rel.get('consider_parallel','?')}")
        for sect, label in (("pathlist", "pathlist"), ("partial", "partial_pathlist")):
            if not rel[sect]:
                continue
            print(f"  {label}:")
            for p in rel[sect]:
                mark = " *cheapest*" if p.get("cheapest") else ""
                pk = " pathkeys" if p.get("pathkeys") else ""
                req = f" req_outer={p['required_outer']}" if p.get("required_outer") else ""
                jt = f" jointype={p['jointype']}" if p.get("jointype") else ""
                idx = f" indexoid={p['indexoid']}" if p.get("indexoid") else ""
                print(f"    {p['type']} rows={p.get('rows','?')}"
                      f" cost={p.get('startup','?')}..{p.get('total','?')}"
                      f" dis={p.get('disabled','?')} pw={p.get('pworkers','?')}"
                      f"{req}{jt}{idx}{pk}{mark}")
        if rel.get("cheapest"):
            c = rel["cheapest"]
            print(f"  cheapest_total_path: {c['type']}"
                  f" cost={c.get('startup','?')}..{c.get('total','?')}")
        rel = None

    def entry_field(cur, name, val):
        """Set a scalar field on a path entry; first value wins."""
        last = name.split(".")[-1]
        if last == "pathtype" and cur.get("type") == "PATH":
            try:
                cur["type"] = PATHTYPE.get(int(val), f"pt{val}")
            except ValueError:
                pass
        elif last == "startup_cost" and "startup" not in cur:
            cur["startup"] = val
        elif last == "total_cost" and "total" not in cur:
            cur["total"] = val
        elif last == "rows" and "rows" not in cur:
            cur["rows"] = val
        elif last == "disabled_nodes" and "disabled" not in cur:
            cur["disabled"] = val
        elif last == "parallel_workers" and "pworkers" not in cur:
            cur["pworkers"] = val
        elif last == "required_outer" and "required_outer" not in cur:
            v = bitset(val)
            if v != "(b)":
                cur["required_outer"] = v
        elif last == "jointype":
            cur["jointype"] = val.split()[0]
        elif last == "pathkeys" and not val.startswith("<>"):
            cur["pathkeys"] = True
        elif last == "indexoid" and "indexoid" not in cur:
            cur["indexoid"] = val

    with open(path, errors="replace") as f:
        for line in f:
            pre = depth
            stripped = line.strip()
            toks = scan_tokens(line)

            # a pending 'cheapest' (value turned out to be <> null) clears on
            # the next field line at rel-field depth
            if (mode == "cheapest" and rel is not None
                    and rel.get("cheapest") is None
                    and FIELD.match(stripped) and pre <= mode_depth):
                mode, cur = None, None

            if rel is None:
                if stripped.startswith("{RELOPTINFO") and pre == 0:
                    rel = {"pathlist": [], "partial": []}
                    mode, cur = None, None
            else:
                if pre == 1 and mode is None:
                    fm = FIELD.match(stripped)
                    if fm:
                        name, val = fm.group(1), (fm.group(2) or "").strip()
                        if name == "relids" and "relids" not in rel:
                            rel["relids"] = bitset(val)
                        elif name == "rows" and "rows" not in rel:
                            rel["rows"] = val
                        elif name == "consider_parallel":
                            rel["consider_parallel"] = val.split()[0]
                        elif name == "pathlist":
                            mode, mode_depth = "pathlist", pre + 1
                        elif name == "partial_pathlist":
                            mode, mode_depth = "partial", pre + 1
                        elif name == "cheapest_total_path":
                            # node-valued field: the {X node opens at the
                            # SAME depth as the field (no list wrapper)
                            mode, mode_depth = "cheapest", pre

                nm = NODE_OPEN.match(stripped)
                if mode and nm and pre == mode_depth:
                    cur = {"type": nm.group(1)}
                    if mode == "cheapest":
                        rel["cheapest"] = cur
                    else:
                        rel[mode].append(cur)

                if cur is not None:
                    fm = FIELD.match(stripped)
                    if fm and not nm:
                        # fields directly on the entry node
                        if pre == mode_depth + 1:
                            entry_field(cur, fm.group(1), (fm.group(2) or "").strip())
                        # indexoid lives inside nested INDEXINFO
                        elif fm.group(1) == "indexoid" and "indexoid" not in cur:
                            cur["indexoid"] = (fm.group(2) or "").strip()

            for t in toks:
                depth += 1 if (t[0] == "{" or t == "(") else -1
            if mode:
                # lists end when depth drops below child depth; a node-valued
                # field ends when its node closes back to field depth — but
                # only after the node was actually seen (cur set)
                if (mode == "cheapest" and cur is not None
                        and depth <= mode_depth):
                    mode, cur = None, None
                elif mode != "cheapest" and depth < mode_depth:
                    mode, cur = None, None
            if rel is not None and depth == 0:
                flush_rel()
    flush_rel()


if __name__ == "__main__":
    for p in sys.argv[1:]:
        distill(p)
