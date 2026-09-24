# M0145-0008p: a scan under a zero-consumer aggregate deforms nothing

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008p (Kind:
impl, Parent: M0145-0008k). Recon: `m0145-0008k-seqscan-row-copy-recon.md`.
Evidence: `analysis/m0145/m0145-0008p/`.

## Defect

`count(*)` over TPC-H lineitem took ~4.8 s serial, against ~3.2 s for
`sum(l_quantity)` and 0.96 s on PG 18.3. The EX1 deform-bound walk
(`internal/executor/scan_deform.go`) had one value for two different facts:
- `deformBoundNone` at the plan root: nothing has been folded yet, and the
  rows go to the client whole, so the leaf deforms every column;
- `deformBoundNone` after an Aggregate whose arms fold no reference
  (`count(*)`): the consumer reads no column at all.

`effectiveDeformBound` resolved both to full width. So the only query that
reads nothing deformed and cloned all 16 columns.

PG deforms nothing here. The aggregate never touches the slot, so
`slot_getsomeattrs` is never called (`postgres/src/backend/executor/execTuples.c`).

## What changed

- **A new bound value, `deformBoundZero` (-2).** The Aggregate arm of
  `deformBoundBelow` starts its fresh bound there instead of at
  `deformBoundNone`. A folded reference raises it to that column index, so
  `count(*) … GROUP BY c2` still deforms through c2.
- **The algebra is unchanged.** Zero sorts below None, so plain `max` still
  unions bounds:
  - every `incoming < 0` mapping arm (the join, semi/anti and NLI maps)
    treats Zero like None, which is full width and so the safe side;
  - Zero ∪ None is None.
  A join under a `count(*)` therefore keeps its per-side key bounds and
  loses no correctness. The side that no key reads is still deformed in
  full, as before.
- **Finalize aggregates are excluded.** A Finalize aggregate reads its
  child's partial-state columns without an arm. Its child is always Gather →
  Partial Aggregate, which resets the bound again, but the arm still starts
  at None for Finalize (`AggModeFinal`), so no future shape can starve it.
- **Leaf resolution.** `effectiveDeformBound` stamps `deformWidthZero` (-1)
  for Zero. A stamp of 0 already means "unset, full width" for scans built
  outside `Build`, so zero width needed its own stamp.
  - The SeqScan resolves stamps through `seqScanSurvivorWidth`: survivor
    window 0. No column is deformed or detoasted, and
    `cloneRowOwnedPrefix(row, 0)` hands up a correctly shaped all-NULL row
    (poisoned when the debug flag is armed). Visibility, SSI and the
    absorbed-qual paths are unchanged.
  - The absorbed qual is covered. The walk folds the Filter predicate
    before the scan is built, so a qual that reads a column never leaves the
    bound at Zero.
- **Sibling leaves, audited.** The index and bitmap heap leaves read any
  stamp ≤ 0 as full width, so they keep full width for a Zero stamp.
  - A Zero bound cannot reach them in practice: `deformIndexLeafBound`
    unions the index key columns, and `deformBitmapLeafBound` folds the
    recheck quals.
  - The Gather worker closure captures the bound `deformBoundBelow` returns,
    so a parallel `count(*)` narrows in every worker.

## Tests

- `TestScanDeformZeroConsumerChains` pins the synthetic shapes:
  - `count(*)` over a bare scan, a Limit and a Gather stamps width 0;
  - a Filter raises the width to its reference, and so does a group key;
  - Finalize stays at None;
  - Zero ∪ None widens to full.
- `TestScanDeformZeroConsumerExecution` runs `count(*)`, `count(*) WHERE`,
  and `count(*), 7` on a real table with the tail poison armed. Any read of
  an undeformed column would panic. The counts must equal the full-deform
  `count(h)` variants.
- `TestEffectiveDeformBoundEdges` gains the Zero row. `seqLeafBound` now
  reads stamps through `seqScanSurvivorWidth`.

## Measured

TPC-H SF1 lineitem, a private copy of the acceptance-arm clone on :5534, HEAD
`b1f93fb81` against the candidate. Each arm is a fresh server, the arms
alternate head/cand/head/cand, and the warm runs are shown.

| query | HEAD | candidate |
|---|---|---|
| `count(*)`, serial | 4.75–4.98 s | 1.46–1.56 s |
| `count(*)`, 2 workers | 1.71–1.83 s | 0.56–0.58 s |
| `sum(l_quantity)`, serial | 3.23–3.40 s | 3.33–3.54 s (unchanged) |
| `sum(l_quantity)`, 2 workers | 0.97–1.02 s | 0.98–1.01 s (unchanged) |

Values are identical: 6001726 and 153055315 on both arms.

Serial `count(*)` is now 1.5× PG's 0.96 s, down from 5×. What remains is the
per-tuple visibility and slot work, plus the all-NULL row allocation that a
zero-copy slot would remove (M0145-0008q, then the slot).

## Gates (staged tree)

- units: PASS.
- `tpch-spotcheck`: PASS (Q12=2, Q13=33).
- `tpcds-sf025 sweep`: PASS=96, PLAN-SHAPE same=99 changed=0.
- acceptance arm: 24 MATCH on values. TPC-H has no ungrouped `count(*)`, so
  no acceptance time is expected to move.
- ea-ratchet: 52/52.
- `TestPort_RegressSuite` (full): the same two failures as a clean HEAD
  worktree, `portals_p2` and `union`. Both are pre-existing wrong results,
  filed as S2 escalations M0145-0008r (bitmap scan over an unproven partial
  index) and M0145-0008s (HashSetOp per worker over partial inputs).

Movement: none. This is an executor-only change and no plan moved. The
count(*) times above are outside the parity instruments.

## Not ported (ledgered)

- PG hands the aggregate a buffer tuple without copying it
  (`ExecStoreBufferHeapTuple`). goopg still allocates one all-NULL row per
  tuple at the retention boundary. Removing that needs the pin-held slot,
  which needs M0145-0008q's cleanup-lock discipline first.
- The side of a join that no key reads is still deformed in full under a
  `count(*)`. A per-side Zero would need the join mapping arms to tell
  "nothing above" (Zero) apart from "root" (None).
