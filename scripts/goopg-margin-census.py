#!/usr/bin/env python3
"""goopg-margin-census — M0144-0007: price PG's winning shape inside goopg's
model for every first-divergence census record.

For each divergent query record emitted by scripts/pg-plan-first-divergence.py
(`Qn depth=D [cat] under PARENT: PG <pgkind> | goopg <gkind>`), this runner
collects three margin evidences on a live goopg lane:

  1. FORCED — session `enable_*` (and parallel-cost) arms that exclude the
     goopg winner; the forced plan is re-censused against the PG capture with
     the same census_query() machinery, so "produced" means the recorded
     divergence record actually moved.  Margin = root total-cost delta %.
  2. CANDIDATE — the lane's DPPATH provenance trace (server must run with
     GOOPG_PGSHAPED_DP_TRACE=1; --log is sliced per query).  When a candidate
     of the PG child's producer family was OFFERED at a rel and dominated,
     the margin is its cost delta vs that rel's winner — the exact
     "priced-and-lost" number the census wants.  "never offered" is the
     unexpressible/structural class.
  3. ORDERED-SEED — `upper.ordered.candidates`/`upper.ordered.seed` summary
     lines tell whether any presorted input existed at all
     (nonemptykeys/keys=0 ⇒ the no-sort / incremental-sort shape could not be
     generated — generation gap, not an election loss).

Margin classes (03-forward-plan §2):
    <1%    election/tie-break      comparePaths-level fix class
    1-20%  input divergence        attribute rows/width/cost-term first
    >20% or unexpressible          structural gap / missing mechanism

usage:
    goopg-margin-census.py --census CENSUS.txt --pgplans PG.txt \
        --qdir DIR --qpat 'query{n}.sql' --port P --db D --user U \
        --log SERVER.log --out OUT.txt [--only Q3,Q7] [--limit N]

The server must already be running (private lane).  Session pins match the
capture harness: work_mem=64MB, max_parallel_workers_per_gather=4.
"""

import argparse
import importlib.util
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PSQL = os.path.join(REPO, "postgres/local_install/bin/psql")
TIMEOUT = 180

spec = importlib.util.spec_from_file_location(
    "census", os.path.join(REPO, "scripts/pg-plan-first-divergence.py"))
cen = importlib.util.module_from_spec(spec)
sys.modules["census"] = cen
spec.loader.exec_module(cen)

# --- census record parsing ------------------------------------------------

CEN_RE = re.compile(
    r"^(\S+) depth=(\d+) \[([^\]]+)\] under (.*?): PG (.*?) \| goopg (.*?)\s*$")

# PG child kind (census `_kind` bucket, detail stripped) -> producer family
# prefixes in goopg's DPPATH vocabulary.  Kinds with no producer analogue
# (plan-boundary kinds like CTE/Subquery Scan/SetOp callers) are absent on
# purpose: they can only be structural.
FAMILY = {
    "Incremental Sort": ["upper.ordered.incrementalsort",
                         "upper.partialsort."],
    "Sort": ["upper.ordered.sort"],
    "GroupAggregate": ["upper.groupagg.sort", "upper.groupagg.plain",
                       "upper.groupagg.sortedidx"],
    "Finalize GroupAggregate": ["upper.groupagg.finalize",
                                "upper.groupagg.split",
                                "upper.groupagg.gathered",
                                "upper.groupagg.gathermerge"],
    "Partial GroupAggregate": ["upper.partialgroupagg.partial"],
    "HashAggregate": ["upper.groupagg.hashed"],
    "Finalize HashAggregate": ["upper.groupagg.finalize",
                               "upper.groupagg.split",
                               "upper.groupagg.gathered",
                               "upper.groupagg.gathermerge"],
    "Partial HashAggregate": ["upper.partialgroupagg.partial"],
    "MixedAggregate": ["upper.groupagg."],
    "Group": ["upper.groupagg.", "upper.distinct."],
    "Aggregate": ["upper.groupagg.", "upper.distinct.", "upper.agg"],
    "Nested Loop": ["join.nestloop", "nestloop.index",
                    "join.nestloop.partial"],
    "Hash Join": ["join.hash", "join.hash.partial"],
    "Parallel Hash Join": ["join.hash", "join.hash.partial"],
    "Merge Join": ["mergejoin"],
    "Seq Scan": ["scan.seq", "scan.seq.partial"],
    "Parallel Seq Scan": ["scan.seq", "scan.seq.partial"],
    "Index Scan": ["index."],
    "Index Only Scan": ["index."],
    "Bitmap Heap Scan": ["bitmap."],
    "Gather": ["gather", "gather.merge", "upper.groupagg.gathermerge"],
    "Gather Merge": ["gather.merge", "upper.partialsort.gathermerge",
                     "upper.groupagg.gathermerge"],
    "Memoize": ["memoize"],
    "Materialize": ["material"],
    "SetOp": ["upper.setop."],
    "HashSetOp": ["upper.setop."],
    "WindowAgg": ["upper.window."],
    "Append": ["append", "upper.setop.append"],
    "Limit": ["limit"],
}

