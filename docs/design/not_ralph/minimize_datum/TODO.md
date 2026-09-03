# TODO — minimize_datum execution checklist

One checkbox ≈ one commit. Each item names its design section and its gate.
Nothing here has started.

**Legend:** `[ ]` open · `[~]` partial (with a close-line saying what remains)
· `[x]` closed · `[-]` dropped (with a reason, in the Dropped table)

---

## Status: BLOCKED. Not approved to start.

Three blockers from the 2026-09-03 review (`REVIEW.md`, findings B1-B7):

1. **No licence from take3.** README §Status and 04 §0.2 — the re-proposal clause
   in take3 13 §10 covers `Datum` re-layout and JIT, not "a new row
   representation". This bundle argues on merits and needs acceptance from
   whoever owns take3 13.
2. **MD-04 onward is blocked by take3 13 §8.2** (EX1 before the batching
   geometry) chained with §8.6 (P4-01 before EX1). Required order:
   **P4-01 → EX1 → geometry → MD-04**. See 04 §10.
3. **The gates depend on instruments that do not exist** — take3 EX0-02
   (protocol), EX0-03 (worker hash counters), EX0-04 (per-operator harness),
   EX0-05 (batch counters). Three of MD-04's four headline numbers are currently
   unmeasurable (05 §6).

MD-01, MD-02, MD-03, MD-03.5 and MD-1x carry no geometry and are unblocked by
(2), but still gated on (1).

---

## Ground rules (do not start an item without these)

1. **Values are the gate, not row counts.** 06 §1: three of the bugs this design
   must not repeat produced correct counts and wrong values. Every commit runs
   the 06 §2 floor including both suites' value digests.
2. **One variable per commit.** In particular: never the retention format and the
   byte layout in one commit (03 TD-4); never the storage without its
   `hashsize` model (04 D-3); never the serial hash build without its parallel
   sibling (04 §4.1).
3. **Plan-shape pin on every commit.** This is executor work that feeds the
   planner through `hashsize` (04 §5, R-6). A moved plan is reported, not fixed
   executor-side by preference.
4. **`Datum` stays 48 bytes.** `const _ uintptr = 48 - unsafe.Sizeof(Datum{})`
   (`datum.go:187`) is untouched by every item below. If an item needs to change
   it, the item is wrong — see 04 §0.1.
5. **Never `-count=1`** in a gate run. Never `--no-verify` for a code commit.
6. **Sequencing with take3's executor bundle.** `TODO_EXECUTOR.md` EX2-01 is a
   retention-boundary audit of the same seam MD-04 onward converts. Either
   EX2-01 lands first and MD-04 consumes it, or MD-04 produces the audit for the
   sites it touches — **but not both in flight independently** (04 §10).

---

## Phase 0 — Foundations (no behaviour change)

- [ ] **MD-01** `TupleDesc` + `attlen`/`attbyval`/`attstorage` on `colTypeInfo`,
      sharing **one** `pg_type.dat` transcription with `userTypeAttrsForOID`.
      Honour `coltypeinfo.go:12-25`'s DDL-staleness contract.
      *design: 04 §3 (D-1), 03 §5 (TD-1) · gate: 06 §3 MD-01 (agreement test +
      oracle spot-check) · files: `coltypeinfo.go`, `pg18_user_catalog_rows.go`,
      new*
      **On landing: re-derive the site count from the compiler and record the
      delta against 05 §2 in the progress log (05 §5).**

- [ ] **MD-02** R-1 audit — derived-column type fidelity. Count the plan nodes
      whose output schema contains a column `NewTupleDesc` declines, by reason,
      raw and weighted by estimated retained rows, over both suites. **Verdict
      in the words proceed / re-scope / stop.**
      *design: 04 §3 (D-2), §9.2 · gate: 06 §3 MD-02 · document only*
      **This item can stop the bundle. It is not a formality.**

---

## Phase 1 — The type, unreachable

