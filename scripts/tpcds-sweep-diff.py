#!/usr/bin/env python3
"""tpcds-sweep-diff.py — diff two SF0.5 sweep reports by NAMED query, not by count.

Why this exists (M0127-P5.6-f-v, 2026-08-05). The SF0.5 gate's verdict line is a
set of COUNTS:

    === SUMMARY: PASS=94 (57 ck-verified, 37 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4 ===

`TIMEOUT=1` is invariant to WHICH query timed out. On 2026-08-04/05 commit
`ce027cee` (P5.6-f) traded one timeout for another — Q72 stopped timing out and
Q47 started (31 s -> 523 s, a 17x re-pricing of the same query) — and the
summary line was byte-identical across FOUR consecutive sweeps. Nobody could
have seen it in the report; it took a bisect against a copy of the cluster to
find (analysis/m0127-p56fiii/README.md, design 09 §5.15).

The plan-shape channel (P5.6-g-i-b, tpcds-plan-diff.py) closed the neighbouring
blind spot — plans moving in silence — but a plan diff answers "did the shape
change", not "did a query that used to finish stop finishing". This tool closes
the remaining one by comparing the per-query STATUS/RUNTIME VECTOR of two
reports and printing what moved, by name:

    TIMEOUT  +Q47 -Q72

Input is the sweep report itself (no new artefact, no new format), so every one
of the ~90 reports already archived under
bench/tpcds/runtime_goopg/tpcds-results-sf05/ is diffable retroactively — which
is how this tool was validated: replayed over the whole corpus, it names the
Q72->Q47 trade the summary line hid.

RUNTIME comparison — the deliberate limits, stated out loud so a quiet report is
never read as "nothing moved":

  * Reports carry INTEGER seconds. A 1 s -> 3 s query is "3x" and is noise, so a
    move is only printed when the larger of the two readings is at least
    --min-secs (default 5 s). The floor is printed in the header every run.
  * TIMEOUT readings are the CAP, not a runtime (a clipped query reports the
    cap). Queries whose verdict is TIMEOUT on either side are excluded from the
    runtime arm — the verdict arm already names them.
  * Per-query seconds are only meaningful on a quiet host. A report stamped
    `FORCE=1` says its own seconds are invalid; this tool repeats that warning
    and still prints the verdict arm, which stays valid.

Usage:
    tpcds-sweep-diff.py OLD NEW [--min-secs N] [--factor F] [--strict]

    --strict   exit 1 when any verdict set changed; the default exit is ALWAYS
               0 so the gate can call this without making a traded timeout
               fatal (gate semantics are unchanged: correctness fails the gate,
               performance does not).
"""

import argparse
import re
import sys

# Q<n> <VERDICT> <secs>s ... — the shapes cmd_sweep printf()s. CKMISMATCH uses a
# narrower seconds field ("%2ss") than the rest ("%4ss"), so the whitespace runs
# are matched loosely and the digits strictly.
TIMED_RE = re.compile(r"^Q(\d+)\s+(PASS|MISMATCH|CKMISMATCH|TIMEOUT|ERROR)\s+(\d+)s\b")
SKIP_RE = re.compile(r"^Q(\d+)\s+(SKIP)\b")
# Parsing stops here: everything after the verdict is the non-blocking plan
# channel, whose diff output mentions query ids of its own ("changed (74): Q1
# Q2 ...") and must never be mistaken for a verdict line.
SUMMARY_RE = re.compile(r"^=== SUMMARY:")
VERDICTS = ("PASS", "MISMATCH", "CKMISMATCH", "ERROR", "TIMEOUT", "SKIP")


