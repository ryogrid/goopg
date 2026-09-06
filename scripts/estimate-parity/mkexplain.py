#!/usr/bin/env python3
"""Turn one TPC-DS query file into the EXPLAIN script the EA gate captures.

A TPC-DS query file holds one or more statements plus `--` comments. Each
statement gets its OWN `EXPLAIN`, so a multi-statement query (Q14, Q23, Q24,
Q39 …) yields one plan per statement rather than one unparseable blob that
the parity parser would silently drop.

TIMING OFF is deliberate: the gate scores row counts, not milliseconds, and
`TIMING ON` costs a clock_gettime per tuple across a 99-query sweep. The
parity parser therefore has to accept an `actual rows=… loops=…` annotation
with no `time=` pair — requiring the pair matched zero nodes in the whole
corpus and reported a clean gate over an empty population.
"""
import sys


def main(path):
    with open(path, errors='replace') as fh:
        txt = fh.read()
    for stmt in txt.split(';'):
        body = '\n'.join(l for l in stmt.splitlines()
                         if not l.strip().startswith('--'))
        if body.strip():
            print('EXPLAIN (ANALYZE, TIMING OFF, VERBOSE OFF) '
                  + body.strip() + ';')


if __name__ == '__main__':
    main(sys.argv[1])
