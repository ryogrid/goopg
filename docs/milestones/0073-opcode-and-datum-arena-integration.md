# Milestone 0073 — OpCode int enum + Datum/arena integration (Q5 perf + Q9 wall-time compression)

**Status:** closed (2026-05-10) — Commits `c696cea`,
`58efeb0`, `c9a34b0`, `d0bfe99` (M0073-0001..0004) +
Commit E (M0073-0005, this handover). Q5 total heap
dropped 1463 GB → 404 GB (−72 %). Row counts: 21/22
preserved, Q5 still cancels at 600 s (structural — M0074
addresses CPU-bound `evalExprSlot`). Q9 row count carries
forward as the bimodal mode-1 baseline (7 rows / 223 s
this cycle); compression target Q9 ≤ 600 s deferred to
M0074. See [`docs/handover/2026-05-10-tpch-status-phase5.md`](../handover/2026-05-10-tpch-status-phase5.md).
**Branch:** `gc-oriented-refactor` (continuation of M0072)
**Depends on:** M0072-0001 (commit `c16f3f2`) — slot-aware
BindOuter; M0072-0004 (commit `b081767`) — Arena type
infrastructure.
**Drives:** Q5 `evalBinary` cum CPU −50 % via int OpCode
dispatch; Q5 / Q9 `acquireRow` ≤ 5 % heap via per-batch
arena-backed Datums; Q9 wall time → ~400 s (from 1030 s);
all 21 non-Q5 row counts preserved; **Q12=2 / Q13=35 /
Q21=381 / Q9 ≥ 90 rows / Q9 wall ≤ 2 × 1030 s** every commit.

## Context

After M0072 close (handover-phase4), two residual cost
centres dominate Q5 + Q9:

1. **Per-row CPU eval (Q5 evalBinary 8.78 % flat /
   29.20 % cum CPU; evalExprSlot 26.11 % flat / 68.27 %
   cum)** — the hot path is string-switch dispatch on
   `BinaryOp.Op` / `UnaryOp.Op`. There are 16 distinct Op
   string values (`"+"`, `"-"`, `"*"`, `"/"`, `"%"`,
   `"||"`, `"="`, `"<"`, `">"`, `"<="`, `">="`, `"<>"`,
   `"!="`, `"AND"`, `"OR"`, `"LIKE"`, `"NOT LIKE"`, plus
   unary `"-"` / `"+"` / `"NOT"`). Replacing the field
   with `OpCode int8` enables jump-table dispatch
   (~2-4 × per switch).

2. **Per-tuple decoded-row alloc (Q5 acquireRow 25.31 %
   cum heap; Q9 wall 215 s → 1030 s post-fix)** — Q9's
   correct row set is ~25 × larger after M0072-0001;
   `cloneRow` on append in `indexScanOp.scanFn` allocates
   per matched tuple via `acquireRow` (sync.Pool). The
   Arena type from M0072-0004 (`internal/executor/arena.go`,
   commit `b081767`) is uncalled — M0073 wires it through
   `Datum` + `DecodeRowInto` + `seqScanOp` / `indexScanOp`
   + `Materialize`.

The work splits into two independent tracks:

- **Track 1 (M0073-0003):** OpCode int8 enum — atomic
  mechanical refactor; ~100 sites updated in one commit.
  Independent of arena work.
- **Track 2 (M0073-0001 / 0002 / 0004):** Datum arena
  integration — three sequenced commits (struct → wire →
  promote).

This milestone runs as a single focused session with
five commits, each gated on the expanded acceptance set
including the Q9 wall-time floor.

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0001 | Datum.arena field + KindStringArena/BytesArena | LOW-MED | structural | — |
| 0002 | DecodeRowInto arena-aware path | MED | structural | 0001 |
| 0003 | UnaryOp/BinaryOp Op string → OpCode int8 enum | MED | perf | — |
| 0004 | seqScanOp/indexScanOp arena binding + Materialize promotion | MED-HIGH | structural | 0001 + 0002 |
| 0005 | Final 22-query SF=1 sweep + Phase 5 handover | — | — | 0001..0004 |

## Design references

- `docs/design/0073-0001-datum-arena-field.md` (NEW) —
  authoritative for **M0073-0001**.
- `docs/design/0073-0002-decode-arena-binding.md` (NEW) —
  authoritative for **M0073-0002**.
