#!/usr/bin/env python3
"""Count TPC-DS plans whose EXPLAIN text equals PG's, costs ignored.

Usage: scripts/tpcds-text-identity.py GOOPG.plans.txt PG.plans.txt [BASELINE.plans.txt]

Reads the `=== Qn` blocked plan captures the fire-set gate writes
(<label>-candidate.plans.txt / -candidate-pg.plans.txt / -baseline.plans.txt).
The `(cost=... rows=... width=...)` annotation, the header, the dash rule and
the `(N rows)` footer are dropped; every other character must match.
Reports the identical-query count and the number of position-aligned
identical lines (a finer signal for partial progress). The plan-parity
classifier normalises several renderings away (Hash nodes, qual text inside
MATCH), so this is the instrument for EXPLAIN text parity (M0146-0005ck).
"""
import re
import sys


def load(path):
    plans, q = {}, None
    with open(path) as f:
        for line in f:
            if line.startswith('=== '):
                q = line.split()[1]
                plans[q] = []
                continue
            if q is None:
                continue
            line = re.sub(r'  \(cost=[^)]*\)', '', line).rstrip()
            s = line.strip()
            if not s or 'QUERY PLAN' in line or re.match(r'^-+$', s) or re.match(r'^\(\d+ rows?\)$', s):
                continue
            plans[q].append(line)
    return plans


def report(name, got, pg):
    same = sorted((q for q in pg if got.get(q) == pg[q]), key=lambda q: int(q[1:]))
    aligned = sum(1 for q in pg for a, b in zip(got.get(q, []), pg[q]) if a == b)
    print('%s: text-identical %d/%d aligned-lines %d  %s' % (name, len(same), len(pg), aligned, ' '.join(same)))


def main():
    if len(sys.argv) not in (3, 4):
        sys.exit(__doc__)
    pg = load(sys.argv[2])
    if len(sys.argv) == 4:
        report('baseline', load(sys.argv[3]), pg)
    report('candidate', load(sys.argv[1]), pg)


if __name__ == '__main__':
    main()
