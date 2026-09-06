#!/usr/bin/env python3
"""The estimate-accuracy (EA) parity ratchet, over TPC-DS.

  usage: parity.py <goopg-EXPLAIN-ANALYZE-capture> <pg-plan-dir> [--json OUT]
                   [--baseline B] [--write-baseline B] [--tol T] [--floor F]

WHY THIS EXISTS. Four TODO_ALL items (C-05, C-10a, C-20a, C-21) cite an
"EA ratchet" as their acceptance gate. Ledger `take3-ea-ratchet-never-ran`
established that it has never run: `scripts/tpch-estimate-audit-arm.sh` is
invoked by no Makefile target, hook, precommit script or nightly stage, and
its default pinned PG baseline is absent from the tree, so a default-flag run
exits before it measures anything. Three structural gaps made it unusable for
the defects it was cited against even if it had been wired: it is TPC-H only
(cmd/estimate-audit/main.go:317-322), it is joinrel-granular so a base
relation estimating `rows=1` is not a candidate (audit.go:96-98), and TPC-H
carries no LIMIT, so the NLI+Memoize shape that produces the aggregate
over-estimates never arises.

This tool closes all four gaps. It reads a real `EXPLAIN (ANALYZE)` capture
over the TPC-DS SF0.5 corpus, so its truth is measured rather than estimated;
it scores base relations and joinrels alike; and its bar is PG-RELATIVE.

WHY THE BAR MUST BE PG-RELATIVE, AND NOT ABSOLUTE. An absolute est-vs-actual
bar is wrong in both directions on this corpus. Q47/Q57/Q81/Q89 emit
`rows=1` on a node whose actual is tens of thousands, and PostgreSQL 18.3
emits `rows=1` on the very same node (bench/tpcds/plans-pg/Q47.txt): a chain
of mutually-implied equi-join selectivities multiplied independently, which
upstream does not de-duplicate at estimate time either. Those are not
defects, and an absolute bar fails them — which is how a gate gets disabled.
Conversely Q99's 8007x aggregate over-estimate is a real defect PG does not
share, and a loose absolute bar passes it. The only bar that PASSES Q47 and
FAILS Q99 is "goopg is materially worse than PostgreSQL on this node".

THE KEY. Nodes are matched across the two planners by the SET OF BASE
RELATIONS in their subtree, which is the one coordinate both planners agree
on: it survives a different join order, a different join algorithm, an extra
Gather, and a materialisation the other side does not have. A singleton set
is a base relation; a larger set is a joinrel; so one keying gives both
granularities, which is exactly what the joinrel-only predecessor could not
do. CTE bodies and sub-plans are keyed under a pseudo-relation named for the
CTE, so a CTE reference does not silently unify with the query that fills it.

THE SCORE. Symmetric q-error, `max(est, actual) / min(est, actual)` with both
floored at 1 — the standard cardinality-estimation metric, which treats a
1000x under-estimate and a 1000x over-estimate as equally bad (they are: one
picks a nested loop over a hash join, the other the reverse).

Estimates and actuals are both taken PER LOOP, never multiplied out: `rows=`
under the inner side of a nested loop is what the planner expects per
rescan, and PG's own annotation means the same thing. Multiplying the actual
by `loops` and comparing it against a per-loop estimate manufactures an
error equal to the loop count on every parameterised path.
"""

import argparse
import json
import os
import re
import sys

# A plan line: name, then PG's cost annotation, then optionally the ANALYZE
# annotation. Written to accept `never executed` because a node under an
# unexecuted SubPlan legitimately has no actual and must be dropped, not
# scored as zero.
NODE = re.compile(
    r'^(?P<pre>\s*)(?P<arrow>->\s{2})?(?P<name>[A-Z].*?)\s+'
    r'\(cost=(?P<sc>[\d.]+)\.\.(?P<tc>[\d.]+) rows=(?P<erows>\d+) width=(?P<w>\d+)\)'
    # `actual time=` is present only under TIMING ON; the capture runs with
    # TIMING OFF (the gate scores row counts, not milliseconds, and TIMING ON
    # costs a clock_gettime per tuple on a 99-query sweep), so the time pair
    # must be OPTIONAL. Requiring it matched zero nodes in the whole corpus
    # and reported a clean gate over an empty population.
    r'(?:\s+\((?:(?:actual\s+(?:time=(?P<st>[\d.]+)\.\.(?P<et>[\d.]+)\s+)?'
    r'rows=(?P<arows>[\d.]+)\s+loops=(?P<loops>[\d.]+))|never executed)\))?\s*$')

