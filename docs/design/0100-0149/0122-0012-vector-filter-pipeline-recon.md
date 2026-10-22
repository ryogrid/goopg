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

## Slice 4 (2026-10-16): in-place numeric deform arm

Slice 3's stall also hit every numeric column: the generic decoder's numeric arm builds its `Datum` in the
stack-returned slot. The arm's first exit already does the real work through two small-result calls,
`decodePhysicalPGVarlena` (payload, length, error) and `nodes.NumericInt64FromStoredPayload` (mantissa, scale, ok).
Both return in registers.

- **Change.** `colTypeInfo.isNumeric` (`numericFastForLoweredName`: plain `numeric`/`decimal`, not arrays) makes
  `decodeRowRangeInfo` call those two directly and write `Datum{Kind: KindNumeric, Int, Scale}` into `dst[i]`.
  Anything they decline goes to the generic arm unchanged: a varlena error, a legacy decimal-text payload,
  NaN/±Infinity, or a mantissa past int64. The generic arm re-reads the varlena and reports the same result or
  error.
- **Sibling pin.** `TestDeformNumericFastArmMatchesDecoder` compares against the generic path:
  - typmod'd and unconstrained numeric, plus `decimal`;
  - int64 extremes, a wide mantissa, a NULL, legacy text payloads including the special spellings, and a malformed
    varlena;
  - big-mantissa values are compared by rendered text, because their `Int` is a per-decode arena handle.
  - A mutation (mantissa + 1 in the arm) fails it.
- **Measured.** Same SF1 clone and query, fresh server per arm, two rounds:
  - HEAD `ee53c3319`: ~1250 ms warm (1253 / 1249 / 1245 / 1257, 1252 / 1249 / 1243 / 1255).
  - Candidate: ~1148 ms (1151 / 1146 / 1152 / 1150, 1146 / 1146 / 1145 / 1158). That is −8 %.
  - `decodeRowRangeInfo` share: 20.3 % → 13.3 %.
  - TPC-H acceptance arm (lineitem holds eight numeric columns): values identical, total 38.91 → 36.56 s, Q1 4.71 →
    3.89 s.
- **Gates.** sf025 `PASS=99 PLAN-SHAPE same=99`; fire-set `fires=none`; ea-ratchet PASS; full `TestPort_RegressSuite`
  PASS.

Across slices 2-4 the filtered scan went from ~1970 ms to ~1148 ms, and the TPC-H arm from 40.28 s to 36.56 s. The
profile is now led by expression evaluation: `evalFastExpr` 27.5 %, `evalBinary` 8 %,
`NumericInt64FromStoredPayload` 8 %, `compareDatum` 6.5 %.

## Slice 5 (2026-10-16): NewBoolDatum copies a stored result

After slice 4, `evalFastExpr` led the profile at 27.5 %. `perf annotate` spread its samples over Datum copies right
after the recursive child calls (`exprnode.go` left/right operands) and after `evalBinary`. It is the slice-3 stall
again, wherever the callee builds a fresh `Datum` with a byte-wide `Kind` store. Every comparison's result does
that through `NewBoolDatum`.

- **Change.** `NewBoolDatum` returns a copy of `boolDatums[0]` or `boolDatums[1]`, which hold the same two values it
  used to build. The copy writes the return slot at full width, so the caller's reload forwards. The array is
  unexported and never written. `TestNewBoolDatumTable` pins both values and checks that mutating a result cannot
  reach the table.
- **Ruled out.** Precomputing the `ExprIntConst` Datum at build time (returned via `constVal`) measured neutral to
  slightly worse: ~1183 / ~1200 ms against HEAD's ~1160 / ~1145 ms. It was not kept.
- **Measured.** Same SF1 clone and query, fresh server per arm, two rounds:
  - HEAD `93dafe89c`: ~1142 / ~1173 ms (warm medians).
  - Candidate: ~1104 / ~1088 ms. That is about −5 %.
  - TPC-H acceptance arm: values identical, total 36.56 → 35.93 s (Q1 3.89 → 3.60 s).
- **Gates.** sf025 `PASS=99 PLAN-SHAPE same=99`; fire-set `fires=none`; ea-ratchet PASS.

Across slices 2-5 the filtered scan went from ~1970 ms to ~1095 ms, and the TPC-H arm from 40.28 s to 35.93 s. The
remaining expression-evaluation cost is the recursive `(Datum, error)` return itself. Removing it means an
evaluator that writes results in place: a structural change, filed as the next slice rather than done piecemeal.

## Slice 6 (2026-10-16): operand leaf copy — measured inside the noise, not landed

