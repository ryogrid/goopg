# M0146-0005: join-order / candidate-pool divergence burn-down

Status: **IN PROGRESS**. Slice 1 landed 2026-09-25 (`23edfda2e`). Task:
`.ralph/fix_plan.md` M0146-0005 (Kind: impl, Parent: none). Evidence:
`analysis/m0146/m0146-0005/`.

The family owns 50 first-divergence records in M0146-0001's census:
- TPC-H: 5;
- TPC-DS SF0.25: 24;
- TPC-DS SF1: 21.

Each slice takes one mechanism, measured with the per-candidate traces:
goopg's `DPPATH` (`GOOPG_PGSHAPED_DP_TRACE=1`) against PG's `PLANCAND`
(the M0144-0005 instrumented build).

## Slice 1: Memoize `calls` is the outer path's rows

### Finding

TPC-H Q3, Q9 and Q10 diverge the same way: PG has a Parallel Hash Join where
goopg has a nested loop. On Q3, `orders ⋈ customer` offers both candidates.

| candidate | goopg total | PG total |
|---|---|---|
| partial Parallel Hash Join | 38260.15 | 37618.5 |
| partial nested loop, Memoize over `customer_pk` | **37991.19** | ≥ 185294 |

goopg's nested loop was about 5x cheaper than PG's, and it won. The distinct
count was not the cause: goopg's ANALYZE gives 93573 for `o_custkey` and
PG's gives 95137. A debug print of `costMemoizeRescan`'s inputs, not
committed and recorded in `goopg-q3-memoize-inputs-before.txt`, showed
`calls=735593`. That is the whole `orders` rel's row count, which put the hit
ratio at 0.87.

PG passes `outer_path->rows`
(`postgres/src/backend/optimizer/path/joinpath.c:812-819`, stored as
`mpath->calls` at `pathnode.c:1693` and read by `cost_memoize_rescan`,
`costsize.c:2549`). For a partial outer that is the per-worker count, about
180K, so each worker's cache is priced by the probes that worker actually
makes. PG's hit ratio here is about 0.47.

### Change

`getMemoizePath` (`internal/optimizer/joinpathsmemoize.go`) passes
`outerPath.Rows` as `calls`. The `< 2 rows` gate keeps reading the rel
(`outer_path->parent->rows`, joinpath.c:696), as PG's does.
`TestMemoizeCallsAreTheOuterPathRows` pins that a per-worker outer prices its
cache on its own rows.

### Measured

- **TPC-H fire set:** Q3, Q9, Q10 and Q18 change. **Q3 and Q10 now match PG
  exactly**: PLAN-PARITY match 3 → 5.
  - `join-order` 17 → 15, `join-method` 10 → 8, `parameterisation` 8 → 4.
  - Q9 still diverges at a nested loop under the Partial HashAggregate; that
    is the next slice's candidate.
- **TPC-DS fire set:** 52 queries at SF0.25 and 37 at SF1 change plans;
  Memoize nested loops are everywhere in TPC-DS.
  - Parity is unchanged: match 4 at SF0.25 and 6 at SF1.
  - `parameterisation` drops by 1 at each scale.
  - No timeouts introduced.
- **Values:** identical. Acceptance arm 24 MATCH; SF0.25 sweep
  `MISMATCH=0 TIMEOUT=0`.
- **Serial arm times:** Q3 2.96 s → 1.56 s, Q10 6.80 s → 1.58 s.
- **EA-RATCHET:** 52 → 52.

Movement: yes. TPC-H PLAN-PARITY match 3 → 5 (Q3, Q10).

### Not ported (ledgered)

PG sizes the key's distinct count with
`estimate_num_groups(param_exprs, calls)`, which also scales for the outer
rel's restriction selectivity. goopg's `memoizeKeyNDistinct` reads the
column's `get_variable_numdistinct` and only clamps it to `calls`.

## Remaining records

Per M0146-0001's `m0146-0001-ranked.txt`, still to be worked:
- TPC-H: Q9, Q17, Q19 (`join-method`).
- TPC-DS: 24 SF0.25 and 21 SF1 records, split between `join-order`,
  `join-method` and presorted-input `sort-strategy`.

Re-run the first-divergence census on each slice's capture before choosing
the next mechanism.
