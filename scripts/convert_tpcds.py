#!/usr/bin/env python3
"""Convert TPC-DS pipe-delimited .dat files to tab-delimited COPY TEXT format.
Empty fields are replaced with the PG NULL sentinel."""
import sys

NULL = chr(92) + 'N'  # literal backslash-N (\N)

for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    line = line.rstrip('\n\r')
    if line.endswith('|'):
        line = line[:-1]
    cols = [NULL if c == '' else c for c in line.split('|')]
    print('\t'.join(cols))
