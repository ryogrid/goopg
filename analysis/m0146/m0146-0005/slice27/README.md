# M0146-0005 slice 27 — parameterized bitmap-probe partial NLI: Gather admission

Q55's post-reload divergence: PostgreSQL elects an item-outer partial
nested loop — `Gather -> Nested Loop(-> Nested Loop(Parallel Seq Scan
item -> Bitmap Heap Scan store_sales), Memoize -> Index Scan date_dim)` —
while goopg wrapped a serial `Nested Loop` around
`Gather(Parallel Hash Join store_sales ⋈ date_dim)` and probed `item`.

## Root cause

Not enumeration, costing, or adjudication:

- The item partial seq scan (`1374.35`, 99 rows/worker) and the
  parameterized `store_sales` bitmap probe (`143.58` per rescan) were
  both already filed.
- The fused partial NL `NL(item_partial, ss bitmap probe)` existed at
  `15632.19`; the 3-rel partial NL `NL(NL(item, ss), Memoize(dd))` sat at
  the head of the `{0,1,2}` partial pathlist at **16255.65 — cheaper than
  every serial candidate** (elected chain previously 19023).
- `cpgather rel={date_dim+item+store_sales} partials=1
  verdict=admitted` ran, yet **no `Gather` was filed**: `makeGatherPath`
  → `gatherSubpathIsRunnable` → `partialPathDrivingKind` classified the
  partial NL head as `PathPrebuilt` because its probe arm admitted only
  `PathIndexScan` inners — a parameterized `PathBitmapHeapScan` failed
  the shape check.

## Change (fail-closed, two surfaces move together)

- `internal/optimizer/gatherpaths.go` — `partialPathDrivingKind`'s
  PathNestLoop probe arm now admits `PathBitmapHeapScan` when it has
  exactly one child and that child is a `PathBitmapIndexScan`; still
  requires `IndexClauses`. Memoize wrapping is unwrapped once but a
  *memoized bitmap* probe stays refused (createPlan would unwrap it into
  `createNestLoopBitmapJoinPlan` and lose the priced cache).
- `internal/optimizer/parallel.go` — `NestedLoopIndexJoinIsPartialCapable`
  gains `nliBitmapProbeIsPartialProbe`: `*BitmapHeapScan` whose `Outer`
  is exactly one `*BitmapIndexScan` with a non-nil index and probe keys
  (`Key` or `Keys`). Kept separate from `lateralProbeIsPartialProbe` —
  the decomposed-lateral executor only supports index/index-only probes.
- No executor code change: every walk (`attachAll` NLI arms,
  `collectBitmapScans`, `drivingScan`, `HasBitmapScan` handling) reads
  the same exported predicate; NLI claims attach to the outer side only,
  so the bitmap inner stays private and serial per worker
  (`bitmapHeapScanOp.BindOuter`/`Rescan` rebuilds a private TID bitmap
  per outer row, `pbm == nil`).

## Executor-safety argument

`attachAll`'s return is advisory; safety rests on planner/executor
predicate agreement. With the single shared predicate widened:

- Outer-side claim descent is unchanged — the `item` partial scan still
  drives the partitioning.
- `collectBitmapScans` descends the NLI's outer only, so the inner
  bitmap is never collected into `prebuildBitmap`'s shared claim set.
- `parallelChildren` does expose the inner (so `HasBitmapScan` may
  trigger a throwaway prebuild that finds zero outer targets — wasted
  work only, never a shared TIDBitmap reaching the inner probe).

## Tests

- Optimizer (`partial_nli_memoize_test.go`): capability predicate +
  walk-agreement + `partialPathDrivingKind` cases — admit
  bitmap-probe NLI; refuse memoized bitmap probe, multi-child bitmap
  heap, non-index-scan child, unparameterized bitmap.
- Executor (`parallel_nli_memoize_test.go`): admission arm, a
  `collectBitmapScans` inner-isolation pin (inner bitmap never collected),
  and a serial-vs-parallel identity test at 1/2/4 workers on a
  hash/merge-disabled bitmap-inner NLI (no N-copy over-counting).

## Result — Q55 elects PG's shape

goopg `EXPLAIN` on the private SF0.25 clone (`:5590`):

```
Limit -> Sort -> Finalize GroupAggregate -> Gather Merge (1 worker)
  -> Partial GroupAggregate -> Sort
    -> Nested Loop
      -> Nested Loop
        -> Parallel Seq Scan on item  (i_manager_id = 52)
        -> Bitmap Heap Scan on store_sales  Recheck (ss_item_sk = i_item_sk)
          -> Bitmap Index Scan on store_sales_pkey
      -> Memoize -> Index Scan date_dim_pkey
```

Shape-identical to PG 18.3's plan modulo goopg's deliberate
Finalize/Partial GroupAggregate + Gather Merge split (M0146-0027
slice 3; PG sorts-then-aggregates serially over the Gather).

- Row set: `rows=68 ck=fe343d36717a4fb5` — exact oracle match
  (`55|OK|68|fe343d36717a4fb5`).
- Elected `{0,1,2}` gather total `17255.95` vs prior elected `19023`.
- Sweep collateral (all checksums clean): Q3/Q37/Q75/Q76 take the same
  gather over their bitmap-probe NLs; Q61/Q49 reprice.

## Gates

- `go test ./internal/optimizer ./internal/executor` — pass.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — pass.
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=33).
- `scripts/tpcds-sf025-regression.sh sweep` — PASS=96 MISMATCH=0
  CKMISMATCH=0; plans changed 7 (Q3 Q37 Q49 Q55 Q61 Q75 Q76),
  verdict-changes none.

## Reproduction

- Clone `tmp/m01460027s3-data-tpcds-sf025` on `:5590`, db `postgres`,
  `GOOPG_DEBUG_PATH_PRODUCERS=1` → `tmp/m0146-0005s27/server.log`
  (excerpts: `goopg-dppath-excerpts.txt`).
- Plans: `q55-goopg-explain.txt` (new), `q55-pg-explain.txt`
  (PG `tpcds025@:65438`); result checksum `q55-checksum.txt`.