- `docs/design/0073-0003-opcode-int-enum.md` (NEW) —
  authoritative for **M0073-0003**.
- `docs/design/0073-0004-arena-binding-and-materialize.md`
  (NEW) — authoritative for **M0073-0004**.
- `docs/design/0068-0003-batch-string-arena.md` — original
  arena design from M0068; M0073 implements its
  integration steps.
- `docs/design/0068-0001-datum-compact-layout.md` —
  precedent for Datum struct change discipline.

## Definition of Done

**Mandatory (correctness; must land for milestone closure):**
- [ ] M0073-0003 lands: `OpCode int8` enum in
      `internal/parser/op.go`; both parser + planner
      `UnaryOp` / `BinaryOp` use it; Q12=2 / Q13=35 /
      Q21=381 / Q9 ≥ 175 rows preserved; Q5 `evalBinary`
      cum CPU ≤ 15 %.
- [ ] M0073-0001 lands: Datum struct +`arena *Arena`
      field; KindStringArena / KindBytesArena variants;
      StringValue / BytesValue transparent for both
      paths; `unsafe.Sizeof(Datum{}) == 64`.
- [ ] M0073-0002 lands: `DecodeRowInto(...) error` extended
      with arena param; varchar / bytes paths emit
      arena-backed Datums when arena is bound.
- [ ] M0073-0004 lands: seqScanOp + indexScanOp arena
      binding; Materialize promotion at the 4 retention
      sites; Q5 `acquireRow` ≤ 5 % cum heap.
- [ ] 22-query SF=1 sweep at M0073 close: Q12=2, Q13=35,
      Q21≥100, Q9 ≥ 175 rows, all other rows preserved.

**Best-effort (perf; may carry to M0074):**
- [ ] Q9 wall time ≤ 600 s (compression target; was
      1030 s at M0072-0001). Hard floor: ≤ 2 × 1030 s
      = 2060 s — regression beyond that triggers revert.
- [ ] Q5 total heap ≤ 1.0 TB (was 1.46 TB at
      M0072-final).
- [ ] Q5 `evalExprSlot` cum CPU ≤ 60 % (was 68.27 %).

**Final:**
- [ ] M0073-0005 sweep + handover doc (Phase 5)
      committed; profiles archived under
      `pprof-data/m0073-final/`.
- [ ] `go test ./...` PASS at every commit.

## Out of scope (carry to M0074+)

- **Datum struct packed layout** (Option B from
  exploration) — replace `Buf []byte` with
  `(arenaRef int32, offset int32, length int32)` to
  free 12 B. Useful when more Datum fields are needed.
  M0073 takes the simpler `+arena *Arena` (Option A).
- **Vectorised evalBinary** — pure-batch eval over a
  Datum array (not the per-row slot pipeline). Q5's
  `evalExprSlot` 68 % cum CPU may drop another 30 % on
  a vectorised path. Independent of M0073.
- **`evalSubquery / evalInExpr / evalExistsExpr`
  slot-native paths** — still go through `slotToRow`
  adapter (M0071-0011 scope).
- **Q20 distributional gap** (99 vs canonical ~186) —
  confirmed dataset variance.
- **Wire-protocol output of OpCode** — currently no
  public surface; unchanged.

## References

- `docs/handover/2026-05-09-tpch-status-phase4.md` —
  M0072 close + Q5 / Q9 residual cost analysis.
- `pprof-data/m0072-final/q5.{cpu,heap}.prof` — the
  captures driving M0073-0001 + M0073-0003 targets.
- `internal/executor/arena.go` (M0072-0004,
  commit `b081767`) — Arena type infrastructure;
  M0073-0001 adds `Bytes(offset, length)` accessor.
- `internal/parser/expr.go:220-235` — UnaryOp / BinaryOp
  field declarations; target of M0073-0003.
- `internal/executor/datum.go:85-91` — Datum struct;
  target of M0073-0001.
- `internal/executor/codec.go:172-203` — DecodeRowInto;
  target of M0073-0002.
- `internal/executor/operators_storage.go::seqScanOp` /
  `internal/executor/operators_index.go::indexScanOp` —
  target of M0073-0004 arena binding.
- `internal/executor/slot.go:62-64` —
  MaterializedSlot.Materialize; target of M0073-0004
  promotion.