- [ ] **MD-03** `PackedTuple` (MinimalTuple layout, 15-byte header, 6 pad bytes
      kept, `hoff`-relative accessors, 4-byte hash prefix) + `PackedSlot`
      (`TupleSlot` impl, `(nvalid, off)` watermark over
      `DecodeRowRangeIntoMctxPGTupleStyled`) + **all six type-switch arms** +
      `attcacheoff` fast path + the exhaustiveness tests moved from `spill.go`.
      **No producer — the type is complete and unreachable.**
      *design: 04 §2, §6 (D-4), §9.1 (R-0), 03 §7.1 (TD-3), TD-2 · gate: 06 §3
      MD-03 (a test per switch, watermark property test, escape check) · files:
      `slot.go`, `opnode.go`, `expr.go`, `exprnode.go`, `operators.go`,
      `codec.go`, new*

---

## Phase 2 — Tier A: `work_mem`-bounded, planner-modelled

- [ ] **MD-03.5** *Throwaway prototype.* Pack and unpack the Q9 build side
      behind a flag with a hardcoded descriptor, measure 05 §6's four numbers,
      **delete the code**. Tests MD-04's hypothesis before ~900 LOC is sunk.
      *design: 05 §6 · gate: values-diff only (the code does not land) · not a
      commit to `master`*

- [ ] **MD-04** Hash join — **serial and parallel in one commit** — with the
      `hashsize` model re-derived in the same commit.
      **BLOCKED on P4-01 → EX1 (take3 13 §8.2/§8.6) and on take3 EX0-03/04/05.**
      Its gate currently names a single query (Q9), which take3 EX-P2 forbids;
      restate over the hash-join operator and a named class of shapes, or split
      the item (06 §3).
      **A fifth retention lane is missing from 04 §4.1's sibling set** — the
      composite multi-key lane (ledger M0127) does not batch, is **unbounded**,
      and already stores packed bytes. Add it to Tier A before starting.
      **This is a measurement slice** (04 §10, 05 §6). It reports batch count at
      PostgreSQL's `work_mem`, retained bytes, wall time and allocation count,
      and it may return a negative result.
      *design: 04 §4.1, §5 (D-3), §9.4 (R-3) · gate: 06 §3 MD-04 (model-vs-
      reality test; batch-count witness against the recorded 128 full / 64
      narrowed) · files: `operators_join_agg.go`, `parallel_hash_build.go`,
      `hashsize/hashsize.go`*
      **Stopping rule: 05 §6. If the measurement says stop, revert MD-04 —
      do not keep it and stop (R-4).**

- [ ] **MD-05** Sort (`operators.go:769`). Gate checks **ordering explicitly**,
      not membership: `operators.go:898-900` records that a chunk sorted by one
      comparator and merged by another emits out-of-order rows with no error.
      *design: 04 §4.1 · gate: 06 §3 MD-05*

- [ ] **MD-06** Materialize (`operators_material.go:68`).
      *design: 04 §4.1 · gate: 06 §2 floor + alloc arm*

---

## Phase 3 — Tier B: unbounded buffers

The win here is reduced OOM exposure, not a batch count — no `work_mem` model
covers these, and goopg has **no tuplestore at all** (04 §4.2). Price and report
them accordingly.

- [ ] **MD-07** Window (`operators_window.go:22`) — buffers an entire partition
      set unconditionally, with no spill path in the operator.
      *design: 04 §4.1 · gate: 06 §2 floor + alloc arm*

- [ ] **MD-08** Memoize (`operators_memoize.go:82,85`), CTE cache
      (`context.go:623`), recursive worktable (`context.go:364`,
      `operators_recursive_cte.go:63-64`), lateral CTE
      (`join_lateral_stream.go:85-86`).
      *design: 04 §4.1 · gate: 06 §2 floor; recursive-CTE lifetimes are the risk*

- [ ] **MD-09** Gather + Gather Merge (`operators_gather.go:44,64`,
      `operators_gather_merge.go:40`). Serial control arm plus worker arms; a
      Datum-safety bug here is a wrong answer, not a crash.
      *design: 04 §8 · gate: 06 §3 (parallel arm,
      `parallel_substrate_test.go:26-80` pattern)*

