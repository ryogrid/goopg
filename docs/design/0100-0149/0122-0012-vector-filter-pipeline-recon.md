# M0122-0012: vector filter pipeline scope reconciliation

status: accepted
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
