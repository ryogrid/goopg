# M0122-0012: vector filter pipeline scope reconciliation

status: accepted (corrected 2026-09-23 — read-ahead disabled under currentTID consumers, `ffe1d020f`)
date: 2026-09-22
supersedes: none

## Decision

The historical filter-batch proposal is not ready to implement as written.
This reconciliation closes the missing scope boundary in M0122-0012 without
claiming that a dormant helper is a vectorized execution path.

## Findings

1. `evalBinaryBatch` has a narrow production call site in `filterOp`. It still
   loops over `evalBinary`; the initial scope proves an ownership-safe caller,
   not a broad scan fast path.
2. The old proposal targeted `filterOp`. The default execution path now
   absorbs a `Filter` directly above a `SeqScan` into `seqScanOp`, then
   evaluates the qualifier exactly once through `evalQual`. Consequently,
   adding a batch path only to `filterOp` would not cover the principal scan
   workload the proposal used to motivate itself.
3. Buffering child slots changes the consumer lifetime contract. A normal
   slot is valid only until the child's next `Next`; a surviving batch must
   retain an independently materialized row. `MaterializedSlot.Materialize`
   and `cloneRowOwned` provide the required copy boundary, including
   arena-backed string, byte, and large numeric datums, but a new batch path
   must prove that it applies that boundary before every later child advance.
4. The current scan-resident qualifier has deliberate early-versus-late error
   ordering and partial-deform rules. A generic batch evaluator cannot replace
   it until it preserves those rules, cancellation checks, NULL three-valued
   filtering, and the filter-removal instrumentation contract.

## Consequence

The next implementation slice is a scan-resident, ownership-safe batch
prototype rather than the old `filterOp` sketch. It must first add a focused
equivalence test that exercises a child reusing its output buffer, then limit
the fast path to a predicate shape whose result is boolean and whose column
references fit the scan's prefilter bound. The fallback remains `evalQual`.
Only after that proof and a Q12/Q13 benchmark can the path be expanded.

This is performance infrastructure, not a PostgreSQL semantic omission, so
it creates no deferral-ledger row.

## First ownership guard

`TestM0122VectorBatchRetainsReusedSlotBeforeNext` is the first executable
guard for the successor. It reuses one concrete `Slot` exactly as an op-node
producer does, snapshots each row with `Materialize`, and evaluates the
snapshots through `evalBinaryBatch`. It proves the batch boundary preserves
each original operand rather than silently retaining the producer's final
overwrite. The future scan-resident path must keep this boundary before any
child advance.

## Narrow production caller

The first production caller is `filterOp`, limited to one row-dependent
comparison with column-or-constant operands. It snapshots up to 64 child slots,
evaluates the operand arrays through `evalBinaryBatch`, and emits survivors in
input order. A comparison of constants stays on the per-row path: it has no
row-dependent work to amortize, so entering the vector path would only add slot
snapshot ownership work. `TestM0122BatchFilterRequiresRowOperand` pins that
admission boundary.
This does not replace the scan-resident qualifier: a Filter directly above a
SeqScan remains absorbed by `seqScanOp`. Compound AND/OR expressions retain
the per-row evaluator until their short-circuit and error ordering are proven.

## Reusable datum buffers

The production caller owns left, right, and result datum vectors for the
lifetime of `filterOp`. `reserveBatchDatums` grows them only when a refill
exceeds prior capacity, and `evalFilterBatch` receives those buffers rather
than allocating operands per refill. This deliberately does not apply to
`batchRows`: each row is a snapshot of a child-owned slot and must retain its
separate ownership boundary. `TestM0122FilterBatchReusesDatumBuffers` crosses
the 64-row refill boundary and pins the three vector identities.

## Scan-prefix admission guard

A simple comparison being eligible for `evalBinaryBatch` does not make it
safe to evaluate before a scan has fully deformed its row. `PlanScanQual`
remains the sole authority: it admits a comparison only when every referenced
column lies in a strict prefix of the row. The guard covers both early prefix
comparisons and a full-row comparison that must remain late, so a later scan
batch caller cannot confuse evaluator eligibility with partial-deform safety.

## Scan-resident batch boundary

The current `seqScanOp.Next` loop is not a place to collect already-prefiltered
rows. Its per-tuple sequence is load-bearing: heap visibility and SSI handling
run under the page lock; early evaluation may be retried late after tail deform
and detoast; and rejected rows release the page lock before continuing. A local
loop around `evalPrefilter` would either retain a borrowed tuple too long or
change error and lock ordering. The next implementation therefore needs an
explicit scan batch interface that owns snapshots and terminal errors across
the full tuple-loop sequence; it must not retrofit `evalBinaryBatch` into the
existing `Next` body.

## References