- [ ] **MD-10** RETURNING buffers (`operators_storage.go:2355,4386,6219`,
      `operators_upsert.go:71`) and `conn_tx.Rows`
      (`internal/postmaster/conn_tx.go:46` — a whole result set per connection).
      *design: 04 §4.1 · gate: 06 §2 floor*

- [ ] **MD-11** Outer-join sweep (`operators_join_agg.go:261,268`) and the
      aggregate group representative. The four `[][]Datum` agg accumulators
      **stay Datums** — Tier C boundary.
      *design: 04 §4.1 · gate: 06 §2 floor*

- [ ] **MD-12** The ~13 small `rows []Row` sites (distinct, setop, SRF,
      catalog views). Mechanical, low value; may be one commit or dropped.
      *design: 04 §4.1 · gate: 06 §2 floor*

---

## Phase 4 — Byte-format fidelity

Independent of Phases 1-3 (03 §7.2). May land before MD-03 or after MD-12,
**never inside another item**.

- [ ] **MD-1x** Conditional alignment, both directions — `att_align_datum` on
      encode, `att_align_pointer` on decode, generalising the one existing
      implementation at `internal/catalog/codec.go:1693-1695`.
      **This changes the on-disk format.**
      *design: 03 §3 (D1), §3.3, TD-4 · gate: 06 §3 MD-1x — byte goldens vs live
      PG 18.3 (TOAST columns excluded, with the reason stated), backward read of
      old nominal-aligned tuples, forward read of a PG-authored unaligned short
      varlena · blocked on: MD-01*

---

## Phase 5 — Convergence

- [ ] **MD-last** Convert `spill.go`'s payload to the PG format. Framing
      (`WriteRowHashed`'s hash-then-tuple) is unchanged. The nine existing test
      functions are **re-pointed at the new codec, not deleted**.
      *design: 03 TD-5, 04 §4.1 Tier D · gate: 06 §3 MD-last · blocked on: every
      in-memory retention site*

---

## Acceptance

06 §5. Six conditions, of which the one this bundle is most likely to fail
quietly is #5: **`Datum` is still 48 bytes.** That is the bundle's own
falsifiable claim — that it did not become the change it declined.

Explicitly **not** in acceptance: a wall-time target (06 §5).

---

## Open gaps recorded, not closed

| gap | where | disposition |
|---|---|---|
| take3 licence for "a new row representation" | README §Status, 04 §0.2 | **must be obtained**; not claimed |
| Composite multi-key hash lane (unbounded, packed-byte keys) | 04 §11.3, ledger M0127 | add to Tier A |
| Cross-statement descriptor ownership (cursors, `CTERowCache`) | 04 §9.8 (R-7) | resolve before MD-08/MD-10 |
| Scratch-arena reset point | 04 §9.9 (R-8) | resolve before MD-03 |
| `Release()` / row-pool story for variable-length buffers | 04 §9.11 (R-10) | undesigned; invalidates take3 EX2-03 |
| PG-format TOAST pointers (`varatt_external`) | 03 §4 (D2) | out of scope; needs a ledger row with a resume point |
| No `tuplestore` — Tier B buffers have no budget | 04 §4.2 | out of scope; a separate design |
| `Datum` re-layout below 48 B | 04 §0.1, 05 §1 | declined; re-proposing needs new measurement, per take3 13 §10 |

---

## Progress log

One row per closed item. Numbers come from the 06 §4 protocol; every timing
carries its `work_mem` and its arm.

| item | closed | commit | batches @ PG work_mem | retained bytes | wall | allocs | notes |
|---|---|---|---|---|---|---|---|

Pre-state for the batch-count column, from
`impl/FINDING-p401-alone-is-not-enough.md`: Q9's `orders` build side at
`work_mem` 4 MB × `hash_mem_multiplier` 2 — **128 batches at full width, 64
narrowed**.

---

## Dropped

Items removed from the plan, with the reason. Keep the original wording —
negative results are only legible if they survive.

| item | date | reason | ledger row |
|---|---|---|---|

(End of file)
