# Milestone 0074 — CPU optimisation + Q9 structural fix + Datum packed layout

**Status:** planned
**Branch:** `gc-oriented-refactor` (continuation of M0073)
**Depends on:** M0073-0005 (commit `1e33801`) — M0073
close handover; M0073-0001 (commit `c9a34b0`) — Datum.arena
field; M0073-0002+0004 (commit `d0bfe99`) — arena wiring;
M0071-0009 — `SchemaColumn.SourceTableIdx` (used by
M0074-0002).
**Drives:** Q5 `evalBinary` cum CPU ≥ 50 % drop; Q5
`evalExprSlot` cum CPU ≤ 50 % via ColumnRef inlining +
vectorised batch eval; Q9 **deterministic 175 rows** via
virtual-coord propagation through SlotView; Datum struct
packed layout (52 B exact, frees 12 B headroom);
`numericMant` flat ≤ 1 % via int64 fast-path; **Q12=2 /
Q13=35 / Q21=381 / Q9=175 (post-0002)** at every commit.

## Context

After M0073 close (Phase-5 handover, commit `1e33801`)
the structural Q5 heap problem is fixed (1463 GB →
404 GB, −72 %). Q5 now cancels at 600 s due to **CPU-
bound** evaluation, not heap. Q5 CPU pprof shows:

| Function | flat % | cum % | Comment |
|---|---:|---:|---|
| `evalExprSlot` | 25.83 | 68.68 | dominant; ColumnRef hot path goes through 13-arm type switch |
| `evalBinary` | 10.39 | 33.72 | arithmetic + comparison + AND/OR; switch on OpCode |
| `compareDatum` | 5.86 | 12.17 | numericCmp allocates fresh big.Int per call via numericMant |

Five concurrent residual issues motivate M0074:

1. **CPU dispatch overhead in `evalExprSlot`** — ColumnRef
   is the dominant case in Q5 but reached only after 13
   type-switch arms. Reordering / inlining shaves
   dispatch.

2. **No batch eval entry point** — `evalBinary` is per-
   row. Vectorising arithmetic / comparison / AND-OR
   over a Datum array unlocks ≥ 30 % CPU reduction on
   Q5's per-page batches.

3. **Q9 chained-NLI virtual-coord mismatch** — when an
   NLI's outer is itself an NLI, the outer's
   `*VirtualSlot` carries `(sourceIdx, sourceCol)` per
   output column, but the inner IndexScan's planner-
   bound `ColumnRef.Index` is a flat schema position.
   Q9 stays bimodal: mode-1 (7 rows / 220 s, silent FN)
   vs mode-2 (175 rows / 1030 s). The cleaner fix is
   **executor-side virtual-coord resolution**.

4. **Datum struct headroom exhausted** — `Datum` is
   64 B exact; M0073-0001's `arena *Arena` consumed all
   8 B padding. Replace `Buf []byte` (24 B) with
   `(arenaRef int32, offset int32, length int32)` (12 B)
   to free 12 B. Blocker: literal Datums + detoasted
   Datums have no arena context. Solution: per-process
   permanent arena.

5. **`numericCmp` / `numericMant` allocates `big.Int`
   even on int64 fast-path operands** — 5.86 % flat CPU
   on Q5. TPC-H NUMERIC mantissas all fit in int64; an
   int64 fast-path eliminates the allocation.

The work splits into four independent tracks:

- **Track 1 (M0074-0006):** Numeric int64 fast-path.
  Self-contained in `numeric.go`; quickest win.
- **Track 2 (M0074-0004):** DecodeRowProjectionIntoArena.
  Small isolated extension to codec.
- **Track 3 (M0074-0002):** Chained-NLI virtual-coord
  propagation. Q9 row-count fix.
- **Track 4 (M0074-0001):** Vectorised evalBinary +
  ColumnRef inline. Q5 CPU drop.
- **Track 5 (M0074-0003):** Datum struct packed layout.
  Highest-risk last.

Land order (cheapest+independent → riskiest):
B (0006) → C (0004) → D (0002) → E (0001) → F (0003).

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0006 | Numeric int64 fast-path in compareDatum / arithmetic | LOW | perf | — |
| 0004 | `DecodeRowProjectionIntoArena` variant | LOW | perf | — |
| 0002 | Chained-NLI virtual-coord propagation through SlotView | MED-HIGH | structural | M0071-0009 |
| 0001 | Vectorised evalBinary + ColumnRef inline | MED | perf | 0006 (reuses int64 fast-path) |
| 0003 | Datum struct packed layout (52 B exact) | HIGH | structural | 0001..0006 (lands last) |
| 0005 | Final 22-query SF=1 sweep + Phase 6 handover | — | — | 0001..0006 |

## Design references

- `docs/design/0074-0001-vectorised-binary-and-columnref-inline.md`
  (NEW) — authoritative for **M0074-0001**.