- `internal/executor/expr_batch.go`
- `internal/executor/operators.go` (`filterOp.Next`)
- `internal/executor/scan_prefilter.go` (`scanPrefilter`, `seqScanOp.evalQual`)
- `internal/executor/slot.go` and `internal/executor/datum.go`
- `docs/design/0050-0099/0075-0004-filter-batch-wiring.md`

## Measured before landing (2026-09-22)

The recon above argued from structure that this first caller would not cover
the principal workload. Three measurements now say how far that goes, and they
are the reason this slice is landed as **infrastructure with no performance
claim**.

**Reachability — it fires, but rarely.** A temporary `FILTEROPCENSUS` counter in
`filterOp.Open`, run over the whole TPC-DS SF0.25 sweep (99 queries), recorded
**363 filter opens**: `batch=false` 292 \(`BinaryOp` shapes the admission rule
declines\), `batch=false` 66 \(`BooleanConst`\), and **`batch=true` 5**. So the
batch path engages on **1.4%** of the corpus's filter opens. Neither corpus's
plan capture contains a standalone `Filter` operator node at all — a `Filter`
above a scan is absorbed into `seqScanOp` and rides as the scan's `Filter:`
property — so every one of those 363 is a filter above a join or another
non\-scan child.

**Speed — no measurable gain.** A/B on the shape the path serves \(400,000\-row
join, filter above it, same binary, batch path toggled by a temporary env
switch\), three runs each after a warm\-up: batch ON 226 / 210 / 219 ms,
batch OFF 265 / 217 / 210 ms. Medians 219 vs 217 ms — inside the noise. **Do
not cite this slice as a speedup.** Rule \#4 exists for exactly this: an
allocation\-shaped win does not imply a wall\-clock win, and here it did not
produce one.

**Semantics — three\-way agreement on the admitted shape.** NULL three\-valued
filtering was compared across batch ON, batch OFF and PostgreSQL 18.3 on the
same data \(`j > 20` over rows containing NULLs, `j <> 10`, and `j = NULL`\):
all three return `1 / 30`, `1`, `0`. This is the sibling\-path check the
practice card requires — a second evaluator is only safe while it agrees with
`evalQual`, and agreement was measured rather than assumed.

**What this slice is worth, stated plainly.** It is the ownership boundary the
scan\-resident successor needs, proven by
`TestM0122VectorBatchRetainsReusedSlotBeforeNext` against a child that reuses
its output buffer, plus an admission boundary pinned by
`TestM0122BatchFilterRequiresRowOperand`. The performance case has to be made
by the scan\-resident slice, where the 292 declined opens and the absorbed scan
quals actually live.

## Correction 2026-09-23 — read-ahead breaks the currentTID side channel

The batch path was admitted on operand shape alone, but it has a second
precondition the admission rule did not state: **nothing above it may read the
scan leaf's current position**. It pulls up to `filterBatchSize` (64) child rows
before emitting the first, while four consumers read
`currentTIDProvider.currentTID()` immediately after pulling each row and assume
nothing was pulled since — `lockRowsOp` (row locking), the streaming NL join's
ctid capture, the hash join's preserved build-side ctids, and `setOp`'s
delegation to its active branch.

Witness (bisected to `d6e42a7f7`): isolation spec
`insert-conflict-do-update-4`, `SELECT * FROM upsert WHERE i = 1 FOR UPDATE`
over a two-partition table — LockRows read the position of a LATER row in the
other partition and failed with `short read at block`. The pass-required spec
had passed every nightly before this slice landed.