# goopg child kind -> session arm(s) that exclude it.  PG-style enable_*
# discouragement semantics; a record with no arm is only measurable via
# DPPATH evidence.
ARM = {
    "Sort": ["enable_sort=off"],
    "HashAggregate": ["enable_hashagg=off"],
    "Finalize HashAggregate": ["enable_hashagg=off"],
    "Partial HashAggregate": ["enable_hashagg=off"],
    "GroupAggregate": [],
    "Nested Loop": ["enable_nestloop=off"],
    "Hash Join": ["enable_hashjoin=off"],
    "Merge Join": ["enable_mergejoin=off"],
    "Seq Scan": ["enable_seqscan=off"],
    "Index Scan": ["enable_indexscan=off"],
    "Index Only Scan": ["enable_indexonlyscan=off"],
    "Bitmap Heap Scan": ["enable_bitmapscan=off"],
    "Memoize": ["enable_memoize=off"],
    "Materialize": ["enable_material=off"],
    "Incremental Sort": ["enable_incremental_sort=off"],
    "Gather": ["max_parallel_workers_per_gather=0"],
    "Gather Merge": ["max_parallel_workers_per_gather=0"],
}
PAR_ENCOURAGE = ["parallel_setup_cost=0", "parallel_tuple_cost=0",
                 "min_parallel_table_scan_size=0",
                 "min_parallel_index_scan_size=0"]
PAR_KINDS = ("Parallel", "Gather", "Finalize", "Partial")

DP_RE = re.compile(
    r"^DPPATH (path|partial) producer=(\S+) relids=(\S+) kind=\d+ "
    r"reqouter=\S+ rows=\S+ startup=\S+ total=(\S+) disabled=\d+ "
    r"pathkeys=\S+ verdict=(\S+)")
SUM_RE = re.compile(r"^DPPATH (candidates|seed) producer=(\S+) (.*)$")
COST_RE = re.compile(r"cost=[\d.]+\.\.([\d.]+)")


def log_size(path):
    try:
        return os.path.getsize(path)
    except OSError:
        return 0


def read_since(path, off):
    try:
        with open(path, "rb") as f:
            f.seek(off)
            return f.read().decode("utf-8", "replace")
    except OSError:
        return ""


def run_explain(args, stmts, extra_sets):
    """One psql session: pins + extra SETs + EXPLAIN per statement."""
    sql = []
    for s in stmts:
        if s.strip():
            sql.append("EXPLAIN " + s.strip() + ";")
    cmd = [PSQL, "-h", "127.0.0.1", "-p", str(args.port), "-U", args.user,
           "-d", args.db, "-X", "-c", "SET work_mem='64MB'",
           "-c", "SET max_parallel_workers_per_gather=4"]
    for st in extra_sets:
        cmd += ["-c", "SET " + st]
    p = subprocess.run(cmd + ["-f", "-"], input="\n".join(sql),
                       capture_output=True, text=True, timeout=TIMEOUT)
    lines = [l for l in (p.stdout + p.stderr).splitlines()
             if l.strip() != "SET"]
    return lines


def root_cost(lines):
    for l in lines:
        m = COST_RE.search(l)
        if m:
            return float(m.group(1))
    return None


