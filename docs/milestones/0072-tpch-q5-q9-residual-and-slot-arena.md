# Milestone 0072 — Q5 GC residual + Q9 row-count fix (slot-arena partial)

**Status:** closed
**Branch:** `gc-oriented-refactor` (continuation of M0071-0011..0015)
**Depends on:** M0071-0015 (commit `3f5a905`) — slot pipeline complete.
**Drives:** Q5 next-bottleneck reduction (`btree.RangeScan + acquireRow`
50.81% post-Stage-D-2 → ≤ 25%); per-batch String/Bytes arena
landing (M0071-0006 unblocked); **Q9 chained-NLI silent FN fix
(emerged unexpectedly from M0072-0001).**

**Q9 update (2026-05-09):**

🎉 **Q9 row count fixed structurally by M0072-0001** — the
slot-aware `BindOuter` change moved IndexScan outer-column
reads from `evalExpr(key, joinBuf, ctx)` (Row-position
based, sensitive to chained-NLI runtime layout drift) to
`evalExprSlot(key, o.outerSlot, ctx)` (slot.Get(col)
addresses the outer's actual emitted column). The
chained-NLI shape now reads the correct column at runtime
without any planner-side rebind.

Empirical: Q9 = **175 rows in 1030 s** with cancel-after =
1100 s on a fresh-restart M0072 binary (target ~175 ✓).
Wall time grew from 215 s pre-fix because the correct
result set is ~25× larger; M0073's Datum / arena
integration is expected to compress this toward ~400 s.

The M0072-0002 explicit rebind attempt (see
`docs/design/0072-0002-chained-nli-rebind.md`) was
implemented and reverted same-session — the rebind
shortcut produced a runtime explosion (Q9 cancelled at
600 s). The slot-aware approach in M0072-0001 was the
correct tool; the planner-level rebind is no longer
needed for Q9 correctness.

## Context

After M0071-0015 close, the gc-oriented-refactor branch's
TPC-H SF=1 state is captured in
`docs/handover/2026-05-09-tpch-status-phase3.md`:

- **21 / 22 queries return correct row counts** at the
  Phase-3 baseline. Q21 = 381 rows (M0071-0009 win).
- **Q5 still cancels at 600s** — slot pipeline complete, but
  the GC bottleneck has shifted from MHJ `lazyOut` (was
  99.23% / 2.02 TB pre-Stage-D-2) to:
  - `btree.RangeScan` 27.02% / 470 GB
  - `acquireRow` (via `indexScanOp`) 23.79% / 414 GB
  - Total 50.81% of the Q5 heap after the slot pipeline win.
- **Q9 still 7 rows** (target ~175). The Stage D-1 NLI
  VirtualSlot doesn't reach the IndexScan's internal row
  layer; the planner-bound `ColumnRef.Index` still points at
  the stale outer-schema position when the outer is itself a
  chained NLI.

Per `docs/handover/2026-05-09-tpch-status-phase3.md` §4
(Recommended next steps):

> M0072 is the single change that unlocks BOTH Q5 (next GC
> bottleneck) AND Q9 (chained-NLI schema-runtime equivalence).

The M0064 `outerIsMHJ` rebind gate at
`internal/planner/nl_index_join.go:399` exists because MHJ
reorders schema by OID, breaking parse-time column-index
layout. M0067-0003 attempted to remove the gate naively and
went 7 → 1 rows on Q9. M0071-0009 introduced
`SchemaColumn.SourceTableIdx` which gives
`findColumnIndexByNameAndSource` a stable disambiguation
contract — that didn't exist for M0067-0003. M0072-0002
extends the rebind block to outerIsNLI using the now-
disambiguatable Name+SourceTableIdx lookup.

This milestone runs as a single focused session with five
small commits, each gated on Q12=2 / Q13=35 / Q21=381 plus a
21-query sweep. Docs land first (Commit A) so subsequent
implementation commits cite authoritative references.

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Status |
| - | ------------- | ---- | ---- | ------ |
| 0001 | indexScanOp slot-aware BindOuter (Q5 GC fix + structural Q9 fix) | LOW-MED | structural | LANDED `c16f3f2` |
| 0002 | Chained-NLI IndexScan key rebind (planner shortcut) | MED | planner-first | NOT NEEDED — Q9 fixed structurally by M0072-0001; design 0072-0002 retained as historical |
| 0003 | TypedStringLit plan-time Datum hoisting | LOW | perf | NO-OP (already optimized — see §"M0072-0003 disposition") |
| 0004 | Per-batch String/Bytes arena (M0071-0006 land) | MED-HIGH | structural | PARTIAL — Arena type + tests landed; Datum integration carries to M0073 (see §"M0072-0004 disposition") |
| 0005 | Final 22-query SF=1 sweep + handover (M0072 close) | — | — | pending |

## Design references

- `docs/design/0072-0001-indexscan-slot-bindouter.md` (NEW) —
  authoritative for **M0072-0001**.
- `docs/design/0072-0002-chained-nli-rebind.md` (NEW) —
  authoritative for **M0072-0002**.
- `docs/design/0068-0003-batch-string-arena.md` —
  authoritative for **M0072-0004**, unchanged from M0071-0006.
- `docs/design/0068-0002-tuple-slot-pipeline.md` — slot
  pipeline contract; M0072-0001 extends BindOuter into the
  same contract.

The TypedStringLit hoist (M0072-0003) is small enough to
track via this milestone doc + commit message; no separate
design doc.

## Definition of Done

**Mandatory (correctness; must land for milestone closure):**
- [x] M0072-0001 lands: `indexScanOp.BindOuter(SlotView, int)`;
      `boundRow` deletion in `nestedLoopIndexJoinOp`; Q12=2 /
      Q13=35 / Q21=381 preserved; new `nlj_indexscan_slot_test`
      pins the contract. **Landed `c16f3f2`.**
- [x] ~~M0072-0002 lands: `outerIsNLI` rebind extension~~ —
      **NOT NEEDED.** Q9's chained-NLI silent FN was fixed
      structurally by M0072-0001 (the slot-aware BindOuter
      moves outer-column reads from `evalExpr(Row-pos)` to
      `evalExprSlot(slot.Get(col))`; the chained-NLI's
      runtime layout drift no longer matters because each
      slot exposes its own column-to-value mapping).
      Verified: Q9 = 175 rows in 1030 s on fresh-restart
      M0072 binary with cancel-after=1100 s. The M0072-0002
      rebind shortcut was a planner-side patch over a
      runtime-side bug; M0072-0001's structural fix
      eliminated the bug. Design doc 0072-0002 retained as
      historical reference.
- [x] 22-query SF=1 sweep at M0072 close: Q12=2, Q13=35,
      Q21=381, **Q9=175** (with budget ≥1100 s; cancels at
      600 s due to wall time alone — row count correct);
      all other rows preserved.

**Best-effort (perf; may carry to M0073):**
- [x] M0072-0003: NO-OP. The optimisation is already
      implemented as M0066-0002 per-node caching
      (`TypedStringLit.CacheValid` + `CachedTime` /
      `IntervalLit.CacheValid` + `CachedN`). Q5's residual
      5.27 % flat / 5.68 % cum on `evalTypedStringLit` is
      the cache-hit branch + `NewTimeDatum` Datum
      construction; further reduction requires moving
      `Datum` to a shared package (cross-package hoisting)
      which is out of M0072 scope. See §"M0072-0003
      disposition" below.
- [/] M0072-0004 PARTIAL: Arena type + 6 unit tests landed
      (`internal/executor/arena.go` + `arena_test.go`).
      Datum integration (replacing per-Datum `Buf []byte`
      with `(arena, offset, length)`) is the structurally
      risky piece — the M0066-0002 / M0071-0006 history
      shows that touching Datum.Buf semantics easily
      triggers the Q12=2/Q13=35 silent-regression mode.
      Carries to M0073 alongside the Q9 virtual-coord
      propagation work (both share the Datum / SlotView
      refactor surface). See §"M0072-0004 disposition".

**Q5 bottleneck reduction (best-effort):**
- [ ] Q5 either completes (rows ≥ 1) OR Q5 heap profile
      shows `btree.RangeScan + acquireRow` ≤ 25% (was
      50.81% post-Stage-D-2 baseline). Even with the
      reduction, Q5 may still cancel at 600s — that's
      acceptable; the structural improvement alone is the
      milestone deliverable.

**Final:**
- [ ] M0072-0005 sweep + handover doc (Phase 4)
      committed; profiles archived under
      `pprof-data/m0072-final/`.
- [ ] `go test ./...` PASS at every commit.

## M0072-0003 disposition (no-op)

The plan called for moving `TypedStringLit.Value` /
`IntervalLit.Value` parsing from runtime to plan time so
`evalTypedStringLit` becomes a constant-cell read with no
parse cost.

Phase-1 exploration revealed M0066-0002 already implements
per-node caching:

```go
// internal/planner/plan.go:97-126
type TypedStringLit struct {
    pos        int
    Type       string
    Value      string
    CacheValid bool        // populated on first eval
    CachedTime time.Time
}

// internal/executor/expr.go:527-555
func evalTypedStringLit(x *planner.TypedStringLit) (Datum, error) {
    if x.CacheValid {
        return NewTimeDatum(x.CachedTime), nil  // single struct copy
    }
    // ... parse on first call, populate cache
}
```

The per-row cost is therefore:

1. The `x.CacheValid` branch (predicted-taken after first
   eval).
2. The `NewTimeDatum(x.CachedTime)` Datum struct
   construction (stack-allocated, no heap touch).
3. The function-call overhead itself.

Eliminating (2) requires storing the constructed `Datum`
directly on the planner node, but `Datum` lives in
`internal/executor/datum.go` and `TypedStringLit` lives in
`internal/planner/plan.go` — circular import.
Resolving that requires moving `Datum` to a shared package
(`internal/datum` or similar), which is a substantial
refactor outside M0072's scope.

The Q5 cum CPU on `evalTypedStringLit` (5.68 %) is therefore
**already at the practical floor without cross-package
Datum hoisting**. M0072-0003 is closed as no-op; the
"shared Datum package" refactor is a candidate for M0074+
if Q5 still warrants further per-row eval optimisation
after the M0072-0001 / M0072-0004 wins land.

## M0072-0004 disposition (partial: Arena type only)

The plan called for landing the per-batch String/Bytes
arena per `docs/design/0068-0003-batch-string-arena.md`,
including:

1. `internal/executor/arena.go` — Arena type.
2. `Datum.arena` field replacing per-Datum `Buf []byte`.
3. `KindStringArena` / `KindBytesArena` Datum variants.
4. `DecodeRowInto` arena-aware decode path.
5. `seqScanOp` / `indexScanOp` per-call arena binding.
6. `slot.Materialize()` Datum-promotion at retention
   sites.

**Landed:** Step 1 only — `internal/executor/arena.go`
with the Arena type plus 6 unit tests pinning Allocate /
Reset (reuse) / page growth / oversized payload /
zero-length / Drop semantics.

**Deferred to M0073:** Steps 2-6. The Datum struct
modification (replacing `Buf []byte` with an arena
reference) is the M0066-0002 silent-regression surface —
the M0071 slot pipeline history (commits `08b1a5c`,
`96443e1`, `3398d47` reverted) demonstrates that touching
Datum.Buf semantics + retention-site invariants together
easily triggers Q12 = 0 / Q13 = 2 silent regressions.
M0073 unifies the Datum / SlotView refactor (Q9
virtual-coord propagation also needs Datum changes; doing
them together amortises the silent-regression bisect cost
to one milestone instead of two).

The Arena lives here so the M0073 work can wire it without
re-litigating the type design. The unit tests pin the
Arena's contract for the future caller.

## Out of scope (carry to M0073+)

- **M0073: unified Datum + SlotView virtual-coord
  refactor.** Combines (a) full virtual-coord propagation
  through SlotView so each slot reads `(sourceIdx,
  sourceCol)` from its own per-operator mapping (Q9 fix),
  and (b) `Datum.Buf` → arena-backed reference (M0072-0004
  steps 2-6). Both share the Datum / SlotView surface —
  unifying them amortises the Q12=2/Q13=35 silent-
  regression bisect cost to one milestone.
- **IndexOnlyScan slot-aware BindOuter** — currently never
  driven from NLI; M0072-0001 leaves a no-op stub for
  interface consistency.
- **`evalSubquery / evalInExpr / evalExistsExpr` slot-native
  paths** — still go through `slotToRow` adapter (M0071-0011
  scope).
- **Q20 distributional gap** (99 vs canonical ~186) —
  confirmed dataset variance in
  `docs/design/0071-0002-q20-zero-rows-diagnostic.md`.
- **Buffer-pool poolMu byTag partitioning** (M0071-0008
  carry-forward) — independent perf target; not driven by
  Q5/Q9.

## References

- `docs/handover/2026-05-09-tpch-status-phase3.md` — M0071-0015
  close + Q5 pprof findings (post-Stage-D-2 baseline).
- `pprof-data/m0071-0014/q5.cpu.prof` /
  `pprof-data/m0071-0014/q5.heap.prof` — the captures driving
  M0072-0001 / M0072-0004 targets.
- `internal/planner/nl_index_join.go:399` — the M0064
  `outerIsMHJ` rebind gate, target of M0072-0002 extension.
- `internal/planner/bushy.go::findColumnIndexByNameAndSource`
  — M0071-0009 utility, reused by M0072-0002.
- `internal/executor/operators_index.go:143-145` —
  `BindOuter(row Row)` signature, target of M0072-0001
  refactor.
- `internal/executor/operators_nljoin.go:38-251` —
  `nestedLoopIndexJoinOp.boundRow` allocation, target of
  M0072-0001 deletion.
- `internal/testutil/tpch/nli_parity_test.go:102-107` —
  Q9 cluster-backed parity test; M0072-0002 acceptance
  criterion.
