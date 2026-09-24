#!/usr/bin/env python3
"""M0146-0001: map each first-divergence record to the M0146 task that owns
its decision point, and rank tasks by records across the three corpora.
Input: the --verbose census files; output: ranked table + per-record map."""
import re, sys, collections

RX = re.compile(r'^(Q\S+) depth=(\d+) \[([a-z-]+)\] under (.*?): PG (.*?) \| goopg (.*)$')

def owner(cat, parent, pg, gp):
    if cat == 'error':
        return 'n/a (both error: dsqgen artefact)'
    if 'Incremental Sort' in pg:
        return 'M0146-0006 incremental sort'
    if gp.startswith('CTE ') or 'CTE' in parent and cat == 'join-order' and False:
        return 'M0146-0007 inline_cte'
    if gp.startswith('CTE '):
        return 'M0146-0007 inline_cte'
    if 'MixedAggregate' in pg:
        return 'UNOWNED MixedAggregate (grouping sets)'
    if pg == 'Group' or pg.startswith('Group ') and 'Aggregate' not in pg:
        return 'UNOWNED Group node (grouping without aggregates)'
    if any(k in pg for k in ('Finalize', 'Partial')) or (cat in ('sort-strategy', 'parallelism') and pg.startswith('Gather Merge')):
        return 'M0146-0003 row-emitting PartialAgg / parallel aggregation'
    if cat in ('sort-strategy', 'parallelism') and (gp.startswith('Gather') or gp.startswith('Gather Merge')):
        return 'M0146-0002a parallel over-election'
    if cat == 'sort-strategy' and pg.startswith('Nested Loop') and gp.startswith('Sort'):
        return 'M0146-0005 join order / presorted input'
    if cat in ('join-method', 'join-order'):
        return 'M0146-0005 join order / method'
    if cat == 'parallelism':
        return 'M0146-0002a parallel over-election'
    if cat == 'qual-placement':
        return 'M0146-0012 restriction placement'
    if cat == 'parameterisation':
        return 'M0146-0011 lateral / parameterised paths'
    if cat == 'scan-type' and 'Index Only Scan' in pg:
        return 'UNOWNED index-only scan (join inner / pkey probe)'
    if cat == 'aggregation-strategy':
        return 'M0146-0009 statistics (hash vs sort grouping election)'
    return f'UNOWNED {cat}'

corpora = sys.argv[1:]
counts = collections.defaultdict(lambda: collections.Counter())
recs = []
for path in corpora:
    name = path.split('census-')[-1].replace('.txt', '')
    for line in open(path):
        m = RX.match(line.strip())
        if not m:
            continue
        q, depth, cat, parent, pg, gp = m.groups()
        o = owner(cat, parent, pg.strip(), gp.strip())
        counts[o][name] += 1
        recs.append((name, q, cat, o))
names = [p.split('census-')[-1].replace('.txt', '') for p in corpora]
rows = sorted(counts.items(), key=lambda kv: -sum(kv[1].values()))
print(f"{'owner':62s} " + ' '.join(f'{n:>11s}' for n in names) + '  total')
for o, c in rows:
    print(f"{o:62s} " + ' '.join(f'{c[n]:>11d}' for n in names) + f"  {sum(c.values()):5d}")
print()
for r in recs:
    print('\t'.join(r))
