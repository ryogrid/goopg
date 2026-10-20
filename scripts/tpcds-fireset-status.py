#!/usr/bin/env python3
"""Compare baseline and candidate execution status for a derived TPC-DS fire set.

The caller supplies result files with one ``Q<n> STATUS`` record per executed
fire.  A candidate timeout which was not a baseline timeout is a failure; a
baseline timeout is not silently treated as a pass, but is reported as an
unchanged known timeout.  This is intentionally independent of the private
lane lifecycle so the execution runner can remain shell-based and testable.

Owner-maintained timeout allowlist (``scripts/tpcds-fireset-timeout-allow.tsv``,
added 2026-10-19): an introduced timeout of a (corpus, query) pair listed there
with an unexpired date is reported as ``allowed`` and does not fail the gate.
It exists for queries whose runtime straddles FIRESET_TIMEOUT on BOTH arms
(SF1 Q74: 543 s baseline vs 522 s candidate, identical output), where the
introduced timeout is host noise, not a change the candidate made. Only the
owner edits that file (harness-protected); an expired row is inert.
"""

import argparse
import datetime
import os
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


DEFAULT_ALLOW = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tpcds-fireset-timeout-allow.tsv")


def read_allow(path, corpus, today=None):
    """Return {query: reason} for unexpired allowlist rows of this corpus.

    Row format (tab-separated): corpus  Q<n>  expires(YYYY-MM-DD)  reason.
    Blank lines and lines starting with # are ignored."""
    today = today or datetime.date.today()
    allowed = {}
    if not corpus or not path or not os.path.exists(path):
        return allowed
    with open(path, encoding="utf-8") as handle:
        for raw in handle:
            line = raw.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            cols = line.split("\t")
            if len(cols) < 4 or cols[0] != corpus:
                continue
            match = re.fullmatch(r"Q(\d+)", cols[1].strip())
            try:
                expires = datetime.date.fromisoformat(cols[2].strip())
            except ValueError:
                continue
            if match and expires >= today:
                allowed[int(match.group(1))] = cols[3].strip()
    return allowed


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
    import tempfile
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False) as handle:
        handle.write("# comment\ntpcds-sf1\tQ74\t2026-11-30\tstraddles\n"
                     "tpcds-sf1\tQ77\t2026-01-01\texpired\ntpcds-sf025\tQ5\t2026-11-30\tother corpus\n")
    allow = read_allow(handle.name, "tpcds-sf1", today=datetime.date(2026, 10, 19))
    os.unlink(handle.name)
    ok2 = allow == {74: "straddles"}
    print("%d/2 passed" % (ok + ok2) if ok and ok2 else
          "FAIL: comparator or allowlist (got %r)" % (allow,))
    return 0 if ok and ok2 else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline")
    parser.add_argument("--candidate")
    parser.add_argument("--fires", help="comma-separated query ids")
    parser.add_argument("--corpus", help="corpus name the allowlist is matched against")
    parser.add_argument("--allow", default=DEFAULT_ALLOW, help="owner-maintained timeout allowlist")
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
    allow = read_allow(args.allow, args.corpus)
    allowed = [q for q in introduced if q in allow]
    introduced = [q for q in introduced if q not in allow]
    print("FIRE-SET-TIMEOUTS: fires=%s introduced=%s unchanged=%s missing=%s allowed=%s" %
          (format_queries(fires), format_queries(introduced), format_queries(unchanged), format_queries(missing),
           format_queries(allowed)))
    for query in allowed:
        print("  allowed Q%d (owner allowlist %s): %s" % (query, os.path.basename(args.allow), allow[query]))
    if missing:
        return 2
    return 1 if introduced else 0


if __name__ == "__main__":
    sys.exit(main())
