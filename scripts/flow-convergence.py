#!/usr/bin/env python3
"""Summarise M0145 flow telemetry from one isolated planner trace capture.

The input is the server-log slice produced by the SF0.25 sweep's fresh
EXPLAIN-only plan tail.  It contains one ``SUBLINKCENSUS route=...`` event for
each sublink-planning route and zero or more ``seam-decline reason=...``
events.  This program deliberately reports only those existing diagnostic
events: it neither changes a plan nor supplies a movement metric.

Usage:
  flow-convergence.py --label LABEL --trend TREND LOG
  flow-convergence.py --self-test
"""

import argparse
import datetime
import re
import sys


ROUTE_RE = re.compile(r"SUBLINKCENSUS route=(\S+)")
DECLINE_RE = re.compile(r"seam-decline reason=(\S+)")


def census(lines):
    """Return route and seam-decline counters from an iterable of log lines."""
    routes = {}
    declines = {}
    for line in lines:
        route = ROUTE_RE.search(line)
        if route:
            key = route.group(1)
            routes[key] = routes.get(key, 0) + 1
        decline = DECLINE_RE.search(line)
        if decline:
            key = decline.group(1)
            declines[key] = declines.get(key, 0) + 1
    return routes, declines


def ratio(routes):
    pinned = routes.get("pinned-spine", 0)
    pulled = routes.get("jointree-pullup", 0)
    return "inf" if pulled == 0 and pinned else ("0.00" if pulled == 0 else "%.2f" % (pinned / pulled))


def summary(label, routes, declines):
    pinned = routes.get("pinned-spine", 0)
    pulled = routes.get("jointree-pullup", 0)
    total = sum(declines.values())
    lines = [
        "FLOW-CONVERGENCE: label=%s pinned-spine=%d jointree-pullup=%d "
        "pinned-per-pullup=%s seam-classes=%d seam-declines=%d" %
        (label, pinned, pulled, ratio(routes), len(declines), total)
    ]
    for reason in sorted(declines):
        lines.append("# flow-convergence: seam-decline reason=%s count=%d" %
                     (reason, declines[reason]))
    return "\n".join(lines)


def append_trend(path, label, routes, declines):
    """Append one tab-separated, grep-friendly row; headers are created once."""
    pinned = routes.get("pinned-spine", 0)
    pulled = routes.get("jointree-pullup", 0)
    encoded_declines = ",".join("%s=%d" % item for item in sorted(declines.items())) or "-"
    needs_header = False
    try:
        needs_header = not open(path, encoding="utf-8").read(1)
    except FileNotFoundError:
        needs_header = True
    with open(path, "a", encoding="utf-8") as trend:
        if needs_header:
            trend.write("# timestamp\\tlabel\\tpinned-spine\\tjointree-pullup\\tpinned-per-pullup\\tseam-declines\\n")
        trend.write("%s\t%s\t%d\t%d\t%s\t%s\n" %
                    (datetime.datetime.now(datetime.timezone.utc).isoformat(), label,
                     pinned, pulled, ratio(routes), encoded_declines))


SELF_TESTS = [
    ("both routes and repeated decline buckets", [
        "SUBLINKCENSUS route=pinned-spine\n",
        "SUBLINKCENSUS route=jointree-pullup\n",
        "seam-decline reason=lateral\n",
        "seam-decline reason=lateral\n",
        "seam-decline reason=leaf-count\n",
    ], {"pinned-spine": 1, "jointree-pullup": 1}, {"lateral": 2, "leaf-count": 1}),
    ("unrelated lines do not create telemetry", ["DPPATH path producer=join.hash\n"], {}, {}),
]


def self_test():
    passed = 0
    for name, lines, want_routes, want_declines in SELF_TESTS:
        got_routes, got_declines = census(lines)
        if got_routes == want_routes and got_declines == want_declines:
            passed += 1
        else:
            print("FAIL: %s (routes=%r declines=%r)" % (name, got_routes, got_declines))
    print("%d/%d passed" % (passed, len(SELF_TESTS)))
    return passed == len(SELF_TESTS)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("log", nargs="?")
    parser.add_argument("--label")
    parser.add_argument("--trend")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return 0 if self_test() else 1
    if not args.log or not args.label or not args.trend:
        parser.error("LOG, --label, and --trend are required unless --self-test is used")
    try:
        with open(args.log, encoding="utf-8", errors="replace") as log:
            routes, declines = census(log)
    except OSError as error:
        print("FATAL: cannot read trace log: %s" % error, file=sys.stderr)
        return 2
    if not routes:
        print("FATAL: trace log contains no SUBLINKCENSUS events", file=sys.stderr)
        return 2
    print(summary(args.label, routes, declines))
    append_trend(args.trend, args.label, routes, declines)
    return 0


if __name__ == "__main__":
    sys.exit(main())
