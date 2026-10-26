# M0147-0022 — complete and promote `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 0 (promote-or-delete).
Lever count 21 → 20 (`scripts/lever-count.sh`).

## The lever

M0139-0007b ([doc](m0139-0007b-memoize-entry-bytes-absorption.md)) ported PG's `cost_memoize_rescan` cache-entry
currency into `costMemoizeRescan` (`internal/optimizer/joinpathsmemoize.go`):

- `relation_byte_size(tuples, width)`, as `pgRelationByteSize`;
- `ExecEstimateCacheEntryOverheadBytes(tuples)` (`nodeMemoize.c`), as `pgMemoizeEntryOverheadBytes`.

The ON arm of `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` (default off) used them. The OFF arm priced goopg's kvcache entry
(`hashsize.EntryBytes` + `memoizeEntryOverheadBytes`).

**"Complete" was already done.** The task asked first for PG's per-key `get_expr_width` term
(`postgres/src/backend/optimizer/path/costsize.c`, the `param_exprs` loop in `cost_memoize_rescan`; ledger line 2189).
M0139-0007c had already ported it: `costMemoizeRescan` takes `keyWidth`, the caller's `memoizeKeyWidths` sum, and
uses it whenever the PG tuple-byte term is used. The task text predates that. No new port was needed.

## Measurement

The ON captures on the HEAD binary (`tmp/m0147-0022/`, not committed) were compared with this loop's OFF census
(`routing-20261026e`):

| corpus | Memoize nodes in plans | ON vs OFF goopg plans | ON PLAN-PARITY |
|---|---|---|---|
| TPC-DS SF0.25 | 41 | byte-identical (0 diff lines, costs included) | 99 / 99 |
| TPC-DS SF1 | 26 | byte-identical (0 diff lines) | 99 / 99 |

At the shipped work_mem, no cache comes near filling in either currency (M0139-0007b saw the same: every corpus
Memoize has `rows=1`). The currency therefore never moves `est_cache_entries` below `ndistinct`, so the arm is
plan-neutral. R3 promotes the PG arm.

## Removed / changed

- `memoize_pgentrybytes.go`: the switch, its env read, `pgMemoizeEntryBytesCostFromEnv`,
  `pgMemoizeEntryBytesCostEnabled` and `setPGMemoizeEntryBytesCostForTest`. The PG struct-size constants and
  `pgMemoizeEntryOverheadBytes` are kept.
- `costMemoizeRescan`: the PG currency is used whenever `pgRelationByteSize` succeeds. When it declines (unknown
  width), both terms fall back to goopg's ruler together, as before. `pgRelationByteSize` is kept.
- `flaglabels.go`: the variable moves to `flagProvenanceRetired` (`retired(M0147-0022)`), and `planner-flags.env`
  is regenerated.

`deadcode ./cmd/...` was diffed before and after. Only the deleted test setter left its list.

## Tests

- `TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy` is replaced by
  `TestCostMemoizeRescanUnknownWidthFallsBackToGoopgCurrency`. The fallback (width 0) must ignore `keyWidth`, and a
  known width must price differently from the fallback.
- The width and key-width currency tests drop their switch pins and their OFF-arm halves, so they now test the
  shipped default.
- The struct-size pin is unchanged.

## Inert-to-plans proof

The capture diff above shows 0 changed lines on both TPC-DS corpora. The fire-set A/B on the promoted tree reports
`fires=none`. No script, CI config or env file sets the variable.

## Gates

See the M0147-0022 entry in `.ralph/fix_plan.md`.
