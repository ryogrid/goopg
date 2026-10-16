# M0122-0012b — batch qual evaluation for the scan-resident prefilter

Status: evaluator landed 2026-10-16 (slice 7, M0122-0012), with no production caller yet. Wiring into `seqScanOp`
is the next slice; its plan is in §4. Parent design: `0122-0012-vector-filter-pipeline-recon.md` (slices 1-6).

## 1. Why

Slices 2-5 took the filtered TPC-DS SF1 `store_sales` scan from ~1970 ms to ~1095 ms by removing per-row Datum
traffic: column deform, stack-returned Datums, store-forwarding stalls. Slice 6 showed the rest of the qual cost is
the tree walk itself. `evalFastExpr` dispatches every node once per row, and the hot samples sit on the
instructions after each recursive call returns. More copy-shaving does not reach that. Evaluating each node over a
batch of rows pays the dispatch once per batch.

PostgreSQL's scan is row-at-a-time for the qual (`ExecScanExtended`, `execScan.h`), but page-at-a-time for
visibility. `heap_prepare_pagescan` (`postgres/src/backend/access/heap/heapam.c:555`) takes the buffer's share
lock once, and `page_collect_tuples` (`:505`) checks every tuple's visibility, collecting the visible offsets.
`heapgettup_pagemode` (`:1009`) then returns them holding only the pin. goopg's batch path borrows that page-mode
shape for its deform and qual phases; §4 says where the analogy stops.

## 2. The evaluator (landed)

`evalFastExprBatch(exprs, idx, rows, views, sel, ctx, scratch, out)` (`exprnode_batch.go`) evaluates the compiled
slab for each row `rows[sel[k]]` into `out[k]`.

- **Total.** Any slab evaluates correctly; only some kinds are faster.
  - Specialised kinds: leaves (`ExprConstVal`, `ExprIntConst`, `ExprBoolConst`, `ExprNullConst`, `ExprColumnRef`)
    and `ExprBinaryOp`.
  - Every other kind goes per row through `evalFastExpr` itself, via `views[i]`: a `SlotView` boxed once per batch
    buffer, so the fallback does not allocate per row.
- **Element semantics** are `evalFastExpr`'s:
  - A column ref copies the same Datum after the same `checkDeformPoison`. An out-of-range index returns an error,
    so the caller's per-row retry raises `evalFastExpr`'s exact error.
  - A BinaryOp's per-element work is `evalFastBinaryApply`, the function that holds the per-row arm's rare-shape
    code (below).
  - AND/OR build a selection vector of the rows the per-row arm would not short-circuit (`Kind == KindBool` and
    false for AND, true for OR). The right operand is evaluated only on those rows, so a right side that would
    raise on a short-circuited row (`x <> 0 AND 12 / x > 2`) is never evaluated there.
- **Errors.** The first error encountered is returned, and `out` is then undefined. Node-at-a-time order can raise
  a different row's error than row-at-a-time order would, so the contract is: on any error, the caller
  re-evaluates the batch per row. That is sound only for pure expressions, which is what the scan prefilter
  admits: `optimizer.PlanScanQual` excludes functions, subqueries and params. An abandoned batch attempt therefore
  has no side effects.
- **Scratch.** `exprBatchScratch` is a stack of Datum and selection vectors, one frame per BinaryOp level, reused
  across batches. A steady-state scan allocates nothing.

### 2.1 Sharing the BinaryOp work without slowing the per-row arm

The BinaryOp arm's post-operand work moved into `evalFastBinaryApply`, so both evaluators run the same code:
- the character-string comparison;
- the pg\_lsn probe;
- the concat and bpchar coercions;
- `evalBinary` / `evalConcat`;
- the int2/int4 overflow check.

Measured per-row on the store\_sales qual shape (`BenchmarkStoreSalesQualPerRow`, 64 rows):

| per-row arm calls the shared function | per 64 rows | vs HEAD 11.6 µs |
|---|---|---|
| by value `(Datum, error)` | 14.6 µs | +26 % |
| by pointer, out-param | 13.3 µs | +14 % |
| only for rare shapes; common case inline | 11.4-11.9 µs | +1.5-2.5 % |

The landed form is the last row:
- **`fastBinarySimple`** (inlined) holds when every preamble branch of `evalFastBinaryApply` is a no-op: no
  ambiguous text shape, no string operand, not a concat, no bpchar side. In that case the result is exactly
  `evalBinary` plus `fastIntOverflow`, and the per-row arm computes it inline.
