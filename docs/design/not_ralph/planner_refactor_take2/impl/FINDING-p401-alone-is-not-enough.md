# P4-01 alone does not make P2-02b free — the 48-byte Datum is co-dominant

**Date:** 2026-09-03. **Status:** changes P4-01's scope and P2-02b's blocker
list. **Method:** the arithmetic check P4-A rev 5 §13.6 named as the decisive
pre-implementation test, run rather than recommended.

## The question

P4-A claims P4-01 (a `PathTarget`, so a join carries only the columns above it
need) is what blocks P2-02b (`work_mem`'s BootVal → PostgreSQL's 4 MB, currently
+23.1 %). If narrowing to Q9's genuinely-needed columns still leaves the
batching join multi-batch at PG's budget, the claim is wrong.

## The measurement

`hashsize.Choose` fed the plan's real row counts at FULL and NARROWED column
counts, at PG's budget (`work_mem` 4 MB × `hash_mem_multiplier` 2) and goopg's
current default (512 MB × 2). The batching join in Q9's 4 MB plan builds
`orders` (1,500,000 rows) under a 2,000,398-row intermediate. Q9 needs 2 of
`orders`' 9 columns (`o_orderkey`, `o_orderdate`) and 9 of the intermediate's 21.

| build side | entry | total | NBatch @ PG 4MB×2 | NBatch @ 512MB×2 |
|---|---:|---:|---:|---:|
| `orders` full (9 cols) | 456 B | 652.3 MB | 128 | 1 |
| `orders` **narrowed (2 cols)** | 120 B | 171.7 MB | **64** | 1 |
| intermediate full (21 cols) | 1032 B | 1968.8 MB | 512 | 4 |
| intermediate **narrowed (9 cols)** | 456 B | 869.9 MB | **256** | 1 |

## The finding

**Narrowing moves the batch count by 2–4×, not to 1.** `orders` narrowed to its
two needed columns still needs **64 batches** at PostgreSQL's `work_mem`, where
PostgreSQL holds its own build in 38 MB and one batch.

So P4-01 is **necessary but not sufficient** for P2-02b. The design's sequencing
(P4-01, then the propagation slices, then P2-02b) will not deliver P2-02b at the
end of it.

## Why: the co-dominant factor is per-column, not column-count

`hashsize.EntryBytes` (`hashsize.go:121-128`) is

    ncols × DatumBytes + RowSliceBytes + avgVarBytes
    ncols × 48         + 24            + avgVarBytes

**48 bytes per column**, whatever the column holds — `DatumBytes`
(`hashsize.go:46`), matched by `estimatedRowBytes` in `spill.go`. A narrowed
2-column `orders` row costs 120 B in goopg's hash table. PostgreSQL stores a
`MinimalTuple` — actual data bytes plus a small header — so the same two columns
(an int key and a date) cost it on the order of 40–60 B including the
`HashJoinTuple` header.

Column count and per-column footprint multiply. P4-01 addresses the first term
only. The second is 07 §6's **48-byte Datum** executor residual, which that
section lists as OUT OF SCOPE for this bundle with a pointer.

That listing should be revisited: it is not a residual behind the planner work,
it is co-dominant with it for this particular goal.

## Consequences

1. **P2-02b's blocker list is wrong in TODO.md and in P4-A §12.** It reads
   "width (P4-01) and the lost Gather (Phase 5)". It should read: the 48-byte
   Datum representation (07 §6), tuple width (P4-01), and the Gather — in that
   order of leverage for the batching term.
2. **P4-01 keeps its own justification** — 2–4× fewer batches is a real gain, and
   §12's width comparison against PostgreSQL stands. What it loses is the claim
   that it unblocks P2-02b.
3. **The cheap check should be repeated for whatever P4-01 actually narrows.**
   The narrowed counts above are hand-derived from Q9's text (2 of 9, 9 of 21).
   Once `neededCols` is consulted at the join levels, re-run this table with the
   collector's real answer before trusting the 2–4× figure.

## What this does not say

It does not say P4-01 is not worth doing, and it does not say the Datum change
is easy — a MinimalTuple-style representation touches every operator that builds
a `Row`. It says only that the two multiply, and that the currently-documented
plan attributes to one of them a result that needs both.
