#!/usr/bin/env python3
"""tpcds-plan-diff.py — compare two TPC-DS plan captures, query by query.

Why this exists (M0127-P5.6-g-i-b, 2026-08-05). The SF0.5 regression gate
compares ROW COUNTS and VALUE CHECKSUMS, which is the right primary bar and is
not weakened here. But it is blind to plan shape: on 2026-08-05 a whole-corpus
EXPLAIN A/B showed that commit `4b820ab8` re-ordered **74 of 99** TPC-DS plans
while the gate reported a line-for-line identical verdict (PASS=94 MISMATCH=0
CKMISMATCH=0, same 57 checksums, same single TIMEOUT). 74 plans moving in
silence is exactly the event a planner milestone wants to see, so the gate gets
a second, NON-BLOCKING channel: capture the plans, diff them against the
previous run, and print what moved.

The noise floor of that channel was measured at ZERO on 2026-08-05: the same
binary run twice produced byte-identical plans for all 99 queries. A diff here
is therefore signal, not flake — which is only true because the capture is
EXPLAIN-without-ANALYZE (no timings, no actual rows) on a freshly restarted
server.

Input format (produced by tpcds-sf05-regression.sh `plans`, and by the
hand-rolled `analysis/leftdeep-joins/2026-08-05-p56gi-capture.sh` that preceded
it — the two are deliberately diff-compatible):

    # optional '#' header lines (provenance), ignored by the diff
    ===== Q1 =====
    <psql EXPLAIN output>
    ===== Q2 =====
    ...

Usage:
    tpcds-plan-diff.py OLD NEW [--verbose] [--strict] [--context N]

    --verbose   print a unified diff of every changed query (--context lines
                of context, default 3)
    --strict    exit 1 when anything moved; the default exit is ALWAYS 0 so the
                gate can call this without making plan drift fatal
"""

import argparse
import difflib
import re
import sys

BLOCK_RE = re.compile(r"^=====\s*Q(\d+)\s*=====\s*$")
# psql prints the provenance the harness stamped; keep it out of the comparison
# so two captures of the same code never differ on their own timestamps.
PROVENANCE_RE = re.compile(r"^#")
# psql stamps errors with the PATH of the script it was reading —
# `psql:/tmp/xyz.sql:29: ERROR: ...`. That path is the harness's business, not
# the plan's: TPC-DS Q36/Q70/Q86 are dsqgen artefacts that fail to parse on PG
# too, so their block is an error message, and every capture written to a
# different directory (SF05_RESULTS_DIR redirected, or the hand-rolled
# predecessor writing to /tmp) reported all three as "changed". Three permanent
# false positives in a channel whose entire value rests on a zero noise floor.
# The line number is KEPT — it moves only when the query file itself does.
PSQL_PREFIX_RE = re.compile(r"^psql:\S+:(\d+):")


def parse(path):
    """path -> ({qid: [line, ...]}, [provenance line, ...]).

    Lines are right-stripped: psql pads the QUERY PLAN header and the dashes
    rule to the widest row, and trailing blanks are invisible churn. The psql
    error prefix has its script path canonicalised (see PSQL_PREFIX_RE).
    Everything else is compared byte for byte — a cost or rows= estimate that
    moved IS the signal this tool reports.
    """
    blocks, header, cur = {}, [], None
    with open(path, encoding="utf-8", errors="replace") as fh:
        for raw in fh:
            line = raw.rstrip()
            m = BLOCK_RE.match(line)
            if m:
                cur = int(m.group(1))
                blocks[cur] = []
                continue
            if cur is None:
                if PROVENANCE_RE.match(line):
                    header.append(line)
                continue
            blocks[cur].append(PSQL_PREFIX_RE.sub(r"psql:<script>:\1:", line))
    # Drop leading/trailing blank lines inside a block: psql emits a trailing
    # newline whose presence depends on how the capture loop appended.
    for qid, body in blocks.items():
        while body and not body[0]:
            body.pop(0)
        while body and not body[-1]:
            body.pop()
    return blocks, header


def label(header, fallback):
    """The '# goopg: <sha> <subject>' line if the capture stamped one."""
    for line in header:
        if line.startswith("# goopg:"):
            return line[len("# goopg:"):].strip()
    return fallback


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("old")
    ap.add_argument("new")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--strict", action="store_true")
    ap.add_argument("--context", type=int, default=3)
    args = ap.parse_args()

    try:
        old, old_hdr = parse(args.old)
        new, new_hdr = parse(args.new)
    except OSError as exc:
        print(f"# plan-diff: unavailable ({exc})")
        return 0

    changed, same = [], []
    for qid in sorted(set(old) & set(new)):
        (same if old[qid] == new[qid] else changed).append(qid)
    removed = sorted(set(old) - set(new))
    added = sorted(set(new) - set(old))

    print(f"# plan-diff: {args.old} -> {args.new}")
    print(f"#   old: {label(old_hdr, 'provenance not stamped')}")
    print(f"#   new: {label(new_hdr, 'provenance not stamped')}")
    if changed:
        print("changed (%d): %s" % (len(changed), " ".join("Q%d" % q for q in changed)))
    if added:
        print("added   (%d): %s" % (len(added), " ".join("Q%d" % q for q in added)))
    if removed:
        print("removed (%d): %s" % (len(removed), " ".join("Q%d" % q for q in removed)))
    print(
        "=== PLAN-SHAPE: queries=%d same=%d changed=%d added=%d removed=%d ==="
        % (len(set(old) | set(new)), len(same), len(changed), len(added), len(removed))
    )
    if not changed and not added and not removed:
        print("# plan shapes identical (noise floor is zero — see header)")

    if args.verbose:
        for qid in changed:
            print(f"\n----- Q{qid} -----")
            for dl in difflib.unified_diff(
                old[qid], new[qid], fromfile=f"old/Q{qid}", tofile=f"new/Q{qid}",
                n=args.context, lineterm="",
            ):
                print(dl)

    if args.strict and (changed or added or removed):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