- **Rare shapes** call the shared function.
- **`fastIntOverflow`** (inlined) is the single copy of the overflow check.

Two traps showed up here. A shared helper on the per-row hot path costs real time, because of the stack-returned
Datum (see slices 3-5). A sibling copy instead of a shared function would be free but would drift. The inline
predicate keeps both paths on one body.

### 2.2 Measured

`BenchmarkStoreSalesQualBatch` over the same 64 rows: 8.3-8.4 µs, against per-row HEAD 11.2-11.7 µs (−28 %). The
qual (`evalFastExpr` + `evalBinary` + `compareDatum` + `evalAnd`) is ~43 % of the filtered SF1 scan, so the
end-to-end ceiling is about 12 % before wiring overhead. That figure has to be measured, not assumed.

### 2.3 Pins

`TestEvalFastExprBatchMatchesPerRow` compares the batch to per-row `evalFastExpr` on every selected row:
- 18 expressions, 40 random batches each, with random selections;
- NULL and non-bool operands of AND/OR, cross-kind numeric/int, strings, arithmetic, int4 overflow, unary and NOT
  (the generic arm), constant-only, guarded and unguarded division by zero.

If any selected row raises per row, the batch must raise; if none does, it must not. The scratch stack must come
back balanced. Two mutations each fail it:
- disabling the AND/OR short circuit;
- reading the wrong column.

## 3. Not done here

There is no production caller yet. The SeqScanOp batch entry in `unimplemented_feat.json` stays open until §4
lands.

## 4. Wiring plan (next slice)

**Admission.** A page is batched only when all of the following hold. Anything else keeps today's per-tuple path
unchanged.
- The scan has a prefilter (`o.prefilterSet`) whose qual compiled (`o.pfIdx != noExpr`).
- The transaction is not serializable. goopg takes tuple-grain SIREAD locks, so batching would change which
  tuples are locked before a LIMIT stops the scan. PG takes a relation lock instead, so there is nothing to match.
- There is no GiST/GIN SSI mode, no TABLESAMPLE sampler, no enum injection and no catalog ACL rendering
  (`typeACLColIdx` / `attrACLColIdx` / `dbACLColIdx` < 0).
- No consumer above reads `currentTID` in lockstep, unless the emitted row's TID is what `currentTID` reports
  (see the invariants below).

**Phases per page**, under one `RLock` like `page_collect_tuples`:
1. For each line pointer in order: fetch, test visibility (skipping invisible tuples exactly as today), and
   deform cols `[0, MaxCols)` into batch row k of a per-scan buffer (rows of `len(o.cols)`, boxed views made once).
   Record the slot number, the resume offset, the toast flag and the hint-bit need.
2. Run `evalFastExprBatch` over rows without a toasted prefix. On any error, evaluate those rows per row with
   `evalQual`, in order, which keeps the early-error re-judge semantics.
3. For each survivor in order: re-fetch the tuple (the scratch `tupleBuf` was overwritten), deform the tail from
   the saved offset, detoast and materialise (`cloneRowOwnedPrefix` unless `borrowRows`), then queue it.
4. Release the RLock. Set the collected hint bits under one `Lock`. Count rejected rows into `statReturned` and
   `filterRemoved` as now.
5. `Next` pops the queue. Late-judged rows (errored early, or toasted prefix) are evaluated at their queue
   position, so an error surfaces after the rows that precede it, as per tuple.

**Invariants to pin when wiring:**
- **`currentTID`** must report the emitted row's TID, not the page's last slot. This is the slice-1 lesson: the
  batch filterOp's read-ahead made FOR UPDATE lock the wrong row. Test it with a `currentTIDProvider` consumer
  over a batched scan, or exclude those consumers through `disableFilterReadAhead`'s walker.
- **Borrowed rows** (`borrowRows`) must not be overwritten before the parent consumes them. The batch buffer is
  refilled only on the next page, after the queue drains.
- **Cancellation** is checked at least once per page.
- **Values and plans.** The sf025 sweep values, the fire-set, the TPC-H arm digests and the isolation family must
  all be unchanged. The scan reads ahead within a page, so run the isolation family (memory
  `stats_changes_need_isolation_family`; slice 1's `currentTID` break was an isolation spec).
