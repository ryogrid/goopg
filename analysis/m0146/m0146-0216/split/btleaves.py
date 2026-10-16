#!/usr/bin/env python3
"""Decode a PG-format btree index file (read-only) and report its leaf geometry.

M0146-0216 slice 3. Both goopg and PostgreSQL write PG's on-disk btree layout:
- page header: pd_lower at offset 12, pd_upper at 14, pd_special at 16;
- line pointers start at offset 24, 4 bytes each;
- BTPageOpaqueData at pd_special: btpo_prev, btpo_next, btpo_level, btpo_flags (BTP_LEAF = 1, BTP_META = 8).
On a non-rightmost page the first line pointer is the high key, which is not data. Leaves are walked left to
right from the leftmost leaf, so two builds of the same insert order can be compared split by split.

usage: btleaves.py <index file> [label]
"""
import struct, sys
from collections import Counter

def pages(path):
    with open(path, 'rb') as f:
        blk = 0
        while True:
            b = f.read(8192)
            if len(b) < 8192:
                return
            yield blk, b
            blk += 1

def main():
    path = sys.argv[1]
    label = sys.argv[2] if len(sys.argv) > 2 else path
    leaves = {}
    free = {}
    for blk, b in pages(path):
        lower, upper, special = struct.unpack_from('<HHH', b, 12)
        if special < 24 or special > 8192 - 16:
            continue
        prev, nxt, level, flags = struct.unpack_from('<IIIH', b, special)
        if flags & 8 or not flags & 1:  # meta page, or not a leaf
            continue
        n = (lower - 24) // 4
        data = n - (1 if nxt != 0 else 0)
        leaves[blk] = (prev, nxt, data)
        free[blk] = upper - lower
    # leftmost leaf: prev == 0
    start = [b for b, (p, _, _) in leaves.items() if p == 0]
    order = []
    if start:
        cur = start[0]
        seen = set()
        while cur and cur in leaves and cur not in seen:
            seen.add(cur)
            order.append(cur)
            cur = leaves[cur][1]
    counts = [leaves[b][2] for b in order]
    total = sum(counts)
    hist = Counter(counts)
    print(f'{label}: leaves={len(leaves)} chained={len(order)} tuples={total} '
          f'mean_items={total / max(len(order), 1):.1f} mean_free={sum(free[b] for b in order) / max(len(order), 1):.0f}')
    print('  items-per-leaf histogram (top 8):', ', '.join(f'{k}:{v}' for k, v in hist.most_common(8)))
    if len(sys.argv) > 3:
        with open(sys.argv[3], 'w') as out:
            out.write('\n'.join(str(c) for c in counts) + '\n')

if __name__ == '__main__':
    main()