def plan_lines(lines):
    """Strip the psql QUERY PLAN banner so census parsing sees plan nodes."""
    out = []
    for l in lines:
        if "QUERY PLAN" in l or set(l.strip()) == {"-"} or not l.strip():
            continue
        out.append(l)
    return out


def parse_dppath(text):
    cands, summaries = [], []
    for l in text.splitlines():
        m = DP_RE.match(l.strip())
        if m:
            cands.append({"list": m.group(1), "producer": m.group(2),
                          "relids": m.group(3), "total": float(m.group(4)),
                          "verdict": m.group(5)})
            continue
        m = SUM_RE.match(l.strip())
        if m:
            summaries.append(l.strip())
    return cands, summaries


def kind_of(child):
    return child.split(" (")[0]


def cand_margin(cands, pg_fam, g_fam):
    """Best (smallest positive) margin of a PG-family candidate vs the
    accepted winner at the same relids+list; plus offered count."""
    offered = [c for c in cands
               if any(c["producer"].startswith(p) for p in pg_fam)]
    if not offered:
        return None, 0
    best = None
    for c in offered:
        same = [w for w in cands
                if w["relids"] == c["relids"] and w["list"] == c["list"]
                and w["verdict"] == "accepted"]
        if g_fam:
            pref = [w for w in same
                    if any(w["producer"].startswith(p) for p in g_fam)]
            if pref:
                same = pref
        if not same:
            continue
        w = min(same, key=lambda x: x["total"])
        if w["total"] > 0:
            m = (c["total"] - w["total"]) / w["total"] * 100.0
            if best is None or m < best:
                best = m
    if best is None and offered:
        # candidate offered but no same-rel accepted winner found —
        # report the cheapest offered vs nothing (margin unknown)
        return None, len(offered)
    return best, len(offered)


def classify(margin, produced, offered, arm_tried):
    """Return (class, basis)."""
    if produced and margin is not None:
        basis = "forced-root"
        m = margin
    elif offered and margin is not None:
        basis = "dppath-cand"
        m = margin
    else:
        if offered:
            return "offered-unpriced", "dppath-cand"
        return ("unexpressible" if arm_tried else "unexpressible/no-arm",
                "none")
    if m < 0:
        if produced:
            # forcing the arm produced PG's shape CHEAPER than goopg's own
            # winner — the winner's advantage was an election/fuzz artifact,
            # not price.
            return "forced-cheaper", basis
        # PG-shape candidate offered CHEAPER than the winner yet not chosen:
        # dominance is multi-criteria (pathkeys/parameterisation), so it lost
        # on feasibility grounds, not price — an input-shape structural gap,
        # not an election loss.
        return "dominated-noncost", basis
    a = abs(m)
    if a < 1.0:
        return "election", basis
    if a <= 20.0:
        return "input", basis
    return "priced-structural", basis