After slice 5, `perf annotate` still put about 17 % of `evalFastExpr` on the left-operand call
(`left, err := evalFastExpr(...)` in the BinaryOp arm). After the return, the operand is copied three times: result
slot → temporary → `left`. Two variants copied a leaf operand straight from where it lives into the caller's local.
The leaf is a column of a `rowSlotView` or `*MaterializedSlot` (same bounds and deform-poison checks), or an
`ExprConstVal`.

- **Wrapper variant** (`evalFastOperandInto`; all operands go through the helper, non-leaves via `evalFastExpr`):
  - store_sales scan: −2.5 % (per-round medians 1114 / 1113 / 1107 → 1095 / 1078 / 1084 ms).
  - TPC-H Q6, a lineitem scan whose qual is an AND chain, was slower in both rounds (0.46 / 0.49 → 0.52 / 0.51 s).
    For non-leaf operands the wrapper adds a call layer and a copy.
- **Leaf-only variant** (`evalFastLeafInto` check, then the original call):
  - store_sales: 1136 / ~1096 / 1134 → 1111 / 1106 / 1047 ms; round 2 went the wrong way.
  - TPC-H Q1 / Q6 / Q14 over three rounds: no detectable difference.
- **Verdict:** inside the noise, so neither variant was landed. The code was discarded before commit; no
  `TestPort` or gate result from it is cited anywhere.

**Reading.** The samples on that line sit on the instructions right after the recursive CALL returns. They are not on
the copies. What remains is the cost of the tree-walk itself: a jump-table dispatch per node per row, and return
and branch prediction across the recursion. Removing per-row Datum traffic does not reach it. The lever is
evaluating each node over a batch of rows, so the dispatch is paid once per batch. That is the scan-resident batch
successor (the SeqScanOp entry in `unimplemented_feat.json`), and it must keep the `currentTID` read-ahead rule above.

## Slice 9 (2026-10-16): numeric decode's 128-bit division — measured inside the noise, not landed

After slice 8, `perf annotate` put about 20 % of `NumericInt64FromStoredPayload` (~9 % of the filtered SF1
`store_sales` scan) on the instruction after `bits.Div64`. That is the 128/64 DIVQ that strips the trailing
base-10000 digit's zero padding. A variant divided by a constant (10, 100, 1000, 10000) whenever the high word is
zero, letting the compiler multiply and shift instead. It was exactly equivalent (`Div64(0, lo, p)` is `lo/p, lo%p`)
and pinned by a randomized text-path oracle over scales 0-18; a wrong divisor failed it.

Measured, six rounds, fresh server per arm: HEAD ~1180 ms, candidate ~1180 ms, with no consistent order between
rounds. The DIVQ latency was already hidden by out-of-order execution; the samples on it were the instruction
waiting, not a bottleneck. Not landed; the code and its test were discarded before commit.

**The scan-qual micro-optimization route is exhausted.** Slices 6, 8 and 9 are three consecutive measured
non-improvements on the per-row filtered-scan path, after slices 2-5 took it from ~1970 ms to ~1095 ms. What remains
needs the single-pass page restructure (0122-0012b §5). The next M0122-0012 work should profile a different workload,
such as a hash join or aggregation (TPC-H Q1/Q9), before attempting another change.

## Slice 10 (2026-10-16): TPC-H Q1 — numeric promotion passes numeric operands through

Following slice 9's advice, a different workload was profiled. TPC-H Q1, the arm's slowest query, is a 4-worker
aggregate over `lineitem` with numeric arithmetic. On a private clone it runs in ~1.91 s warm, against PG 18.3's
~1.23 s on :65432. Its CPU profile:

| symbol | share |
|---|---|
| `evalFastExpr` | 17 % |
| `aggregateOp.applyAgg` | 9.9 % |
| `evalBinary` | 9.7 % |
| `promoteToNumeric` | 6.1 % |

Per-row allocation (`mallocgcTiny`, string concatenation in the group key) is only ~3 %.

`promoteToNumeric` runs on every numeric `+ - * /`. It returns both operands through `toNumeric`, which hands a
numeric back unchanged, so the common numeric-numeric case paid two 48-byte Datum round trips per operation. That is
the slice-3 stall pattern. `promoteNumericOperands` (numeric.go) calls `toNumeric` only for the side that is not yet
numeric; `evalBinary`'s add/sub and mul/div/mod arms use it. `evalBinary` is shared by both evaluators, so the siblings
change together. `TestPromoteNumericOperandsMatchesPromoteToNumeric` pins the result and error against
`promoteToNumeric` for every pairing of numeric, int, numeric-looking string and rejected operands.