- `docs/design/0074-0002-chained-nli-virtual-coord.md` (NEW)
  — authoritative for **M0074-0002**.
- `docs/design/0074-0003-datum-packed-layout.md` (NEW) —
  authoritative for **M0074-0003**.
- `docs/design/0074-0004-decode-row-projection-arena.md`
  (NEW) — authoritative for **M0074-0004**.
- `docs/design/0074-0006-numeric-int64-fast-path.md` (NEW)
  — authoritative for **M0074-0006**.
- `docs/design/0072-0002-chained-nli-rebind.md` —
  reverted approach; carrying lessons forward.
- `docs/design/0068-0001-datum-compact-layout.md` —
  Datum struct change discipline (precedent for 0074-0003).

## Definition of Done

**Mandatory (correctness; must land for milestone closure):**
- [ ] M0074-0006 lands: int64 fast-path in `numericCmp`,
      `numericAdd/Sub/Mul/Div`; full SF=1 sweep row-count
      preserved exactly (numerics must round-trip
      identically); `numericMant` flat ≤ 1 %.
- [ ] M0074-0004 lands: `DecodeRowProjectionIntoArena`
      variant; index-build call sites wired; SF=1 sweep
      unchanged.
- [ ] M0074-0002 lands: VirtualSlot-aware ColumnRef
      resolution at evalExprSlot boundary; Q9 = 175 rows
      DETERMINISTICALLY (no longer bimodal); Q21 still
      = 381 rows.
- [ ] M0074-0001 lands: ColumnRef hoisted to first
      switch arm in evalExprSlot; `evalBinaryBatch`
      entry for vectorisable arms; seqScanOp predicate
      batch path; Q5 `evalBinary` cum CPU ≤ 15 %;
      `evalExprSlot` cum ≤ 50 %.
- [ ] M0074-0003 lands: `Buf []byte` → `(ArenaRef,
      Offset, Length)` int32 triplet; permArena +
      arenaRegistry infrastructure; struct = 52 B exact;
      all 38 NewStringDatum/NewBytesDatum sites + detoast
      + spill + COPY paths migrated; full SF=1 sweep
      preserved.
- [ ] 22-query SF=1 sweep at M0074 close: Q12=2, Q13=35,
      Q21≥100, Q9 = 175 rows DETERMINISTICALLY, Q22=7,
      all other rows preserved.

**Best-effort (perf; may carry to M0075):**
- [ ] Q5 wall time ≤ 110 % of M0073-final (no regression
      from M0074-0003 layout flip).
- [ ] Q5 heap ≤ 500 GB total (was 404 GB at M0073-final;
      permArena permanent overhead bounded).
- [ ] Q9 wall time ≤ 1100 s (no compression target,
      just within current budget).

**Final:**
- [ ] M0074-0005 sweep + handover doc (Phase 6) committed;
      profiles archived under `pprof-data/m0074-final/`.
- [ ] `go test ./...` PASS at every commit.

## Out of scope (carry to M0075+)

- **Full columnar batching of all operators**
  (filterOp, projectOp, hashAgg, sortOp). M0074-0001
  lands the vectorisation entry point + seqScanOp
  predicate; the rest of the pipeline still runs row-
  at-a-time.
- **Per-connection permArena scoping**. M0074-0003 lands
  process-global permArena. For multi-tenant production,
  per-connection scoping with bounded LRU eviction is
  M0075 candidate.
- **Q5 wall-time compression**. M0074 targets cum CPU
  reduction; Q5 may still cancel at 600 s.
- **Q9 wall-time compression**. M0074-0002 targets
  determinism (175 rows); wall time may stay > 1100 s.
- **SIMD intrinsics for evalBinaryBatch**. Plain Go
  batch loops; hand-tuned SIMD is M0075+.
- **Q20 distributional gap** (99 vs canonical ~186) —
  confirmed dataset variance.

## References

- `docs/handover/2026-05-10-tpch-status-phase5.md` —
  M0073 close + M0074 candidate enumeration.
- `pprof-data/m0073-final/q5.{cpu,heap}.prof` — captures
  driving M0074-0001 + M0074-0006 targets.
- `internal/executor/expr.go:52-265` — evalExprSlot +
  evalBinary; target of M0074-0001 + M0074-0002.
- `internal/executor/numeric.go:34-39, 416-425` —
  numericMant + numericCmp; target of M0074-0006.
- `internal/executor/slot.go:106-150` — VirtualSlot +
  virtualCol; target of M0074-0002.
- `internal/executor/datum.go:101-122` — Datum struct;
  target of M0074-0003.
- `internal/executor/codec.go:65-127` — DecodeRowProjection;
  target of M0074-0004.
- `internal/executor/operators_index.go:166, 379` —
  indexScanOp BindOuter + lookupKey; target of
  M0074-0002.
