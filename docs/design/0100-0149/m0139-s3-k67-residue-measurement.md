# M0139-S3 — measure the residue against K67's floor

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: recon (measurement only; no production diff, per the plan-parity
harness's recon-task carve-out in `AGENT.md` §"Plan-parity harness")

## Goal

K67 (`docs/design/not_ralph/plan_parity_fix_take2/r70-hash-footprint/SCOPE.md`)
estimated, before any join-leg narrowing existed, that TPC-H Q12's hash-build
side would still be **72 B/row → 103 MB** even "narrowed to one column" — and
still spills at `work_mem=64MB`, where PG's own MinimalTuple is 22 B/row →
31 MB. That figure was **analytical**, not measured: `EntryBytes(1, 0) =
1·48 + 24 + 0 = 72` (`internal/executor/hashsize/hashsize.go:144`), i.e. it
assumed the narrowed build side keeps exactly one column and that column
costs zero extra bytes beyond the fixed 48-byte Datum box. Now that M0139-S1
and -S2 have landed real narrowing at the join-leg hook, S3's job is to
**measure** what Q12 actually narrows to at HEAD and report the real number,
not re-derive the analytical anchor.

## Method

A throwaway probe (`internal/testutil/tpch/zz_probe_m0139s3_test.go`,
deleted before commit, same pattern S1/S2 used) built a **private, disposable
cluster from HEAD** via `internal/testutil/cluster` + the existing
`scaleLoader` (`tpch_scale_run_test.go`) — never touching the shared,
peer-owned `:65433` TPC-H bench server. It loaded 20,000 orders (~68,000
lineitems, real TPC-H DDL from `tpch.DDL()`, canonical categorical
vocabularies), ran `ANALYZE`, then:

1. Read `pg_stats.avg_width` for every `orders`/`lineitem` column — the exact
   statistic `entrywidth.go`'s `buildAvgVarBytes` sums over a retained
   schema, i.e. the real input to `hashsize.EntryBytes`.
2. Ran `EXPLAIN (VERBOSE, COSTS OFF)` and `EXPLAIN (ANALYZE, VERBOSE)` on the
   canonical Q12 text (`internal/testutil/tpch/tpch.go`'s query 12) to read
   the actual retained-column list at the Hash Join node and the executor's
   own measured `Buckets`/`Batches`/`Memory Usage`.

Small scale is not a validity gap here: `avg_width` for a column drawn from a
fixed small vocabulary (`o_orderpriority`'s 5 values, `l_shipmode`'s 7) is
scale-invariant, and the retained-column *list* is a query-structure property,
independent of row count.

## What HEAD actually does (measured, not assumed)

The Hash Join's own `Output:` line reads:

```
Output: l_shipdate, l_orderkey, l_commitdate, l_receiptdate, l_shipmode, o_orderkey, o_orderpriority
```

— down from all 16 `lineitem` and all 9 `orders` columns at the two Seq Scans
below it. (The scans' own `Output:` lines still list every column; narrowing
happens at the join-leg hook above the scan, not inside it — expected, matches
S1/S2's design.) This confirms the hook **does** fire on Q12 at HEAD. A
first check against the shared `:65433` bench server showed **no** narrowing
on Q12 (`orders`/`lineitem` full-width straight into the Hash Join) — that
binary is stale relative to this HEAD and was not used for measurement; the
private cluster is authoritative.

`orders` is the (non-parallel) build/inner side; its retained columns above
the join are exactly `{o_orderkey, o_orderpriority}` — **2 columns, not
K67's assumed 1**. `o_orderkey` cannot be dropped even though it is never
referenced above the join: the hash entry must retain the join key itself
for probe-time equality verification, on top of whatever the aggregate needs
downstream (`o_orderpriority`, consumed by the `CASE` expressions). K67's
"narrow to one column" floor never accounted for this — the join key is
unconditionally part of `ncols`, not a separate free dimension.

Measured `pg_stats.avg_width` on the private cluster (20,000 orders):

| column | avg_width |
|---|---|
| `o_orderkey` (bare `NUMERIC`, `dscale=0`) | 0 |
| `o_orderpriority` (`CHAR(15)`, 5-value vocabulary) | 8.3701 |

(Both numeric and text-ish columns come from HammerDB's actual DDL —
`o_orderkey` is `NUMERIC` with no declared precision/scale, not `INTEGER`;
see `hammerdb_tpch_integer_keys_are_numeric` — but `dscale=0` small values
still cost 0 extra bytes beyond the 48-byte Datum box, so K67's `avgVarBytes
≈ 0` assumption for the join key alone was fine. It was the *payload*
column's non-zero width, and the join key's mandatory co-retention, that K67
missed.)

## The number

```
EntryBytes(ncols=2, avgVarBytes=8.3701) = 2·48 + 24 + 8.3701 = 128.37 B/row
```

Cross-checked directly against the executor's own `EXPLAIN (ANALYZE,
VERBOSE)` output for the same query — **not** a second independent
derivation, a consistency check on the same mechanism:

```
Buckets: 32768  Batches: 1  Memory Usage: 4044kB  Build Time: 16.864 ms
```

for 20,000 build rows. Subtracting the bucket-table term
(`MapSlotBytes=48` × 32,768 buckets = 1536 KB, `hashsize.go:80`) from the
4044 KB total leaves 2508 KB of entry storage ÷ 20,000 rows = **128.4 B/row**
— matching the formula to within rounding.

Extrapolated to SF=1's real 1,500,000 orders (Q12 has no predicate on
`orders`, so the build side is unfiltered) using the same accounting K67
used (entries only, no bucket-table term, for apples-to-apples comparison):

```
1,500,000 × 128.37 B ≈ 192.6 MB
```

**vs. K67's stated 72 B/row → 103 MB, and PG's 22 B/row → 31 MB.**

## Verdict

Landing S1/S2's real narrowing did **not** close the gap to K67's floor —
the measured post-pushdown residue (**≈128.4 B/row → ≈193 MB**) is *worse*
than the anchor the campaign had been budgeting against, not better. The
mechanism narrows correctly (confirmed live: 9→2 columns on the build side),
but K67's "one column, 72 B/row" was itself too optimistic by construction —
it assumed a hash entry could shed the join key and that the one surviving
payload column costs nothing beyond the 48-byte Datum box. Real TPC-H content
never gives you that: a hash entry always retains its join key, and even the
narrowest possible payload here (`o_orderpriority`) still carries ~8.4 bytes
of real text. Both engines' floors have residues once narrowing is applied
(goopg 128 B/row and PG's 22 B/row are both non-zero), but goopg's residue is
**~5.8× PG's, not the ~3.3× K67 anchored the campaign against.**

At `work_mem=64MB` × `hash_mem_multiplier=2.0` (`hashsize.go:294`, PG 18's
own default) = 128 MB effective budget, 192.6 MB of entries alone already
exceeds it (`NBatch` rounds up to 2), confirming the spill K67 predicted —
goopg still spills where PG (31 MB, comfortably under even the un-multiplied
64 MB) does not.

## What this does and does not decide

This is a **measurement**, not a proposal. It feeds directly into
M0139-0006 ("put the packed-retention decision to the owner"): the residue is
now a real number (128.4 B/row measured, not 72 B/row assumed), which
strengthens rather than weakens the case that projection pushdown alone
(M0139's whole scope) is necessary but not sufficient — the packed-retention
question (`minimize_datum`, **NOT APPROVED TO START**) is not resolved or
advanced by this task, only better informed. No production code changed.

## Gates

Recon task: no production diff. `go test -run TestZZProbeM0139S3Residue
./internal/testutil/tpch/` passed on the throwaway probe (deleted before
commit — see M0139-S1/S2 precedent, `AGENT.md` §"Plan-parity harness" →
"Way of working"). No ledger row: no PG-incompatibility surfaced, and the
finding itself is checked into this design doc rather than left as a forward
reference.
