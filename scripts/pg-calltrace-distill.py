#!/usr/bin/env python3
"""Distill PostgreSQL debug_plan_callgraph (CGT) trace lines into a per-query
planner-route skeleton.

Input: a server-log slice containing CGT records emitted by the
M0144-0006-instrumented build (-finstrument-functions on
src/backend/optimizer/, hooks in optimizer/util/calltrace.c):

  CGT base <addr>              — PIE anchor: runtime &standard_planner
  CGT e <depth> <fn> <site>    — function entry
  CGT x <depth> <fn>           — function exit
  CGT tag <key=value ...>      — explicit arg marker (level=, mkjoin, joinlist)

Resolution: addresses are runtime PIE addresses; the script loads `nm -n`
output for the postgres binary once, computes delta = base_addr -
static(&standard_planner), and maps every event address to the nearest
symbol at-or-below (delta-corrected). Statics, local clones (.part.0,
.isra, .constprop), and GCC-emitted local copies of extern leaf functions
(list_nth_cell, newNode, castNodeImpl inside optimizer TUs) all resolve —
the leaf clones are just noise filtered out by the route set.

Output per slice:
  1. Route skeleton — one line per ROUTE-set enter/exit, indented by the
     recorded depth, with CGT tag markers interleaved at their position.
  2. Call-count table — top 40 functions by entry count (all functions,
     including non-route ones, so hot spots are visible).

Usage: pg-calltrace-distill.py <postgres-binary> <log-slice> [more...]
"""

import bisect
import re
import subprocess
import sys
from collections import Counter

# Structural planner functions — the "route" the task asks about. Leaf
# predicates (is_dummy_rel, list_*, cost_*), expression mutators, and
# catalog lookups are excluded; candidate-level detail lives in PLANCAND.
ROUTE_RE = re.compile(r"""^(
    planner | standard_planner | subquery_planner | grouping_planner |
    query_planner | preprocess_targetlist |
    pull_up_sublinks | pull_up_simple_union_all | pull_up_subqueries |
    make_one_rel | set_base_rel_sizes | set_base_rel_pathlists |
    set_base_rel_consider_startup | set_rel_pathlist |
    set_plain_rel_pathlist | set_append_rel_pathlist |
    set_dummy_rel_pathlist | set_subquery_pathlist |
    set_foreign_pathlist | set_function_pathlist | set_values_pathlist |
    set_cte_pathlist | set_worktable_pathlist |
    set_namedtuplestore_pathlist | set_tablefunc_pathlist |
    create_plain_partial_paths | make_rel_from_joinlist |
    standard_join_search | geqo.* | join_search_one_level |
    make_rels_by_clause_joins | make_rels_by_clauseless_joins |
    make_join_rel | build_join_rel | join_is_legal |
    has_join_restriction | have_relevant_joinclause |
    populate_joinrel_with_paths | try_partitionwise_join |
    add_paths_to_joinrel | sort_inner_and_outer |
    match_unsorted_outer | hash_inner_and_outer |
    try_.*path.* | generate_useful_gather_paths |
    generate_gather_paths | generate_partitionwise_join_paths |
    add_paths_to_append_rel | create_append_path |
    create_merge_append_path | create_.*append.* |
    create_.*_path.* | create_.*_paths.* | set_cheapest |
    create_plan | set_plan_references | apply_.* |
    get_useful_pathkeys_for_relation | degenerate.*
)$""", re.VERBOSE)


def load_syms(binary):
    out = subprocess.check_output(["nm", "-n", binary], text=True)
    addrs, names = [], []
    for line in out.splitlines():
        p = line.split()
        if len(p) == 3 and p[1] in ("t", "T"):
            addrs.append(int(p[0], 16))
            names.append(p[2])
    return addrs, names


def distill(binary, addrs, names, path):
    base = None
    events = []          # (kind, depth, name | tagtext)
    counts = Counter()
    raw_e = raw_x = 0

    def name_of(addr):
        i = bisect.bisect_right(addrs, addr) - 1
        return names[i] if i >= 0 else f"<?0x{addr:x}>"

    for line in open(path):
        m = re.match(r"CGT base (0x[0-9a-f]+)", line)
        if m:
            base = int(m.group(1), 16)
            continue
        m = re.match(r"CGT ([ex]) (\d+) (0x[0-9a-f]+)", line)
        if m:
            kind, depth, addr = m.group(1), int(m.group(2)), int(m.group(3), 16)
            events.append(("e" if kind == "e" else "x", depth, addr))
            if kind == "e":
                raw_e += 1
            else:
                raw_x += 1
            continue
        m = re.match(r"CGT tag (.*)", line)
        if m:
            events.append(("tag", None, m.group(1).strip()))

    if base is None:
        print(f"# {path}: NO CGT base anchor — cannot resolve")
        return
    sp_static = addrs[names.index("standard_planner")]
    delta = base - sp_static

    route = []
    for kind, depth, val in events:
        if kind == "tag":
            route.append(("tag", depth, val))
            continue
        n = name_of(val - delta)
        if kind == "e":
            counts[n] += 1
        if ROUTE_RE.match(n):
            route.append((kind, depth, n))

    print(f"# {path}")
    print(f"# base=0x{base:x} delta=0x{delta:x} "
          f"entries={raw_e} exits={raw_x} balanced={raw_e == raw_x}")
    for kind, depth, val in route:
        if kind == "tag":
            print("    TAG " + val)
        elif kind == "e":
            print("  " * depth + ">" + val)
        else:
            print("  " * depth + "<" + val)
    print("--- top-40 call counts ---")
    for n, c in counts.most_common(40):
        print(f"{c:7d} {n}")
    print()


def main():
    binary = sys.argv[1]
    addrs, names = load_syms(binary)
    for p in sys.argv[2:]:
        distill(binary, addrs, names, p)


if __name__ == "__main__":
    main()