# `CTE v1` / `InitPlan 1 (returns $0)` / `SubPlan 2` marker lines. These carry
# no cost annotation but DO open an indent level, so a parser that ignores
# them attaches a CTE body to whatever node happened to precede it and every
# relation-set above that point is wrong.
MARK = re.compile(r'^(?P<pre>\s*)(?P<kind>CTE|InitPlan|SubPlan)\s+(?P<label>\S+).*$')

# Base-relation extraction. The trailing alias is dropped deliberately: goopg
# and PG do not always agree on when an alias is printed, and the alias is not
# part of the coordinate the key is built on.
SCAN_ON = re.compile(r'\bon\s+(?:(?P<schema>\w+)\.)?(?P<rel>\w+)')
CTE_SCAN = re.compile(r'^CTE Scan on (?P<rel>\w+)')
WORKTABLE = re.compile(r'^WorkTable Scan on (?P<rel>\w+)')

# Node names that scan something nameable. "Subquery Scan on x" is excluded on
# purpose: `x` is an inlined subquery alias, not a relation, and its subtree
# already contributes the real relations underneath it.
SCAN_KINDS = ('Seq Scan', 'Index Scan', 'Index Only Scan', 'Bitmap Heap Scan',
              'Tid Scan', 'Sample Scan', 'Foreign Scan')

# `Bitmap Index Scan on customer_pkey` names an INDEX, not a relation, and it
# contains the substring `Index Scan`. Without this exclusion the index name
# joins the relation set and a bitmap plan's joinrel keys stop matching the
# same joinrel reached by any other access method — on either side.


class Node:
    __slots__ = ('name', 'erows', 'width', 'arows', 'loops', 'indent',
                 'parent', 'children', 'relset', 'scope', 'pseudo')

    def __init__(self, name, erows, width, arows, loops, indent, scope,
                 pseudo=False):
        self.name = name
        self.erows = erows
        self.width = width
        self.arows = arows
        self.loops = loops
        self.indent = indent
        self.scope = scope
        self.parent = None
        self.children = []
        self.relset = None
        # A `CTE v1` / `SubPlan 2` marker carries no cost annotation of its
        # own; it exists to open a scope. It must never be SCORED, or every
        # CTE contributes a phantom node with est=0.
        self.pseudo = pseudo


def base_relation(name, scope):
    """The base relation this node scans, qualified by its CTE/SubPlan scope.

    Returns None for a node that scans nothing nameable. The scope prefix is
    what stops a CTE body's `store_sales` from unifying with the main query's
    `store_sales`: they are different instances with different quals, and
    merging them would compare a filtered estimate against an unfiltered
    actual.
    """
    m = CTE_SCAN.match(name) or WORKTABLE.match(name)
    if m:
        # A CTE reference is a leaf here, not a window onto the body: the body
        # is scored separately under its own scope. Naming it `cte:v1` keeps
        # goopg's inlined clone and PG's shared body on the same key.
        return 'cte:' + m.group('rel')
    head = name.split('  ')[0].strip()
    if 'Bitmap Index Scan' in head:
        return None
    for kind in SCAN_KINDS:
        if kind in head:
            m = SCAN_ON.search(head)
            if m:
                return (scope + '.' if scope else '') + m.group('rel')
            return None
    return None


