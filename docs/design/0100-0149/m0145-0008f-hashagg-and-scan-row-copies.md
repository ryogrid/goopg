# M0145-0008f: per-row copies in the hashed grouping loop and the seq scan

Status: landed 2026-09-24 `11968441a`. Movement: none (executor only, no plan
change). TPC-H Q18 9.53 s → 8.48 s; the remaining gap to PG belongs to
M0145-0008k.
Task: `.ralph/fix_plan.md` M0145-0008f (Kind: impl, Parent: M0145-0008b).
Evidence: `analysis/m0145/m0145-0008f/` (CPU profiles before and after,
timings).

## Measured

Q18's semi-join body, `GROUP BY l_orderkey HAVING sum(l_quantity) > 313`
over 6M rows and 1.5M groups, serial on the private TPC-H lane: goopg took
about 6.8 s, PG 18.3 3.05 s. The profile split into:
- the seq scan (about 50%: deform, a full-row `cloneRowOwned`, page reads);
- GC (about 17%, driven by per-row allocation);
- the grouping loop (about 20%: key slices, `datumKey`, map lookup);
- the numeric sum.

## Changes

1. **Seq scan retention clone** (`cloneRowOwnedPrefix`, `datum.go`). The
   scan deforms only `[0, survivorBound)` (EX1-03a). `cloneRowOwned` still
   deep-copied the whole row, including the undeformed tail, which holds the
   previous tuple's stale datums (the long text column's bytes included). The
   tail is now written as NULL, or kept poisoned when EX1-01's debug flag is
   armed, so a read past the bound still panics there. Measured across two
   runs: clone CPU 3.04 s → 1.78 s, memmove 1.08 s → 0.36 s.
2. **Hashed grouping loop** (`evalGroupKeysScratch`,
   `operators_join_agg.go`):
   - The per-row key `Row` and `[]string` are per-operator scratch buffers.
   - A key value is detached from the arena only when its row founds a new
     group, the only case where it is retained (the M0073-0004 boundary
     moved, not removed).
   - `setGroupKey` returns a single column's `datumKey` directly instead of
     re-copying it through a `strings.Builder`. `datumKey` is type-tagged,
     so it cannot collide with `"__all__"`.
   - The sorted path keeps its own allocations because it holds the previous
     row's key parts (`curParts`).

## Result

| serial, warm | before | after | PG 18.3 |
|---|---|---|---|
| `GROUP BY l_orderkey` | 4.55 s | 3.8 s | 1.86 s |
| … `HAVING sum(l_quantity) > 313` | ~6.8 s | ~6.1 s | 3.05 s |

- Acceptance arm (parallel, values 24 MATCH): Q18 9.53 → 8.48 s, Q13 4.26 →
  3.94 s, total 64.0 → 62.6 s.
- Parallel and partial aggregate tests pass under `-race` (15 cases): each
  worker builds its own operator, so the scratch buffers are not shared.

## What remains (M0145-0008k)

- The scan still allocates a full-width row per tuple at its retention
  boundary (`acquireRow(len(src))`) and deep-copies the survivor window.
  PG hands the aggregate a slot pointing into a pinned shared buffer and
  copies nothing. goopg copies because it releases the page lock before the
  parent reads, and a concurrent page change could tear arena-backed bytes.
- The allocation also drives the GC share.
- `count(*)` over lineitem (4.95 s) is slower than `sum(l_quantity)` (3.2 s)
  on goopg, where PG takes 0.96 s and 1.40 s. A count reads no column at
  all.