def arm_list(rec):
    """Candidate session-arm combos for this record."""
    g = kind_of(rec["goopg"])
    pg = kind_of(rec["pg"])
    combos = []
    if g in ARM and ARM[g]:
        combos.append(ARM[g])
    if any(pg.startswith(k) for k in PAR_KINDS):
        par = list(PAR_ENCOURAGE)
        if combos:
            combos.append(combos[0] + par)
        else:
            combos.append(par)
    return combos


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--census", required=True)
    ap.add_argument("--pgplans", required=True)
    ap.add_argument("--qdir", required=True)
    ap.add_argument("--qpat", default="query{n}.sql")
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--db", required=True)
    ap.add_argument("--user", default="postgres")
    ap.add_argument("--log", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--only", default="")
    ap.add_argument("--limit", type=int, default=0)
    args = ap.parse_args()

    # census records
    records = {}
    for l in open(args.census):
        m = CEN_RE.match(l.rstrip())
        if m:
            records[m.group(1)] = {
                "depth": int(m.group(2)), "cat": m.group(3),
                "parent": m.group(4), "pg": m.group(5), "goopg": m.group(6)}
    only = {q.strip() for q in args.only.split(",") if q.strip()}
    if only:
        records = {k: v for k, v in records.items() if k in only}
    pg = cen.load_side(args.pgplans)

    out = open(args.out, "w")
    hdr = ("qid | cat | under | PG | goopg | produced | cost0 | cost1 | "
           "rootd% | candm% | offered | class | basis | note")
    out.write(hdr + "\n")
    done = 0
    for qid in sorted(records, key=lambda k: int(re.sub(r"\D", "", k) or 0)):
        rec = records[qid]
        if rec["cat"] in ("error", "timeout", "unparsed", "missing-node"):
            continue
        n = int(re.sub(r"\D", "", qid))
        qf = os.path.join(args.qdir, qid + ".sql")
        if not os.path.exists(qf):
            qf = os.path.join(args.qdir, args.qpat.format(n=n, n02=n))
        if not os.path.exists(qf):
            out.write("%s | %s | %s | %s | %s | - | - | - | - | - | - | "
                      "no-query-file | - | %s missing\n"
                      % (qid, rec["cat"], rec["parent"], rec["pg"],
                         rec["goopg"], qf))
            continue
        stmts = open(qf).read().split(";")
        pg_lines = pg.get(qid)

        off0 = log_size(args.log)
        base = run_explain(args, stmts, [])
        off1 = log_size(args.log)
        cost0 = root_cost(base)
        base_dpp, base_sum = parse_dppath(read_since(args.log, off0))

        # sanity: reproduce the census record on this lane
        recheck = cen.census_query(qid, plan_lines(base), pg_lines) \
            if pg_lines else None
        repro = (recheck is not None and
                 recheck["cat"] == rec["cat"] and
                 cen._kind(recheck["goopg"]) == kind_of(rec["goopg"]) and
                 cen._kind(recheck["pg"]) == kind_of(rec["pg"]))

        produced, cost1, arm_used, newrec = False, None, "", None
        for combo in arm_list(rec):
            forced = run_explain(args, stmts, combo)
            fc = root_cost(forced)
            nr = cen.census_query(qid, plan_lines(forced), pg_lines) \
                if pg_lines else None
            changed = (nr is None or
                       cen._kind(nr["goopg"]) == kind_of(rec["pg"]) or
                       (nr["cat"], nr["parent"], cen._kind(nr["pg"]),
                        cen._kind(nr["goopg"]), nr["depth"]) !=
                       (rec["cat"], rec["parent"], kind_of(rec["pg"]),
                        kind_of(rec["goopg"]), rec["depth"]))
            if changed:
                produced, cost1, arm_used, newrec = True, fc, \
                    "+".join(combo), nr
                break
        off2 = log_size(args.log)
        arm_dpp, _ = parse_dppath(read_since(args.log, off1))

        pg_fam = FAMILY.get(kind_of(rec["pg"]), [])
        g_fam = FAMILY.get(kind_of(rec["goopg"]), [])
        cm, offered = (None, 0)
        if pg_fam:
            cm, offered = cand_margin(base_dpp, pg_fam, g_fam)
            if cm is None:
                cm2, off2n = cand_margin(arm_dpp, pg_fam, g_fam)
                if cm2 is not None or off2n > offered:
                    cm, offered = cm2, offered or off2n

        rootd = ((cost1 - cost0) / cost0 * 100.0
                 if produced and cost0 and cost1 is not None else None)
        cls, basis = classify(rootd if produced else cm,
                              produced, offered, bool(arm_list(rec)))
        note = ""
        if not repro:
            note = "baseline-record-not-reproduced"
        if newrec is not None:
            note += (" new=[%s] under %s: PG %s | goopg %s"
                     % (newrec["cat"], newrec["parent"], newrec["pg"],
                        newrec["goopg"]))
        seed = [s for s in base_sum if "upper.ordered" in s]
        if seed:
            note += " seed=" + ";".join(seed[:2])

        out.write("%s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %d | "
                  "%s | %s |%s\n"
                  % (qid, rec["cat"], rec["parent"], rec["pg"], rec["goopg"],
                     ("yes:" + arm_used) if produced else "no",
                     ("%.2f" % cost0) if cost0 is not None else "-",
                     ("%.2f" % cost1) if cost1 is not None else "-",
                     ("%.3f" % rootd) if rootd is not None else "-",
                     ("%.3f" % cm) if cm is not None else "-",
                     offered, cls, basis, note))
        out.flush()
        done += 1
        if args.limit and done >= args.limit:
            break
    out.close()


if __name__ == "__main__":
    main()