def parse(body):
    """Rebuild the plan forest from raw indentation.

    Marker lines push a pseudo-level and rename the scope, so everything under
    `CTE v1` is keyed `v1.<rel>`.
    """
    roots, stack = [], []
    for ln in body.splitlines():
        m = NODE.match(ln)
        if m:
            indent = len(m.group('pre'))
            # A non-`->` line indented past column 2 is a detail line
            # (`Sort Key:`, `Filter:`) that happens to start with a capital.
            if not m.group('arrow') and indent > 2:
                continue
            while stack and stack[-1][0] >= indent:
                stack.pop()
            parent = stack[-1][1] if stack else None
            n = Node(m.group('name').strip(), int(m.group('erows')),
                     int(m.group('w')),
                     float(m.group('arows')) if m.group('arows') else None,
                     float(m.group('loops')) if m.group('loops') else None,
                     indent, parent.scope if parent else '')
            if parent is not None:
                n.parent = parent
                parent.children.append(n)
            else:
                roots.append(n)
            stack.append((indent, n))
            continue
        mk = MARK.match(ln)
        if mk:
            indent = len(mk.group('pre'))
            while stack and stack[-1][0] >= indent:
                stack.pop()
            kind, label = mk.group('kind'), mk.group('label')
            scope = label if kind == 'CTE' else '%s%s' % (kind.lower(), label)
            # A marker is a PSEUDO-NODE and a new ROOT, not a child of the
            # node above it. Attaching a CTE body under the plan node that
            # happens to precede it would fold the body's relations into
            # every ancestor's relation set, so a `CTE v1` scanning
            # store_sales would make the plan root's key contain both the
            # main query's store_sales and the body's — two different
            # instances, unified, and every joinrel key above wrong.
            p = Node(kind + ' ' + label, 0, 0, None, None, indent, scope,
                     pseudo=True)
            roots.append(p)
            stack.append((indent, p))
    return roots


def walk(n):
    yield n
    for c in n.children:
        yield from walk(c)


def annotate_relsets(n):
    """Bottom-up: every node's key is the set of base relations beneath it."""
    s = set()
    for c in n.children:
        s |= annotate_relsets(c)
    own = base_relation(n.name, n.scope)
    if own:
        s.add(own)
    n.relset = frozenset(s)
    return s


def qerror(est, actual):
    """Symmetric q-error with both sides floored at 1.

    The floor is not cosmetic: PG clamps every estimate to >= 1
    (`clamp_row_est`), and goopg's `EstimateRows` does the same, so a node
    that truly returns 0 rows would otherwise divide by zero on the side the
    planner cannot express anyway.
    """
    e = max(float(est), 1.0)
    a = max(float(actual), 1.0)
    return max(e / a, a / e)


def collect(roots, want_actual):
    """{relset: {'est':…, 'actual':…, 'node':…}} for one query's plan forest.

    When one key occurs several times (a self-join, a CTE referenced twice,
    a scan repeated under an Append), the occurrence with the LARGEST actual
    is kept: it is the one whose misestimate can actually cost time, and it
    is the one both planners will agree is the interesting instance.
    """
    out = {}
    for root in roots:
        annotate_relsets(root)
        for n in walk(root):
            if n.pseudo or not n.relset:
                continue
            if want_actual and n.arows is None:
                continue                      # `never executed`
            key = tuple(sorted(n.relset))
            rec = {'est': n.erows, 'actual': n.arows, 'node': n.name.split('  ')[0].strip()}
            prev = out.get(key)
            if prev is None:
                out[key] = rec
            elif want_actual and (rec['actual'] or 0) > (prev['actual'] or 0):
                out[key] = rec
            elif not want_actual and rec['est'] > prev['est']:
                out[key] = rec
    return out


