#!/usr/bin/env python3
"""Compare baseline and candidate execution status for a derived TPC-DS fire set.

The caller supplies result files with one ``Q<n> STATUS`` record per executed
fire.  A candidate timeout which was not a baseline timeout is a failure; a
baseline timeout is not silently treated as a pass, but is reported as an
unchanged known timeout.  This is intentionally independent of the private
lane lifecycle so the execution runner can remain shell-based and testable.
"""

import argparse
import re
import sys


LINE = re.compile(r"^Q(\d+)\s+(PASS|TIMEOUT|ERROR)\b")


def read_status(path):
    rows = {}
    with open(path, encoding="utf-8", errors="replace") as handle:
        for raw in handle:
            match = LINE.match(raw.strip())
            if match:
                rows[int(match.group(1))] = match.group(2)
    return rows


def compare(fires, baseline, candidate):
    """Return introduced timeouts, unchanged timeouts, and missing records."""
    introduced, unchanged, missing = [], [], []
    for query in fires:
        before, after = baseline.get(query), candidate.get(query)
        if before is None or after is None:
            missing.append(query)
        elif after == "TIMEOUT" and before != "TIMEOUT":
            introduced.append(query)
        elif after == "TIMEOUT":
            unchanged.append(query)
    return introduced, unchanged, missing


def format_queries(queries):
    return " ".join("Q%d" % query for query in queries) or "none"


def self_test():
    baseline = {77: "PASS", 78: "TIMEOUT", 95: "ERROR"}
    candidate = {77: "TIMEOUT", 78: "TIMEOUT", 95: "PASS"}
    introduced, unchanged, missing = compare([77, 78, 95, 99], baseline, candidate)
    ok = introduced == [77] and unchanged == [78] and missing == [99]
    print("1/1 passed" if ok else "FAIL: comparator did not distinguish introduced, unchanged, and missing")
    return 0 if ok else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline")
    parser.add_argument("--candidate")
    parser.add_argument("--fires", help="comma-separated query ids")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return self_test()
    if not args.baseline or not args.candidate or not args.fires:
        parser.error("--baseline, --candidate, and --fires are required")
    try:
        fires = sorted({int(query) for query in args.fires.split(",") if query})
        baseline, candidate = read_status(args.baseline), read_status(args.candidate)
    except (OSError, ValueError) as error:
        print("FATAL: cannot read fire-set status: %s" % error, file=sys.stderr)
        return 2
    introduced, unchanged, missing = compare(fires, baseline, candidate)
    print("FIRE-SET-TIMEOUTS: fires=%s introduced=%s unchanged=%s missing=%s" %
          (format_queries(fires), format_queries(introduced), format_queries(unchanged), format_queries(missing)))
    if missing:
        return 2
    return 1 if introduced else 0


if __name__ == "__main__":
    sys.exit(main())
