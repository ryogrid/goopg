# M0147-0035 — retire `GOOPG_JOIN_SLOT_CHAIN` (probe-side slot chaining switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 16 → 15 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

M0127-P1.1 binds a hash join's probe child slot directly as a source of the join's emit `VirtualSlot`
(`joinOp.bindProbe`, `internal/executor/operators_join_agg.go`), instead of flattening it into a Row on every pull.
That is the shape of PG's slot passing: the join reads the outer tuple slot in place. `GOOPG_JOIN_SLOT_CHAIN=off`
(default on) forced the old copy on every probe row.

Unlike the M0147-0034 prefilter, the OFF arm was not a test oracle. The worker-path identity tests compare both arms
against a **serial baseline**. The copy path itself is not deleted: it stays the fallback whenever the child's slot is
narrower than the columns the emit slot reads (`TestProbeChainOuterOnlyWidthFallback`, and the fresh-slot and
rotating probe shapes). So phase 2 removes only the switch.

## Removed / changed

- `operators_join_agg.go`:
  - `joinSlotChainOn`, its `init` env read, `SetJoinSlotChainEnabled` (exported, test-only),
    `joinSlotChainEnabled`, and the `os` and `sync/atomic` imports.
  - The `joinOp.lazyChainProbe` field, which held the flag per join. `bindProbe` now chains whenever the child's
    slot is wide enough.
  - The `bindProbe` comment now names one fallback condition.

`deadcode ./cmd/...` was diffed before and after. Only `SetJoinSlotChainEnabled` left its list, and nothing new
appeared.

## Tests

- `TestProbeChainKillSwitch` is deleted: it pinned the switch.
- `BenchmarkProbeSeam` drops its `off` sub-benchmark.
- `TestJoinSeamWorkerTransferIndependence` and the gathered-corpus worker test drop their "killswitch" arm. Both
  still compare the chained seam with the serial baseline.
- The worker-build pin drops its `lazyChainProbe` check. It still asserts that the probe source is the child's slot,
  not the copy fallback.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to
chaining, which is the only behaviour left. This executor change is values-checked by the sf025 sweep and the
acceptance arm.

## Gates

See the M0147-0035 entry in `.ralph/fix_plan.md`.
