#!/usr/bin/env python3
"""seam-decline-census.py -- per-class seam-decline census from a
GOOPG_PGSHAPED_DP_TRACE=1 capture log (M0137-0014).

Why this exists
----------------
`AGENT.md` §"Plan-parity harness" item 4 makes a seam-decline census **by
class, not by total**, at a stated timeout, a mandatory field in every
M0137-0143 task report -- because a class can be *converted* rather than
removed, and censuses are only comparable at equal timeouts. Until this
tool existed there was no producer for it: the harness's own defect audit
(`tmp/METHODLOGY3_RALPH_CHECK0915/03-harness-defects.md`, item D1) measured
that only 3 of the first 22 tasks carried it, and only because a loop
hand-classified `traceSeamDecline`'s stderr output that loop. A number a
human must re-derive by hand in an autonomous loop reliably rots -- the
same reasoning that produced `pg-plan-parity-diff.py`'s
`CATEGORIES-EXCL-MATCH:` line for category movement
(`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced`'s sibling
fix). This is that fix for the seam-decline item: it automates exactly the
one-liner AGENT.md names as the manual fallback --
`grep -oP "seam-decline reason=\\K\\S+" <log> | sort | uniq -c` -- and adds
the timeout stamp the manual command has no way to carry.

What it does NOT do: run a capture itself. The caller still runs
`GOOPG_PGSHAPED_DP_TRACE=1` against the server/tool of choice (e.g.
`./bin/estimate-audit -plan-only` per `m0137-0003-baseline-capture-
procedure.md`) and redirects stderr to a log; this tool only aggregates
that log's `seam-decline reason=...` lines
(`internal/optimizer/joinsearchtrace.go`'s `traceSeamDecline`, called from
the 18-member fixed reason vocabulary in `joinsearchseam.go` and
`relfromjoinlist.go`). `--timeout` is a required, free-text label (not
parsed from the log -- the log carries no timeout of its own) so a report
cannot paste a census without also stating what it is comparable against.

Input format: any text file/stream containing zero or more lines matching
`seam-decline reason=<class>` anywhere in the line (the exact substring
`traceSeamDecline` emits); everything else is ignored, so a full server
log or `estimate-audit` capture can be pointed at directly.

Usage:
  seam-decline-census.py --timeout TIMEOUT LOG [LOG ...]
  seam-decline-census.py --self-test

Exit status:
  0  report produced (a census is report-only, like pg-plan-parity-diff.py
     -- it is a number a task report pastes, not a pass/fail gate)
  2  a log file could not be read, or --timeout was not given
"""

import argparse
import re
import sys

DECLINE_RE = re.compile(r"seam-decline reason=(\S+)")


def census(lines):
    """iterable of lines -> {reason: count}, total declines."""
    counts = {}
    for line in lines:
        m = DECLINE_RE.search(line)
        if not m:
            continue
        counts[m.group(1)] = counts.get(m.group(1), 0) + 1
    return counts


def census_files(paths):
    counts = {}
    for path in paths:
        with open(path, encoding="utf-8", errors="replace") as fh:
            for reason, n in census(fh).items():
                counts[reason] = counts.get(reason, 0) + n
    return counts


def format_report(counts, timeout, logs):
    total = sum(counts.values())
    lines = ["SEAM-DECLINE-CENSUS: timeout=%s logs=%d classes=%d declines=%d" %
             (timeout, logs, len(counts), total)]
    for reason in sorted(counts):
        lines.append("%6d reason=%s" % (counts[reason], reason))
    return "\n".join(lines)


def run(paths, timeout):
    try:
        counts = census_files(paths)
    except OSError as e:
        print("unavailable: cannot read log: %s" % e)
        return 2
    print(format_report(counts, timeout, len(paths)))
    return 0


# ---------------------------------------------------------------- self-test

SELF_TESTS = [
    {
        "name": "no decline lines -> zero census",
        "lines": ["some unrelated server log line",
                  "DPTRACE top=t1+t2 pairs=3 declined=0 costs=3 status=ok"],
        "counts": {},
    },
    {
        "name": "real traceSeamDecline format, single class",
        "lines": ["DPTRACE seam-decline reason=leaf-count nrels=11 nleaves=1"],
        "counts": {"leaf-count": 1},
    },
    {
        "name": "multiple classes, repeated -- matches the AGENT.md grep|sort|uniq -c fallback",
        "lines": [
            "DPTRACE seam-decline reason=leaf-count nrels=11 nleaves=1",
            "DPTRACE seam-decline reason=outer-spine nrels=4 nleaves=4",
            "DPTRACE seam-decline reason=leaf-count nrels=6 nleaves=1",
            "DPTRACE seam-decline reason=lateral nrels=3 nleaves=3",
        ],
        "counts": {"leaf-count": 2, "outer-spine": 1, "lateral": 1},
    },
    {
        "name": "line has other prefix text before DPTRACE (full server log) -- unanchored match",
        "lines": ["2026-09-15T12:00:00Z backend[123] DPTRACE seam-decline "
                   "reason=nil-leaf nrels=5 nleaves=2"],
        "counts": {"nil-leaf": 1},
    },
]


def self_test():
    passed = 0
    for t in SELF_TESTS:
        got = census(t["lines"])
        ok = got == t["counts"]
        status = "PASS" if ok else "FAIL"
        if ok:
            passed += 1
        else:
            print("%s: %s (want %r got %r)" %
                  (status, t["name"], t["counts"], got))
    print("%d/%d passed" % (passed, len(SELF_TESTS)))
    return passed == len(SELF_TESTS)


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("logs", nargs="*")
    ap.add_argument("--timeout",
                    help="label stamped into the report, e.g. '10m' or "
                         "'estimate-audit default (10m)' -- required unless --self-test")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()
    if args.self_test:
        sys.exit(0 if self_test() else 1)
    if not args.logs:
        ap.error("at least one LOG is required unless --self-test is given")
    if not args.timeout:
        print("unavailable: --timeout is required (AGENT.md item 4: "
              "'censuses are only comparable at equal timeouts')")
        sys.exit(2)
    sys.exit(run(args.logs, args.timeout))


if __name__ == "__main__":
    main()