Fix (`ffe1d020f`): `disableFilterReadAhead` (operators_lockrows.go) mirrors
`findScanLeaf`'s descent — single-child spine, both `setOp` branches, both join
sides, the NLI outer — and pins every `filterOp` it reaches to the per-row path
(`noReadAhead`, sticky across re-Open; `batchEnabled` cleared directly because
consumers call it after the child's Open). Each consumer calls it before its
first `Next`. The batch path stays available everywhere else, so this slice's
measurements (reachability 1.4 %, timing inside noise) are unchanged in kind.
Pinned by `TestForUpdateOnPartitionedTableThroughBatchFilter` (fails with the
exact error without the fix) and `TestDisableFilterReadAheadCoversEveryWalkerBranch`.

**Rule for the scan-resident successor:** any operator that buffers ahead of
its consumer must either carry row identity in the slot all the way to the
consumer (the resjunk-ctid route) or be switched off under a `currentTID`
consumer by the same walker. Reading ahead is a semantic change, not only a
performance one.

## Slice 2 (2026-10-16): scan-qual hot-path guards — measured, value-neutral

A CPU profile of a filtered TPC-DS SF1 `store_sales` seq scan (numeric/integer
range quals; private clone, capped server) put about 44 % in `evalQual` and
about 41 % in `decodeRowRangeInfo`. `evalFastExpr` was 27 % flat. Two pieces
of that time paid for checks that never succeed on such a qual:

- **`evalFastExpr` BinaryOp arm (exprnode.go).** `concatOperandsAsText` and
  `comparisonOperandsAsBpchar` were called on every binary operator, and each
  copied four Datums before returning its operands unchanged. Their own
  preconditions (`op == OpConcat`; a bpchar-typed side, `lbp > 0 || rbp > 0`)
  are now tested at the call site, so semantics are identical. This was about
  9 % of the scan.
- **`numericPayloadIsSpecialSpelling` (numeric_storage.go).** It ran
  `bytes.TrimSpace` before rejecting an ordinary digit string. A first-byte gate
  now rejects any ASCII byte that is neither whitespace nor a spelling's
  initial. Non-ASCII and whitespace bytes take the full path, so the answer is
  exactly the trimmed comparison's. Pinned by
  `TestNumericSpecialSpellingGateIsExact` against the reference
  TrimSpace+EqualFold rule, which covers Unicode whitespace and every 1–2 byte
  payload. This was about 3 %.

Measured on the same clone and query with server age held constant: about
1970 ms before and about 1590 ms after (median of the A/B). TPC-H acceptance arm:
values identical (24 MATCH), total 40.28 s → 39.73 s, Q6 0.77 s → 0.56 s
(`analysis/m0122/m0122-0012/accept`). Plans did not move: sf025
`PLAN-SHAPE same=99`, and fire-set A/B `fires=none` on both TPC-DS corpora.

The `decodeRowRangeInfo` share is the scan-resident batch successor's target.
It is still open as the SeqScanOp predicate-batch entry in
`unimplemented_feat.json`. The filterOp wiring entry (M0076-0002) is resolved
by slice 1 (`d6e42a7f7`).

## Slice 3 (2026-10-16): by-value integer deform arm — store-forwarding stall removed

After slice 2, `decodeRowRangeInfo` was 35.5 % of the filtered SF1 `store_sales` scan (`perf record -e cycles:u`),
and almost all of it was flat in the loop itself, not in the value decoder. `perf annotate` put 47.8 % of the
function's samples on one instruction: the first 16-byte load right after `decodePhysicalPGValueLowered` returns.

- **Cause.** `Datum` is 48 bytes and 11 words, so it is too large for Go's register ABI and is returned through a
  stack slot. Every `NewXxxDatum` arm writes that slot as three 16-byte zeroing stores, then a 1-byte `Kind` store
  and 8-byte field stores. The caller then copies the slot with 16-byte loads. A load that spans a wider and a
  narrower pending store cannot be store-forwarded, so it waits for the stores to retire. That happens once per
  column per row.
- **Ruled out.** Passing the 72-byte `array.OutputStyle` by pointer (the copy visible beside the call) measured
  ~1 % (1580 → 1570 ms), inside the noise. That experiment was not kept.
- **Change.** `colTypeInfo.intWidth` is resolved once per scan by `intWidthForLoweredName`: 2, 4 or 8 for the
  int2/int4/int8 spellings, 0 for anything else, including arrays. When it is set, `decodeRowRangeInfo` writes
  `NewIntDatum` straight into `dst[i]`. Alignment, the NULL bitmap, the missing-attribute arm and the exhausted-data
  arm all run first, unchanged. A value shorter than its width falls through to the generic decoder, which reports
  the error as before. This is PG's `fetch_att` for a by-value attribute (`postgres/src/include/access/tupmacs.h`),
  which `slot_deform_tuple` reads in place.
- **Sibling pin.** `TestDeformIntFastArmMatchesDecoder` checks the following against the generic path:
  - every spelling's width is what the generic decoder consumes, as a `KindInt`;
  - non-integer and array names get 0;
  - row decodes with alignment padding, a NULL and the extreme values of each width agree (`reflect.DeepEqual`,
    same end offset);
  - truncated payloads give the same error.
- **Measured.** Same SF1 clone and query, fresh server per arm, two rounds of 5 runs:
  - HEAD `f42e9a99b`: 1585 / 1592 / 1585 / 1602 and 1582 / 1605 / 1602 / 1594 ms (warm runs).
  - Candidate: 1279 / 1252 / 1300 / 1244 and 1245 / 1268 / 1167 / 1163 ms.
  - That is ~1590 → ~1250 ms, −21 %. The `decodeRowRangeInfo` share fell from 35.5 % to 20.3 %.
  - TPC-H acceptance arm: values identical (PASS vs `bench/tpch/baseline-digests.txt`), total 39.73 → 38.91 s.
    HammerDB declares TPC-H's keys NUMERIC, so this arm rarely fires there.
- **Plans and values.** sf025 `PASS=99 PLAN-SHAPE same=99`; fire-set `fires=none` on both corpora; full
  `TestPort_RegressSuite` passes (Rule #5: decode-path change).

The generic decoder still pays the same stall for every non-integer column, numeric included, which is the scan's
remaining deform cost. The fix there is the same shape (decode into the destination, or a per-type arm). The
SeqScanOp batch entry stays open in `unimplemented_feat.json`.
