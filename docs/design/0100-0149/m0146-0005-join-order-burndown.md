# M0146-0005: join-order / candidate-pool divergence burn-down

Status: **IN PROGRESS**. Slice 1 landed 2026-09-25 (`23edfda2e`); slice 2 landed
2026-09-25 (`2962ae22d`). Task:
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

## Slice 2: the partial nested loop re-pays the inner's rescan startup

### Finding

After slice 1, TPC-H Q9 still ended in a partial nested loop into
`orders_pk`, where PG has a Parallel Hash Join against `orders`.

| candidate for the last join | goopg | PG |
|---|---|---|
| partial nested loop into `orders_pk` | **49356.8** | at least 83062 (not even built) |
| partial Parallel Hash Join | 78386.8 | 81246.5 |

- goopg's nested loop was the 43026.6 outer plus only 6330 for 96086 probes,
  i.e. 0.066 per probe. Its parameterized `orders_pk` path costs 0.375..0.431.
- PG's parameterized probe costs 0.4275..0.4663.
- PG's cheapest nested-loop candidate for this joinrel is killed by
  `add_partial_path_precheck` at 83062 or more.

For the PG side, the private instrumented PG needed Q9's tables plus the
reference's composite FK `lineitem_partsupp_fk`. Without that FK PG's own
estimate collapses to 47 rows. With it, the private copy reproduces the
reference plan (`pg-reference-fks.txt`).

The partial nested-loop arm (`addPartialNestLoopPaths`) called
`nestloopCost(..., 0, matRescan)`, with the rescan **startup** hard-coded to
0. `initial_cost_nestloop` charges
`(outer_path_rows - 1) * inner_rescan_start_cost`
(`postgres/src/backend/optimizer/path/costsize.c:3299-3302`), which is the
index descent that a parameterized inner re-pays on every rescan, whether or
not the outer is parallel. The serial NLI arm got this term in R69; the
partial arm repeated the arithmetic inline and missed it. That is a
sibling-path defect (hard-won rule 2).

### Change

- Both arms now call one helper, `nliNestLoopCost`
  (`internal/optimizer/joinpathsnli.go`): `initial_cost_nestloop` plus
  `final_cost_nestloop` for a parameterized or memoized inner, with the
  rescan startup included. The two arms can no longer price the same pair
  differently.
- `TestNLINestLoopCostChargesRescanStartup` pins that a probe's startup is
  re-paid per rescan: the same 0.43 per probe whether the startup is 0.375 or
  0. Before the fix it came to 0.055.

### Measured

- **TPC-H fire set:** match 5 → 5; Q3 and Q10 still match.
  - Q9's first divergence moves from `join-method` at depth 4 to
    `qual-placement` at depth 5. Its top is now PG's Parallel Hash Join on
    `orders`.
  - `join-method` 8 → 7.
  - Q9 on the serial arm: 3.54 s → 1.85 s.
- **TPC-DS fire set:** no timeouts introduced at either scale.

  | scale | match | new matches | `parameterisation` | `join-method` | `join-order` |
  |---|---|---|---|---|---|
  | SF0.25 | 4 → **7** | Q12, Q15, Q20 | 54 → 38 | 66 → 56 | 91 → 84 |
  | SF1 | 6 → **8** | Q7, Q91 | 48 → 41 | 65 → 58 | 85 → 83 |

- **Values:** identical. Acceptance arm 24 MATCH; SF0.25 sweep `MISMATCH=0`,
  with 71 shapes changed.
- **EA-RATCHET:** FAIL, with 51 findings (was 52), of which one is NEW:
  - The new finding is Q7's Gather over
    `customer_demographics+date_dim+item+store_sales` (estimate 47, actual
    1944, `pg_est` null).
  - It is the key class the change FIXED under Q27, the sibling template of
    Q7, now surfacing under Q7 because its plan changed.
  - Per the gate table it gets a ledger row and an owning task
    (M0146-0009a).

Movement: yes. TPC-DS PLAN-PARITY match 4 → 7 at SF0.25 (Q12, Q15, Q20) and
6 → 8 at SF1 (Q7, Q91).

## Remaining records

Per M0146-0001's `m0146-0001-ranked.txt`, still to be worked:
- TPC-H: Q17 and Q19 (`join-method`); Q9's first divergence is now a
  `qual-placement` one.
- TPC-DS: 24 SF0.25 and 21 SF1 records, split between `join-order`,
  `join-method` and presorted-input `sort-strategy`.

Re-run the first-divergence census on each slice's capture before choosing
the next mechanism.
