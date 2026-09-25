#!/usr/bin/env python3
"""Translate a TPC-H acceptance-arm output file into fire-set status records.

M0145-0021b. The TPC-DS fire-set arms execute `query<n>.sql` through psql and
read the exit status; TPC-H has no .sql files at all — its query bank lives in
`cmd/tpch-runner` — so its fire set is executed by `tpch-acceptance-arm.sh`
with QUERIES=<ids>, and this tool maps that arm's per-query lines onto the same
`Q<n> PASS|TIMEOUT|ERROR` vocabulary `tpcds-fireset-status.py` compares.

The mapping is the load-bearing part, because the gate's whole pass condition
is "no new timeout":

    Q7: OK elapsed=1.23s ...                                    -> PASS
    Q9: ERROR after 600.10s - ... user request (57014)           -> TIMEOUT
    Q9: ERROR after 0.02s - ... syntax error ...                 -> ERROR

The runner enforces PER_Q as a statement timeout, so a query that exceeds the
budget comes back as an ERROR carrying SQLSTATE 57014 ("canceling statement").
Classifying that as ERROR would hide exactly the class this gate exists to
catch, and classifying every ERROR as TIMEOUT would invent them.

One numeric id can produce several labels (Q15 runs as Q15-CREATEVIEW,
Q15a-VIEWBODY, Q15b-MAIN). They fold to one record, worst outcome winning:
TIMEOUT > ERROR > PASS. An id with no line at all is fatal — a silently
missing record would read as "not executed yet" to the comparator's resume
logic and could let an incomplete run look complete.
"""

from __future__ import annotations

import argparse
import re
import sys

# `Q15a-VIEWBODY: OK ...` and `Q9: ERROR after ...`. The id is the leading run
# of digits; anything after it up to the colon is a sub-label, so `Q1` never
# matches `Q10`.
LINE_RE = re.compile(r"^Q(\d+)([A-Za-z0-9_-]*)\s*:\s*(OK|ERROR)\b(.*)$")
# SQLSTATE 57014 is query_canceled; the runner's per-query cap surfaces as
# exactly that. The text is matched too because a driver may render the code
# without the parenthesised form.
TIMEOUT_MARKERS = ("57014", "canceling statement")

RANK = {"PASS": 0, "ERROR": 1, "TIMEOUT": 2}


def classify(kind: str, rest: str) -> str:
    if kind == "OK":
        return "PASS"
    low = rest.lower()
    if any(m in low for m in TIMEOUT_MARKERS):
        return "TIMEOUT"
    return "ERROR"


def parse(text: str, ids: list[int]) -> tuple[list[str], list[int]]:
    """Return (status lines, ids with no matching arm line)."""
    seen: dict[int, str] = {}
    for line in text.splitlines():
        m = LINE_RE.match(line.strip())
        if not m:
            continue
        qid = int(m.group(1))
        status = classify(m.group(3), m.group(4))
        if qid not in seen or RANK[status] > RANK[seen[qid]]:
            seen[qid] = status
    out = [f"Q{q} {seen[q]}" for q in ids if q in seen]
    return out, [q for q in ids if q not in seen]


def self_test() -> int:
    arm = """# arm=fireset GOOPG_PGSHAPED_DP=1
Q1: OK elapsed=1.00s colsig=a ordered=b unordered=c rows=4
Q9: ERROR after 600.10s - pq: canceling statement due to user request (57014)
Q10: ERROR after 0.02s - pq: syntax error at or near "selct"
Q15-CREATEVIEW: OK elapsed=0.01s rows=0
Q15a-VIEWBODY: OK elapsed=0.40s rows=10000
Q15b-MAIN: ERROR after 600.00s - pq: canceling statement due to user request (57014)
# finished runner-rc=0
"""
    out, missing = parse(arm, [1, 9, 10, 15])
    assert out == ["Q1 PASS", "Q9 TIMEOUT", "Q10 ERROR", "Q15 TIMEOUT"], out
    assert missing == [], missing

    # Q1 must not absorb Q10's line, in either direction.
    out, _ = parse("Q10: OK elapsed=1s\n", [1, 10])
    assert out == ["Q10 PASS"], out

    # A sub-label that passes does not mask a sibling that timed out, and the
    # reverse fold (PASS after TIMEOUT) does not downgrade it either.
    out, _ = parse("Q15b-MAIN: ERROR after 1s - 57014\nQ15a: OK elapsed=1s\n", [15])
    assert out == ["Q15 TIMEOUT"], out

    # An ERROR does not outrank a TIMEOUT, and a missing id is reported.
    out, missing = parse("Q3: ERROR after 1s - pq: relation does not exist\n", [3, 4])
    assert out == ["Q3 ERROR"], out
    assert missing == [4], missing

    # Noise lines (headers, the arm's own summary) are ignored.
    out, _ = parse("# per-query cap 600s\nSUMMARY: 24 MATCH\nQ2: OK elapsed=1s\n", [2])
    assert out == ["Q2 PASS"], out
    print("tpch-fireset-parse self-test: 5/5 OK")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("arm_file", nargs="?", help="tpch-acceptance-arm.sh output file")
    ap.add_argument("ids", nargs="?", help="comma-separated query ids that were requested")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()
    if args.self_test:
        return self_test()
    if not args.arm_file or not args.ids:
        ap.error("arm_file and ids are required without --self-test")
    ids = [int(q) for q in args.ids.split(",") if q]
    with open(args.arm_file, encoding="utf-8", errors="replace") as f:
        out, missing = parse(f.read(), ids)
    if missing:
        print(
            "FATAL: no per-query line for id(s) %s in %s — the arm did not run them"
            % (",".join(map(str, missing)), args.arm_file),
            file=sys.stderr,
        )
        return 2
    for line in out:
        print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