def parse(path):
    """path -> ({qid: (verdict, secs|None)}, [header line, ...]).

    Only the region above `=== SUMMARY:` is read (see SUMMARY_RE). Header lines
    are the leading `#` provenance block the harness stamps.
    """
    rows, header = {}, []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for raw in fh:
            line = raw.rstrip()
            if SUMMARY_RE.match(line):
                break
            if line.startswith("#"):
                if not rows:
                    header.append(line)
                continue
            m = TIMED_RE.match(line)
            if m:
                rows[int(m.group(1))] = (m.group(2), int(m.group(3)))
                continue
            m = SKIP_RE.match(line)
            if m:
                rows[int(m.group(1))] = ("SKIP", None)
    return rows, header


def header_field(header, prefix, fallback=""):
    for line in header:
        if line.startswith(prefix):
            return line[len(prefix):].strip()
    return fallback


def qlist(qids):
    return " ".join("Q%d" % q for q in sorted(qids))


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("old")
    ap.add_argument("new")
    ap.add_argument("--min-secs", type=int, default=5,
                    help="ignore runtime moves where max(old,new) is below this (default 5)")
    ap.add_argument("--total-pct", type=float, default=2.0,
                    help="report an AGGREGATE runtime move of at least this "
                         "percent. Arm 2 is per-query with a --factor floor and "
                         "cannot see a broad shallow regression; take2's "
                         "per-tuple index qual cost moved the sweep +3.3%% with "
                         "runtime-moves=0. Three same-day sweeps spanned "
                         "+/-0.4%%, so 2%% sits outside the observed noise.")
    ap.add_argument("--factor", type=float, default=2.0,
                    help="report a runtime move at or beyond this factor (default 2.0)")
    ap.add_argument("--strict", action="store_true")
    args = ap.parse_args()

    try:
        old, old_hdr = parse(args.old)
        new, new_hdr = parse(args.new)
    except OSError as exc:
        # Same contract as the plan channel: an unreadable baseline degrades to
        # a note, never to a failed gate.
        print(f"# status-delta: unavailable ({exc})")
        return 0

    print(f"# status-delta: {args.old} -> {args.new}")
    print("#   old: %s" % header_field(old_hdr, "# goopg:", "provenance not stamped"))
    print("#   new: %s" % header_field(new_hdr, "# goopg:", "provenance not stamped"))

    # Comparability warnings. None of these suppress the diff — they say which
    # arm of it to trust, which is the whole point of the tool.
    old_to = header_field(old_hdr, "# timeout:", "?")
    new_to = header_field(new_hdr, "# timeout:", "?")
    if old_to != new_to:
        print(f"#   NOTE: per-query timeout differs (old {old_to}, new {new_to}) — "
              "the TIMEOUT set is not comparable across caps")
    for tag, hdr in (("old", old_hdr), ("new", new_hdr)):
        if header_field(hdr, "# FORCE=1"):
            print(f"#   NOTE: {tag} report ran under FORCE=1 — its per-query seconds are NOT valid")
        probe = header_field(hdr, "# SUBSET PROBE")
        if probe:
            m = re.search(r"\(QUERIES=[^)]*\)", probe)
            print(f"#   NOTE: {tag} report is a SUBSET PROBE {m.group(0) if m else ''} — "
                  "only the queries it covers are compared")

    moved = False

    # Both arms compare the INTERSECTION only. A query absent from one report
    # (subset probe, or a corpus that grew) has no verdict there — counting it
    # as "left the PASS set" made every full-vs-probe pair scream. What is
    # missing is reported once, by name, as ONLY-OLD / ONLY-NEW below.
    shared = set(old) & set(new)

    # --- arm 1: the verdict sets, by name.
    for verdict in VERDICTS:
        was = {q for q in shared if old[q][0] == verdict}
        now = {q for q in shared if new[q][0] == verdict}
        gained, lost = now - was, was - now
        if not gained and not lost:
            continue
        moved = True
        parts = []
        if gained:
            parts.append("+" + " +".join("Q%d" % q for q in sorted(gained)))
        if lost:
            parts.append("-" + " -".join("Q%d" % q for q in sorted(lost)))
        print("%-10s %s" % (verdict, "  ".join(parts)))

    # --- arm 2: runtimes that moved by --factor, excluding clipped readings.
    slower, faster = [], []
    for qid in sorted(shared):
        (ov, os_), (nv, ns) = old[qid], new[qid]
        if os_ is None or ns is None or "TIMEOUT" in (ov, nv):
            continue
        if max(os_, ns) < args.min_secs:
            continue
        # A 0 s reading is sub-second, not zero; treat it as 1 s so the ratio
        # stays finite (the --min-secs floor keeps such rows out anyway unless
        # the other side is large, which IS a move worth naming).
        lo, hi = max(os_, 1), max(ns, 1)
        ratio = hi / lo
        if ratio >= args.factor:
            slower.append((qid, os_, ns, ratio))
        elif ratio <= 1.0 / args.factor:
            faster.append((qid, os_, ns, ratio))

    def fmt(rows):
        return "  ".join("Q%d %ds->%ds (%.1fx)" % (q, a, b, r) for q, a, b, r in rows)

    if slower:
        moved = True
        print("SLOWER     " + fmt(sorted(slower, key=lambda r: -r[3])))
    if faster:
        moved = True
        print("FASTER     " + fmt(sorted(faster, key=lambda r: r[3])))

    # --- arm 3: the AGGREGATE total.
    #
    # Arm 2 is per-query with a --factor floor, and that is blind to a broad,
    # shallow regression: a change can move sixty plans, slow ten queries by
    # 3-5 s each, and report runtime-moves=0 because no single query crossed
    # 2.0x. That is not hypothetical — take2's per-tuple index qual cost did
    # exactly this (TPC-DS 1115s -> 1152s, +3.3%, runtime-moves=0), and it was
    # caught only by summing the sweep by hand.
    #
    # The band is deliberately tight. Three sweeps on this harness on one day
    # sat at 1110 / 1115 / 1119 s — +/-0.4% — so 2% is comfortably outside the
    # observed noise while still not firing on it.
    def sweep_total(tbl):
        # Only rows this comparison can trust: a reading is skipped when EITHER
        # side is missing or clipped, so both totals sum the SAME query set.
        return sum(tbl[q][1] for q in shared
                   if old[q][1] is not None and new[q][1] is not None
                   and "TIMEOUT" not in old[q][0] and "TIMEOUT" not in new[q][0])

    old_tot, new_tot = sweep_total(old), sweep_total(new)
    total_moved = False
    if old_tot > 0:
        pct = (new_tot - old_tot) / old_tot * 100.0
        if abs(pct) >= args.total_pct:
            total_moved = True
            moved = True
            print("TOTAL      %ds -> %ds (%+.1f%%)  <== aggregate move; no single "
                  "query need cross %.1fx for this to matter"
                  % (old_tot, new_tot, pct, args.factor))

    only_old, only_new = sorted(set(old) - shared), sorted(set(new) - shared)
    if only_old:
        print("ONLY-OLD   " + qlist(only_old) + "  (absent from the new report — NOT compared)")
    if only_new:
        print("ONLY-NEW   " + qlist(only_new) + "  (absent from the old report — NOT compared)")

    verdict_moved = any(
        {q for q in shared if old[q][0] == vd} != {q for q in shared if new[q][0] == vd}
        for vd in VERDICTS)
    total_note = "none"
    if old_tot > 0:
        total_note = "%+.1f%%" % ((new_tot - old_tot) / old_tot * 100.0)
    print("=== STATUS-DELTA: compared=%d verdict-changes=%s runtime-moves=%d "
          "total-delta=%s (>=%.1fx, floor %ds, total band %.1f%%, "
          "TIMEOUT readings excluded) ==="
          % (len(shared), "yes" if verdict_moved else "none",
             len(slower) + len(faster), total_note,
             args.factor, args.min_secs, args.total_pct))
    if not moved:
        print("# every query kept its verdict and stayed within %.1fx — "
              "this is the only form of 'nothing changed' the gate can assert." % args.factor)

    if args.strict and moved:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