def load_pg(pgdir):
    """PG 18.3's estimates per query, keyed identically.

    The PG captures carry no ANALYZE, so only `est` is read from them. Their
    actuals come from goopg's run — same data, same queries, so the actual
    row count of a given relation set is a property of the workload, not of
    the planner that computed it.
    """
    pg = {}
    for fn in os.listdir(pgdir):
        m = re.fullmatch(r'Q(\d+)\.txt', fn)
        if not m:
            continue
        with open(os.path.join(pgdir, fn), errors='replace') as fh:
            pg[int(m.group(1))] = collect(parse(fh.read()), want_actual=False)
    return pg


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('capture')
    ap.add_argument('pgdir')
    ap.add_argument('--json')
    ap.add_argument('--baseline')
    ap.add_argument('--write-baseline')
    # TOL: how much worse than PostgreSQL goopg may be on a node before the
    # node is a finding. 2.0 is deliberately generous — the defects this gate
    # is aimed at are three to four ORDERS OF MAGNITUDE, and a tight
    # tolerance would turn the gate into the change-detector that
    # `make plan-gate MODE=semantic-cost` already is.
    ap.add_argument('--tol', type=float, default=2.0)
    # FLOOR: absolute q-error below which nothing is reported however much
    # worse than PG it is. A node PG estimates exactly (q=1) and goopg
    # estimates at q=3 is not a five-orders-of-magnitude defect.
    ap.add_argument('--floor', type=float, default=10.0)
    ap.add_argument('--top', type=int, default=25)
    args = ap.parse_args()

    with open(args.capture, errors='replace') as fh:
        text = fh.read()
    blocks = re.split(r'^===== Q(\d+) =====$', text, flags=re.M)
    pg = load_pg(args.pgdir)

    findings, scored, unmatched = [], 0, 0
    queries = []
    for i in range(1, len(blocks), 2):
        q = int(blocks[i])
        queries.append(q)
        goopg = collect(parse(blocks[i + 1]), want_actual=True)
        pgq = pg.get(q, {})
        for key, rec in goopg.items():
            scored += 1
            gq = qerror(rec['est'], rec['actual'])
            pgrec = pgq.get(key)
            if pgrec is None:
                unmatched += 1
                pq = None
            else:
                pq = qerror(pgrec['est'], rec['actual'])
            # An unmatched key is scored against an absolute bar only. PG
            # having no node with this relation set usually means the two
            # planners chose different join orders, which is a plan-shape
            # question, not an estimate one — so the bar there is loose.
            bar = args.floor if pq is None else max(args.floor, pq * args.tol)
            if gq > bar:
                findings.append({
                    'q': q, 'key': list(key), 'node': rec['node'],
                    'est': rec['est'], 'actual': rec['actual'],
                    'qerr': round(gq, 1),
                    'pg_est': pgrec['est'] if pgrec else None,
                    'pg_qerr': round(pq, 1) if pq is not None else None,
                })

    findings.sort(key=lambda f: -f['qerr'])
    ids = sorted('Q%d:%s' % (f['q'], '+'.join(f['key'])) for f in findings)

    print('capture:   %s' % args.capture)
    print('pg plans:  %s' % args.pgdir)
    print('queries:   %d   nodes scored: %d   unmatched-in-PG: %d'
          % (len(queries), scored, unmatched))
    print('bar:       qerr > max(%.1f, PG_qerr * %.1f)' % (args.floor, args.tol))
    print('FINDINGS:  %d' % len(findings))
    print()
    hdr = ('%5s %10s %12s %10s %10s %10s  %-22s %s'
           % ('Q', 'qerr', 'goopg est', 'ACTUAL', 'pg est', 'pg qerr', 'node', 'relset'))
    print(hdr)
    print('-' * len(hdr))
    for f in findings[:args.top]:
        print('%5s %10.1f %12d %10.0f %10s %10s  %-22s %s'
              % ('Q%d' % f['q'], f['qerr'], f['est'], f['actual'],
                 f['pg_est'] if f['pg_est'] is not None else '-',
                 ('%.1f' % f['pg_qerr']) if f['pg_qerr'] is not None else '-',
                 f['node'][:22], '+'.join(f['key'])[:60]))
    if len(findings) > args.top:
        print('  ... %d more (see --json)' % (len(findings) - args.top))

    if args.json:
        with open(args.json, 'w') as fh:
            json.dump({'queries': queries, 'scored': scored,
                       'tol': args.tol, 'floor': args.floor,
                       'findings': findings}, fh, indent=1)

    if args.write_baseline:
        with open(args.write_baseline, 'w') as fh:
            fh.write('# EA parity ratchet baseline — one line per finding.\n')
            fh.write('# tol=%.1f floor=%.1f\n' % (args.tol, args.floor))
            for i in ids:
                fh.write(i + '\n')
        print('\nbaseline written: %s (%d entries)' % (args.write_baseline, len(ids)))
        return 0

    if args.baseline:
        with open(args.baseline) as fh:
            base = set(l.strip() for l in fh
                       if l.strip() and not l.startswith('#'))
        cur = set(ids)
        new = sorted(cur - base)
        fixed = sorted(base - cur)
        print()
        print('RATCHET vs %s' % args.baseline)
        print('  baseline findings: %d   current: %d' % (len(base), len(cur)))
        for f in fixed:
            print('  FIXED    %s' % f)
        for f in new:
            print('  NEW      %s' % f)
        if new:
            print('\nEA-RATCHET: FAIL (%d new estimate finding(s))' % len(new))
            return 1
        print('\nEA-RATCHET: PASS%s'
              % (' (%d fixed — re-pin the baseline)' % len(fixed) if fixed else ''))
        return 0

    return 0


if __name__ == '__main__':
    sys.exit(main())