**Measured:** Q1 on the private clone, three rounds, fresh server per arm:

| round | HEAD median (ms) | candidate median (ms) |
|---|---|---|
| 1 | 2114 | 2025 |
| 2 | 1959 | 1905 |
| 3 | 1959 | 1909 |

Every round favoured the candidate, by 2.5-4 %. The TPC-H acceptance arm passed with identical values. Its totals ran
on a loaded host (load average ~2.8, a peer's pre-commit gate running alongside) and are not comparable with earlier
slices.

Not done here:
- `applyAgg` lower-cases each aggregate's name per row (`strings.ToLower`, ~1 %).
- The group key is built as a string per row (`datumKey` + `setGroupKey`) and evaluated through the interpreter
  (`evalExprSlot`). Together these are ~5 % of Q1.
- The integer literal in `1 - l_discount` is still promoted per row; PG coerces it to numeric at parse time.

## Slice 11 (2026-10-16): the hashed aggregate's group key is built in place

The hashed grouping loop (`aggregateOp`, operators_join_agg.go) built its key on every input row from strings:
`datumKey` per grouping column, a type-tagged string such as `"s:" + value`, then `setGroupKey`'s
`strings.Builder` join for a multi-column key. It then looked the result up in the group map. TPC-H Q1 groups
6M lineitem rows on `(l_returnflag, l_linestatus)`, which is three small allocations per row in each of 4 workers.

- **`appendDatumKey`** appends `datumKey`'s bytes to a buffer. It covers null, bool, int, small numeric and string in
  place; every other kind, including the big-mantissa numeric lane, defers to `datumKey` itself.
- **`appendGroupKey`** appends `setGroupKey`'s bytes, including the multi-set prefix and the bpchar padding strip
  that `gkTrims` / `trimStringDatum` apply.
- **The loop** builds the key into the reused `gkBuf` and looks it up as `groups[string(gkBuf)]`, a conversion Go
  does not allocate for. The key string is created only when a row founds a group, so the map keys and the emission
  order are byte-identical to before.
- **Replaced:** `evalGroupKeysScratch` and its `gkKeys` scratch, by `evalGroupValsScratch`.
- **Pin:** `TestAppendGroupKeyMatchesSetGroupKey` compares the two paths byte for byte over random rows of every
  kind (big numerics, padded strings, bytes, time, interval), every set shape, with and without the multi-set prefix.
  Dropping the trim fails it.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `73559041a` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1884 / 1880 / 1876 / 1887 | 1642 / 1647 / 1640 / 1638 |
| 2 | 1857 / 1875 / 1876 / 1860 | 1653 / 1667 / 1641 / 1626 |
| 3 | 1852 / 1869 / 1874 / 1873 | 1640 / 1665 / 1647 / 1627 |

That is ~1875 → ~1645 ms, −12 %, far more than allocation's ~3 % share of the CPU profile suggested. Per-row
allocation across four parallel workers costs more than its own samples show. The TPC-H acceptance arm returned
identical values, with total 40.55 → 37.94 s and Q1 4.36 → 3.38 s. sf025 PASS=99 PLAN-SHAPE same=99, and the full
`TestPort_RegressSuite` passed.

Q1 against PG is now ~1.65 s vs ~1.23 s. What remains of the slice-10 list:
- the per-row `strings.ToLower` in `applyAgg`;
- interpreted group-key evaluation (`evalExprSlot`);
- the int literal promoted per row.

## Slice 12 (2026-10-16): applyAgg — cached aggregate names and an in-place numeric sum

After slice 11, Q1's profile put `applyAgg` at 11 % flat. `perf annotate` split its time across four lines:
- 34 % on `numericAdd` for the running sum (a stack-returned Datum);
- 20 % on the argument evaluation's return;
- 15 % on the per-row name tests;
- 10 % on storing the sum.

`strings.ToLower(call.Name)` ran on every row as well (1.3 % of Q1).

- **`aggLowerName`** caches each aggregate's lower-cased name by aggregate index. The cache is keyed on the source
  name, so a reused operator cannot be served a stale entry.
- **In-place sum.** In `sum` / `avg` over `KindNumeric`, when neither the running sum nor the argument is in the
  big-mantissa lane and both have the same scale, the sum is updated in place with `addInt64Overflow`. That is
  exactly the value `numericAdd` returns in that case: `alignNumericInt64` changes nothing at one scale, and the
  result Datum carries no other field. Mixed scales, big values and overflow take `numericAdd` as before.
- **Pin:** `TestAggNumericSumInPlaceMatchesExact` checks `sum` against `math/big` over three columns: a same-scale
  `numeric(15,2)` (the in-place path), mixed scales 0-3 (the aligned path), and near-int64 integers whose sum
  overflows into the big lane. A `+1` mutation in the in-place update fails it.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `91d7e0143` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1685 / 1709 / 1689 / 1657 | 1567 / 1551 / 1533 / 1528 |
| 2 | 1629 / 1622 / 1694 / 1685 | 1563 / 1552 / 1541 / 1561 |
| 3 | 1631 / 1629 / 1634 / 1658 | 1560 / 1552 / 1579 / 1571 |

That is ~1660 → ~1555 ms, −6 %. The acceptance arm's single-shot totals were flat (37.94 → 37.93 s), inside its
known ±15 % per-query noise; values identical. sf025 PASS=99 PLAN-SHAPE same=99, and `TestPort_RegressSuite`
passed.

Across slices 10-12, Q1 went from ~1.91 s to ~1.55 s, against PG 18.3's ~1.23 s.

## Slice 13 (2026-10-16): the seq scan reads tuples in place

After slice 12, `runtime.memmove` was 6.5 % of TPC-H Q1, all of it from `seqScanOp.Next`.
`PageGetHeapTupleInto` copied every tuple's bytes into a per-scan scratch buffer before decoding. PG reads a heap
tuple in place under its buffer pin (`heapgettup_pagemode` returns a pointer into the page). `seqScanOp` now does
the same with `PageGetHeapTupleNoCopy`, which the index scan's hot path already uses (M0092-0006).

Why aliasing the page is safe here:
- **Every read of `tuple.Data` / `tuple.Bitmap` happens before that tuple's `RUnlock`.** That covers the prefix
  deform, the tail deform, detoast, the GiST-SSI match and `cloneRowOwnedPrefix`. After the unlock only
  `tuple.Header`, a value copy, is read: the SSI conflict hooks and the hint-bit decision.
- **No decode arm returns a Datum aliasing the tuple bytes.** Varlenas land in the per-page arena or in owned
  memory; `TestDecodedRowDoesNotAliasSourceBuffer` pins this.
- **Ring-buffer pages** are private copies that stay valid until the scan moves to the next page.

The unused `tupleBuf` field was removed.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `ef43b333b` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1598 / 1558 / 1575 / 1547 | 1511 / 1526 / 1495 / 1493 |
| 2 | 1604 / 1590 / 1577 / 1557 | 1488 / 1504 / 1517 / 1496 |
| 3 | 1517 / 1511 / 1559 / 1572 | 1511 / 1516 / 1516 / 1501 |

That is median ~1563 → ~1505 ms, about −3.5 %. The candidate's median was lower in every round, but round 3
overlaps. The acceptance arm returned identical values; its single-shot totals (37.93 → 38.01 s) are inside its
noise.

**Gates:** units, the full `TestPort_Isolation*` family (this changes how the scan touches shared page memory),
tpch-spotcheck, sf025 PASS=99 PLAN-SHAPE same=99, and `TestPort_RegressSuite`.

## Slice 14 (2026-10-16): the numeric fast paths test the big-lane flag

numericAdd, numericSub, numericMul, numericDiv and numericCmp chose the int64 lane with
`a.NumericBigValue() == nil && b.NumericBigValue() == nil`. `NumericBigValue` is not inlinable, so every numeric
operation paid two calls just to learn that the flag was clear; on a flagged value it would also decode a
`*big.Int` only to compare it with nil. The new helper `isBigNumeric(d)` is `d.Flags&flagBigNumeric != 0`, and it
inlines at all 17 call sites. `numericMant` and `datumToInt64Key` use it too.

The swap is exact. `NumericBigValue` returns nil only when the flag is clear; every flagged path returns a value,
including a zero `big.Int` when the arena is gone. `TestIsBigNumericMatchesNumericBigValue` checks the two agree on
the int64 lane, a stored big mantissa, a flagged datum with no arena and non-numeric kinds. It also checks that a
big operand still takes the exact lane.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `fc4087fc1` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1507 / 1506 / 1499 / 1504 | 1462 / 1473 / 1461 / 1467 |
| 2 | 1493 / 1510 / 1486 / 1474 | 1400 / 1417 / 1429 / 1447 |
| 3 | 1490 / 1490 / 1472 / 1466 | 1444 / 1482 / 1491 / 1445 |

That is median ~1490 → ~1455 ms, about −2.4 %. The candidate's median was lower in every round, but round 3
overlaps. The acceptance arm returned identical values.

**Gates:** units, tpch-spotcheck, sf025 PASS=99 PLAN-SHAPE same=99, and `TestPort_RegressSuite`.

## Slice 15 (2026-10-16): evalBinary promotes int operands in place

TPC-H Q1 computes `l_extendedprice * (1 - l_discount)` and `... * (1 + l_tax)` for every row. PG coerces the
literal `1` to numeric at parse time. goopg keeps it as an int and promotes it at evaluation time:
`evalBinary`'s numeric arms called `promoteNumericOperands`, which returned two 48-byte Datums through its stack
slots (store-forwarding, the slice 3-5 pattern). That happened on all four arithmetic operations per row, including
the two whose operands were both numeric already. The function was 4.5 % of Q1's profile.

Both arms now rewrite a `KindInt` operand in place with `numericFromInt`, through `promoteIntOperand(*Datum)`, which
inlines. They call `promoteNumericOperands` only when an operand is still not numeric (a string that failed to parse,
or a kind that raises 42883). For an int, `toNumeric` returns exactly `numericFromInt`, so the result is identical.
`TestEvalBinaryNumericArmsMatchPromoteToNumeric` compares `evalBinary` with promote-then-operate, for `+ - * / %`
over numeric, big numeric, int, numeric-looking string and rejected operands, including the errors. It fails if
the promoted literal's representation changes.

A first attempt put the fast path in a helper that returned the pair. It was not inlinable (cost 129), so it would
have kept the stack return; it was replaced before measurement.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `3a9d9a88c` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1487 / 1451 / 1456 / 1427 | 1328 / 1329 / 1307 / 1327 |
| 2 | 1469 / 1464 / 1465 / 1456 | 1287 / 1310 / 1299 / 1309 |
| 3 | 1479 / 1440 / 1469 / 1429 | 1324 / 1315 / 1312 / 1285 |

That is median ~1460 → ~1310 ms, about −10 %, with no overlap between the arms. As with slice 11, the saving is
larger than the function's profile share; the removed stack round trip also stalled its callers. The acceptance arm
returned identical values.

**Gates:** units, tpch-spotcheck, sf025 PASS=99 PLAN-SHAPE same=99, and `TestPort_RegressSuite`.

## Slice 16 (2026-10-16): applyAgg takes its AggregateCall by pointer

The per-row aggregate loops (the hashed drain in `Open`, `openSorted` and `openSortedPartialEmit`) iterated
`for i, call := range o.plan.Aggs`, then called `applyAgg(st, call, slot, i)` with `call optimizer.AggregateCall`
by value. `unsafe.Sizeof(AggregateCall{})` is 320 bytes. Its three `catalog.Type` fields, the slices and the
expressions make it large. Q1 has eight aggregates, so each input row copied 8 × 2 × 320 B before any work. Slice
6 left the expression tree walk alone, because the samples there were on the recursion, not on copies; this copy
sits outside the walk.

The loops now take `call := &o.plan.Aggs[i]`, and `applyAgg` takes `*optimizer.AggregateCall`. `applyAgg` never
assigns to `call` or forwards it by value; every use is a field read or `call.Pos()`. So the change cannot alter
behaviour, and the existing `applyAgg` tests (`join_agg_compile_test.go`, `parallel_agg_combine_test.go`) cover it.
The window operator's two calls pass the address of their per-function local. `finishAgg` still takes the call by
value: it runs once per group, not once per row.

**Measured:** Q1 on a private TPC-H clone, three rounds, fresh server per arm:

| round | HEAD `67c5a5091` (ms) | candidate (ms) |
|---|---|---|
| 1 | 1330 / 1316 / 1281 / 1297 | 1261 / 1231 / 1220 / 1233 |
| 2 | 1272 / 1257 / 1280 / 1285 | 1243 / 1212 / 1257 / 1274 |
| 3 | 1299 / 1301 / 1283 / 1278 | 1205 / 1211 / 1206 / 1218 |

That is median ~1290 → ~1225 ms, about −5 %. The candidate's median was lower in every round, but round 2 overlaps.
Across slices 10-16, Q1 went from ~1.91 s to ~1.22 s, level with PG 18.3's ~1.23 s on the same corpus. The acceptance
arm returned identical values.

**Gates:** units, tpch-spotcheck, sf025 PASS=99 PLAN-SHAPE same=99, and `TestPort_RegressSuite`.

## Slices 17 onward

Slices 17 onward (the TPC-H Q17 and Q13 scan pipeline: Project folding, the scan row borrow, walking past
unread columns) continue in [0122-0012-tpch-scan-pipeline-slices.md](0122-0012-tpch-scan-pipeline-slices.md),
split out on 2026-10-21 under the 800-line design-doc limit.
