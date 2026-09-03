# 02 — goopg's current row representation

**Verified against the working tree at 2026-09-03.** Every claim carries
`internal/...:line` or a symbol name. Where a fact contradicts an older document,
the contradiction is stated rather than silently corrected.

Architectural context lives in take3 `11-executor-goopg-design.md` §§3, 13 and is
not repeated. This document covers what 04 has to change and what it can reuse.

---

## 1. `Datum` — 48 bytes, pinned

`internal/executor/datum.go:171-184`:

| offset | size | field | note |
|---:|---:|---|---|
| 0 | 1 | `Kind DatumKind` | narrowed from `int` at M0107-0002 |
| 1 | 1 | `Flags uint8` | `flagBigNumeric`; others reserved |
| 2 | 2 | `ArenaID mmgr.ContextID` | replaced `*mctx.Context`; 0 = no arena payload |
| 4 | 2 | `Scale int16` | numeric scale |
| 6 | 1 | `TimeSub TimeSubtype` | carved out of the alignment pad (M0127-P5.9-u) |
| 7 | 1 | `_pad0` | |
| 8 | 8 | `Int int64` | |
| 16 | 24 | `Buf []byte` | nil when arena-backed |
| 40 | 8 | `Hi uint64` | interval sub-day micros; UUID high half |

Pinned at exactly 48 by `const _ uintptr = 48 - unsafe.Sizeof(Datum{})`
(`datum.go:187`) and by `datum_arena_test.go:17-19`. `Row` is `[]Datum`
(`datum.go:870`); the slice header is 24 bytes.

**The arena.** Hot-path `KindString`/`KindBytes` carry `ArenaID != 0` and pack
`(offset<<32 | length)` into `Int`, with `Buf == nil` — **zero GC-traced pointers
per Datum** on the scan path (`datum.go:158-174`; design
`docs/design/perf-optimize/02-datum-pointer-free.md`). Payload access resolves
`mmgr.Lookup(d.ArenaID)` per call (`datum.go:210,234,354,453,661,669`).

So goopg has *already* moved variable-length payloads out of line. The 48 bytes
are pure per-column fixed overhead. Against PostgreSQL's 8 (01 §7), the
Datum-array-versus-packed-bytes ratio that motivates `MinimalTuple` is **six
times more severe in goopg than in PostgreSQL**, and the `Buf` field's GC-scan
cost has no PostgreSQL analogue at all.

**NULL is a Kind, not a flag.** `KindNull DatumKind = iota` (`datum.go:22`), so a
zeroed `Datum` is SQL NULL; `NullDatum` `:191`; `IsNull()` `:195`. The documented
invariant for `KindNull` is "all zero" (`:154`).

**A second discriminator rides `KindTime`.** `TimeSubtype` (`datum.go:88-147`) —
`TimeSubTimestamp`/`Date`/`TimestampTZ`/`Time`/`TimeTZ` — all share one carrier
Kind. Its doc comment is a post-mortem: it *was* a `Flags` bit, `encodeDatum`
failed to serialise it, and every spilled DATE silently became a bare timestamp.
TPC-DS Q72 failed at small `work_mem` and passed at 2 GB. The fix was to promote
it to a struct field so `TestSpillDatumRoundTripCoversEveryKind` could enumerate
it; `datumKindCount` (`:69`) and `timeSubtypeCount` (`:145`) exist solely to make
that test total.

**04 must treat this as precedent, not trivia:** a packed format that round-trips
*values* but drops a *type discriminator* passes value-comparison tests and fails
in production, at a size threshold, on one query.

---

## 2. The slot layer — already the inter-operator currency

`Operator` (`internal/executor/operator.go:34-39`) returns a `TupleSlot`, not a
`Row`:

```go
type Operator interface {
	Open(ctx *Context) error
	Next() (TupleSlot, error)
	Close() error
	Schema() optimizer.Schema
}
```

There are **75** non-test implementations (`grep -c ') Next() (TupleSlot,
error)'`). `operator.go:49-66` documents the *removed* `BorrowSemantics` /
`OwnedRow` / `BorrowedRow` lifetime protocol, replaced by lifetimes encoded in
the slot kind.

`TupleSlot` (`slot.go:18-45`) = `SlotView` + `Schema()` + `Width()` + `Row() Row`
+ `Materialize() *MaterializedSlot` + `Release()` + `TID()`. `SlotView`
(`slot.go:52-55`) is just `Get(col int) Datum` and `IsNull(col int) bool`.

Four implementations exist:

| type | file | shape |
|---|---|---|
| `MaterializedSlot` | `slot.go:68-74` | owns a `Row`; carries ctid |
| `VirtualSlot` | `slot.go:135-143` | `sources []TupleSlot` + `cols []virtualCol{sourceIdx, sourceCol int16}` |
| `Slot` | `opnode.go:62-72` | Phase-C concrete, stack-allocatable, `Cells []Datum` + `HasRow` |
| `rowSlotView` | `slot.go:60` | bare `Row` adapter, zero-cost |

**Correction to an earlier document.** `FINDING-p401-alone-is-not-enough.md`
says `TupleSlot.Row()`'s "future `VirtualSlot` materializes lazily" comment shows
the lazy slot "was anticipated; it was never built". The intent reading is right,
the naming is not: `VirtualSlot` **exists** and is a *column-reference* slot
(column N = `sources[i].Get(j)`), which is neither PostgreSQL's virtual slot
(01 §5) nor a packed slot. What is missing is a slot over packed bytes. 04 uses
the name `PackedSlot` to keep the two apart.

`VirtualSlot` is nonetheless the proof of concept that matters: a non-`[]Datum`
slot already flows through the pipeline today.

**`Row()` is the choke point.** Every consumer that is not slot-native calls it.
For `VirtualSlot` it already allocates and fills (`slot.go:173-179`).

---

## 3. Where rows are retained — the sites that pay

Each of these holds `48 × columns + 24` bytes per retained row.

| site | field | file:line |
|---|---|---|
| hash join build (string key) | `lazyHash map[string][]Row` | `operators_join_agg.go:53` |
| hash join build (int key) | `lazyIntHash map[int64][]Row` | `operators_join_agg.go:71` |
| sort | `rows []Row` | `operators.go:769` |
| materialize | `mem []Row` | `operators_material.go:68` |
| CTE cache | `CTERowCache` | `context.go:623` |
| recursive worktable | `WorkTableRows` | `context.go:364` |
| aggregate hash table | `groups map[string]*groupRuntime`, each with `groupValues Row`, `passthroughVals Row` | `operators_join_agg.go:2009`, `:1850-1855` |
| aggregate input buffer | `rows []Row` on `aggregateOp` | `operators_join_agg.go:1823-1841` |

Two of these are the same data twice: `operators_join_agg.go` maintains **both**
`lazyHash` and `lazyIntHash`, so peak build memory on the int-key path is ~2×
(take2 07 §6 records this separately).

`hashsize.EntryBytes` (`internal/executor/hashsize/hashsize.go:121-128`) models it
as `ncols × DatumBytes + RowSliceBytes + avgVarBytes` = `ncols × 48 + 24 +
avgVarBytes`, with `DatumBytes` at `:46`. `estimatedRowBytes` in `spill.go`
matches. The model is **faithful** — that was checked in
`FINDING-p401-alone-is-not-enough.md` §"Two cheap alternatives, ruled out", and
the planner is not over-pricing hash builds.

Four sites in the source say so in their own words. Only `hashsize.go:29`
carries the sentence *"a packed MinimalTuple. goopg has no such thing"*;
`optimizer/cost_funcs.go:296` says a PG hash entry "is a packed MinimalTuple
whose size follows the byte width", `:385` that "`relation_byte_size`'s
MinimalTuple math is replaced by `hashsize.EntryBytes`", and
`optimizer/path.go:230-231` that "a goopg hash entry is a `[]Datum` and not a
packed MinimalTuple". The cost model has been written around the absence.

---

## 4. Where rows are produced, and the one existing lazy deform

`seqScanOp` (`operators_storage.go:1877`) is the most evolved path and already
implements PostgreSQL's discipline in miniature:

- reusable decode buffer `o.scanRow` from `acquireRow(len(o.cols))` (`:1151`,
  `:2082`), pooled;
- cached boxed `SlotView` `o.scanSlot` (`:1041-1044`), to avoid
  `runtime.convTslice` per row;
- pre-resolved `o.colInfo []colTypeInfo`;
- **two-phase deform** (`:2090-2126`): when `o.prefilterSet`, decode
  `[0, prefilter.MaxCols)`, evaluate the compiled prefilter, `continue` on
  reject, otherwise decode `[MaxCols, len(cols))` **resuming at `off`**. The
  comment records the payoff: *"On TPC-H Q6 that is 6 of 16 columns and ~2 % of
  rows."*
- then `DetoastRow` if needed (`:2141`), then **`row = cloneRowOwned(row)`**
  (`:2181`) — unconditional, because a concurrent UPDATE could tear the bytes
  before the page RLock is released. This is the mandatory per-row allocation.

The other scans do not narrow. `indexScanOp` (`operators_index.go:543`) calls
`DecodeHeapTupleRowInto(o.scanRow, ..., tuple, nil)` (`:611`) — full width, and
note the `nil` mctx, so no arena on that path. `bitmapHeapScanOp`
(`operators_bitmap.go:458`) decodes full width (`:1030-1037`).
`indexOnlyScanOp`'s heap fallback `decodeRowFromHeap` (`operators_indexonly.go:789`)
decodes the **full** row with an allocating `DecodeHeapTupleRow` and then throws
away everything the index does not cover.

---

## 5. The PG-format codec that already exists

This is the finding that changes the shape of the work: **goopg is not missing a
PG tuple format. It has one, on disk, and it has been the only on-disk row format
since M0111-0002.**

### 5.1 Header

`internal/storage/heap.go:372-382`:

```go
type HeapTupleHeader struct {
	Xmin, Xmax, Xvac TransactionID
	CTID             ItemPointer
	Infomask         uint16
	Infomask2        uint16
	Hoff             uint8
}
```

with `HeapTuple{ Header; Bitmap []byte; Data []byte }` (`:389-393`) —
note the bitmap is a separate Go field and is merged only at marshal time.

- `SizeOfHeapTupleHeaderData = 23` (`:14`), `DefaultHeapTupleHoff = 24` (`:18`),
  both pinned by `heap_test.go:754,758`.
- `MarshalBinary` (`:472-509`) writes PostgreSQL's exact field order and
  endianness, **including the frequently-mis-ordered `t_infomask2` before
  `t_infomask`**, and leaves the bitmap-to-`hoff` gap zeroed as the MAXALIGN pad.
- `ParseHeapTuple` (`:500`) copies; `parseHeapTupleAlias` (`:516`) is
  zero-copy against the pinned page.
- `maxAlign8` `:436`; `NewHeapTupleWithNulls` `:445` stamps `HeapHasNull` and
  computes `hoff = maxAlign8(23 + len(bitmap))`; `SetNatts` `:399`.
- Infomask bits at `:174-192`; `HeapHotUpdated`/`HeapOnlyTuple` are correctly in
  **`t_infomask2`** with an extended comment (`:150-172`) recording the
  2026-08-11 bug where they lived in `t_infomask` and made HOT chains mutually
  unreadable between goopg and PostgreSQL.

### 5.2 Data area

`internal/executor/codec.go` — despite the file name this is the *physical heap
tuple* codec, not a wire codec (§7).

| need | function | line |
|---|---|---|
| Datum array → packed data area | `EncodeRowPG` / `EncodeRowPGCtx` / `encodeRowPGCtx` | `:41`, `:50`, `:89` |
| per-value pack | `encodeValuePG` / `encodeValuePGCtx` | `:427`, `:440` |
| null bitmap build | `NullBitmapPG` | `:64-82` |
| null bitmap read | inline in `decodeRowRangeInfo` | `:1369` |
| varlena short/long emit | `varlenaTextBytes`, `varlenaBytes`, `varlenaPayloadBytes` | `:1099`, `:1084`, `:1076` |
| varlena header dispatch on read | `decodePhysicalPGVarlena` | `:1999-2027` |
| typalign | `alignPhysicalPGOffset`, `physicalPGTypeAlign`, `physicalPGTypeAlignLowered` | `:1449`, `:1457`, `:1464` |
| attlen == -1 predicate | `pgPhysicalTypeIsVarlena` | `:1476-1502` |
| `HEAP_HASVARWIDTH` / `HEAP_HASEXTERNAL` inputs | `pgRowHasVarWidth`, `pgRowHasExternal` | `:1511`, `:1536` |
| header-driven decode | `DecodeHeapTupleRowInto`, `DecodeHeapTupleRow` | `:1403`, `:1411` |

`EncodeRowPG`'s own comment (`:28-40`) says it "mirrors PostgreSQL's heap tuple
layout"; `NullBitmapPG`'s (`:57-63`) says the convention "matches PG's
`heap_fill_tuple`: bit i is set when column i is NOT NULL". Both are correct
(01 §3).

The varlena emitters are byte-exact: `total = len+1`; if `<= 127` emit
`byte(total<<1)|1` then the payload, else a 4-byte LE `uint32(total)<<2`. That is
`SET_VARSIZE_SHORT` / `SET_VARSIZE`. It is pinned byte-for-byte by
`canonical_tuple_bytes_test.go:39-60` (`'bootstrap'` → `0x15` plus 9 bytes,
hoff 24).

`decodePhysicalPGVarlena` reads all four PostgreSQL varlena shapes — short,
4-byte uncompressed, 4-byte PGLZ-compressed (`VARATT_IS_4B_C` → `pglz.
DecodeInlineCompressed`) — and rejects only external.

PGLZ is real and upstream-validated: `internal/access/common/pglz/pglz.go`
`Compress` `:79`, `Decompress` `:191`, `BuildCompressedVarlena` `:254`,
`DecodeInlineCompressed` `:268`, golden tests in `upstream_golden_test.go`.

### 5.3 The `slot_getsomeattrs` analogue — already written, with no owner

`DecodeRowRangeIntoMctxPGTupleStyled(dst, cols, data, bitmap, storedNatts, sctx,
style, from, to, off) (int, error)` — `codec.go:1331`, workhorse
`decodeRowRangeInfo` `:1340`. It decodes columns `[from, to)` resuming at byte
offset `off` and **returns the offset reached**.

Its doc comment (`:1315-1330`) states this document's premise verbatim, and also
its central constraint:

> The range form exists so a scan can deform only the columns its own predicate
> reads, test that predicate, and pay for the remaining columns only on the rows
> that survive — PostgreSQL's `slot_getsomeattrs` discipline. **A physical tuple
> has no column offset array, so a suffix can be skipped but a prefix cannot;**
> the returned offset is what makes resumption exact, and it must be threaded
> back unchanged or the second half decodes garbage.

**The function exists; the watermark has no owner.** Today `seqScanOp.Next`
carries `(from, to, off)` by hand across two calls within one `Next()`. Nothing
holds `(nvalid, off)` *across* operator calls, which is what PostgreSQL's
`tts_nvalid` + `HeapTupleTableSlot.off` do (01 §4). That is precisely the gap 04
fills.

`ALTER TABLE ADD COLUMN` fast defaults are already handled:
`decodeRowRangeInfo` returns `c.MissingValue` for `i >= storedNatts`
(`codec.go:1360-1367`) — PostgreSQL's `attmissingval` rule (01 §4).

`colTypeInfo` / `resolveColTypeInfo` (`internal/executor/coltypeinfo.go:26-40`)
is the per-column memoization passed into `decodeRowRangeInfo`. Its comment
(`:12-25`) names PostgreSQL's `TupleDesc`/`heap_deform_tuple` as the pattern and
quantifies what it saved: goopg lowercased `catalog.Type.Name` three times per
value, costing **4.64 % of TPC-H Q14's CPU and 6.13 % of Q3's**. It also states
the staleness contract — *"it MUST be derived wherever the column list itself is
resolved (an operator's `Open`), never cached against a table across DDL."*

Only `seqScanOp` populates it. `indexScanOp`, `bitmapHeapScanOp` and
`indexOnlyScanOp` pass `nil` and re-derive per value.

### 5.4 A second PG-tuple decoder

`internal/catalog/codec.go` holds an independent decoder for bootstrap catalogs:
`DecodePGClassPhysicalRow` `:878`, `DecodePGAttributePhysicalRow` `:970`,
`DecodePGTypePhysicalRow` `:1056`, `DecodePGIndexPhysicalRow` `:1220`,
`DecodePGStatisticPhysicalRow` `:1491`. `internal/access/nbtree/pgformat.go` is
the index-tuple analogue.

This one matters for 03: `decodeTextArray` (`internal/catalog/codec.go:1661-1709`)
is the **only place in goopg that implements PostgreSQL's conditional
alignment** — `if off < len(blob) && blob[off] == 0 { off = (off+3) &^ 3 }`
(`:1693-1695`), with a comment explaining the bug it fixes.

---

## 6. The other, private packed encoding

`internal/executor/spill.go` already converts `Row → bytes → Row`, but in a
**goopg-private kind-tagged TLV**, not the PG format:

- `appendRowPayload(buf, row)` `:112` — `binary.AppendUvarint(len(row))` then
  `encodeDatum` per Datum;
- `encodeDatum(d, buf)` `:326` — a `DatumKind` byte then a per-kind payload; it
  resolves arena indirection at encode time via `d.StringValue()`/`BytesValue()`;
- `decodeRowPayload(data, dst)` `:274`; `spillReader.ReadRowInto` for the
  buffer-reusing form;
- frame: 4-byte LE length prefix; `bufio.Writer` sized to
  `hashsize.FileBufferBytes` so the cost model's one-BLCKSZ-per-batch-file
  assumption is true rather than aspirational (`:66-72`);
- files under `<datadir>/base/pgsql_tmp/pgsql_tmp<pid>.*`, registered with the
  Context so statement end unlinks them;
- `rowsOp` `:651` and `spillOp` `:674` read them back as `Operator`s;
- guarded by `TestSpillDatumRoundTripCoversEveryKind`, which walks every
  `DatumKind` **and** every `TimeSubtype` (§1).

**`WriteRowHashed` (`:104`) already mirrors `ExecHashJoinSaveTuple`'s
hash-value-then-tuple framing and cites `nodeHashjoin.c:1414`.** So the framing
survives a format change; only the payload encoder/decoder pair swaps.

This is a second retention encoding, and its bug history (§1, the `TimeSubtype`
post-mortem) is the best available evidence about what a packed format loses.

---

## 7. The wire protocol is text, and is not this codec

- Non-zero result format codes in Bind are rejected —
  `internal/postmaster/extended.go:228-235`, `errcodes.FeatureNotSupported`,
  *"binary result formats are not supported"*. `Format: 0` is hardcoded in the
  `RowDescription` at `internal/postmaster/dispatch.go:3739`.
- The client loop is `dispatch.go:3746-3796` (simple) and `:4605-4627`
  (extended): `slot.Row()` at `:3760`, then per cell
  `s.appendTypedCellText(...)` (`:3782`) or `d.AppendValueText` (`:3785`), with
  the nil-vs-empty distinction at `:3786-3792`, then
  `w.PutDataRowScratch`. Wire encoders `internal/libpq/messages.go:476`, `:507`.
- The only PG-binary output goopg produces is COPY BINARY
  (`internal/executor/copy_binary.go`, gated at `copy.go:18`).

Consequence for 04: **the top of the pipeline cannot consume packed bytes
directly** — output is text, type- and GUC-driven (DateStyle, array output
style), and needs the semantic Datum. But it consumes columns *in ascending
order and immediately*, never retaining, which is the ideal access pattern for a
prefix-walk lazy deform. `slot.Row()` at `dispatch.go:3760` is the only thing
forcing full materialisation there.

---

## 8. What must not be broken

### 8.1 The type-assertion capability surface

`internal/executor/rowshape_assert.go:39-48` is an in-tree post-mortem of the
wrapper approach, worth quoting because it bounds 04's implementation choices:

> **WHY NOT A WRAPPER.** The obvious implementation — wrap every Operator in
> `maybeInstrument` — was built, and it CHANGED QUERY RESULTS: TPC-H went to 7
> VALUE-DIFF / 4 ROWS-DIFF with zero assertion failures, i.e. the wrapper itself
> was the bug. This package discovers operator capabilities by TYPE ASSERTION …
> and an opaque wrapper hides all of them, so a type switch falls to its default
> arm.

There are ~26 such sites. The ones that matter for a **new slot type** are the
switches over `TupleSlot`/`SlotView`, because a missing arm fails *silently*:

| site | file:line | failure mode if `PackedSlot` is missing |
|---|---|---|
| `slotToRow` | `slot.go:230-259` | `default: return nil` — the row becomes nil |
| `evalFastExpr` `ExprColumnRef` arm | `exprnode.go:288` | falls through to an unchecked `slot.Get(colIdx)` |
| `evalExprSlot` ColumnRef hoist | `expr.go:413` | four per-type bounds guards; an unlisted type skips them |
| ctid propagation in `fillFromTupleSlot` | `opnode.go:176` | `default: hasCTID = false` |
| ctid propagation in `projectOp.Next` | `operators.go:367` | same |
| `VirtualSlot` fast path | `opnode.go:153` | performance only |

`slotToRow`'s `*Slot` arm carries its own post-mortem comment
(`slot.go:246-251`): when that arm was missing, expressions that convert the slot
to a Row (`InExpr`, `CaseExpr`, `SubqueryExpr`, `ExistsExpr`, `ExtractExpr`,
`FuncCall`) fell to `default` and produced spurious "nil slot" errors. **The
exact bug 04 must not repeat, already committed once.**

`exprnode.go:288`'s comment explains why it is a concrete switch rather than a
`Width() int` capability interface: the itab lookup measured ~1.4 ns/eval on
`BenchmarkJoinKeyEval` and made the compiled path slower than the interpreter it
replaced. **Adding a fifth arm is cheap; widening the interface is not.**

### 8.2 Expression evaluation is already slot-native — but with escape hatches

Two evaluators, enforced-in-sync by `expr_sibling_parity_test.go`:

- interpreted `evalExprSlot(e, slot SlotView, ctx)` — `expr.go:413`, with
  `evalExpr(e, row, ctx)` (`:352`) wrapping a `Row` in `rowSlotView`;
- compiled `evalFastExpr(exprs exprTreeSlab, idx int32, slot SlotView, ctx)` —
  `exprnode.go:288`, over a flattened `exprTreeSlab []ExprNode` (`:106`) built at
  `BuildFast` time.

Columns are referenced **by flat integer index into the slot** —
`optimizer.Expr`'s doc comment (`internal/optimizer/plan.go:44-47`) states the
contract. Both paths bounds-check, because a raw panic once escaped the hash-join
build drain (which `gatherOp.Open` runs in the leader goroutine, outside
`ParallelGroup.Go`'s recover) and closed the client socket — TPC-DS Q8,
`"index out of range [57] with length 1"` (`expr.go:439-449`,
`exprnode.go:305-317`).

**The escape hatches.** `evalExprSlot` calls `slotToRow(slot)` — a **full
materialisation** — for `CaseExpr`, `SubqueryExpr`, `ArraySubqueryExpr`,
`MultiAssignSubqElem`, `ExistsExpr` and `ExtractExpr` (`expr.go:479-502`), plus
`InExpr` inside `evalInExpr` (`:10246`), two row-constructor helpers (`:10370`,
`:10458`) and `operators_memoize.go:220`. **`FuncCall` is not one of them** —
`expr.go:1307-1308` passes the slot (`evalFuncCall(x, slot, ctx)`). The name
appears in `slot.go:247-252`'s historical post-mortem list, which is where it
belongs. `ctx.OuterRows` is `[]Row`; `ctx.ParamExec` is `[]Datum`.

And `ExprAdapter` (`exprnode.go:74`) — the compiled form's catch-all for every
cast, function call and CASE — holds an opaque `optimizer.Expr` and delegates.
Any static "highest attribute referenced" analysis must therefore fall back to
`len(cols)` for a subtree containing an adapter, which today is most non-trivial
predicates. The one working narrowing analysis, `scanPrefilter.MaxCols`
(`scan_prefilter.go`, consumed at `operators_storage.go:2100`), is computed only
for expressions `planScanPrefilter` proves safe to evaluate twice, and is
disarmed entirely when a post-decode rewrite is live (`:1578-1610`).

### 8.3 Hash keys and comparison are irreducibly Datum-based

- `compareDatum(a, b Datum, pos int)` — `expr.go:4487`. Siblings:
  `compareDatumWithNullsFirst` (`operators_join_agg.go:3318`),
  `compareSortDatums` (`operators_window.go:1203`), `compareMergeKeys`
  (`join_merge_key.go:87`), `compareEq` (`expr.go:11364`). It performs
  **cross-kind coercion** — int vs numeric vs float, enum by sort order,
  timestamp/date subtypes.
- `datumKey(d Datum) string` — `operators_join_agg.go:4240-4289`, the hash-key
  builder. `KindInt` maps to `canonicalNumericKey(d.Int, 0)`, *deliberately
  identical* to a scale-0 numeric so `aid = $1` matches whether `$1` arrives as
  int or numeric; `canonicalNumericKey` (`:4300`) strips trailing zero pairs so
  `1`, `1.0` and `1.00` collapse. There is a bug post-mortem inline at
  `:4256-4264`: reading `d.Int` on the big-numeric lane returns the arena
  `offset<<32|len`, so two equal big numerics at different arena offsets produced
  different keys and a hash join silently dropped pairs.
- Composite keys (`join_composite_key.go`) have two lanes: a **packed int lane**
  (`compositeKeyWidth = 8`, `:46`; big-endian int64 per key column, looked up as
  `o.lazyHash[string(o.execKeyBuf)]` at `:302` precisely because the compiler
  special-cases that form into a no-copy map access) and a general
  length-prefixed `datumKey` lane. `demoteCompositeIntKeys` (`:245`) re-keys the
  whole table mid-build when a non-int64 arrives.
- Sort: `sortKeyVals(row)` (`operators.go:905`) precomputes keys via
  `evalSortKeyValue`; `lessKeyVals` (`:923`) and the un-precomputed twin
  `lessRows` (`:1074`). `operators.go:898-900` warns that a chunk sorted by one
  comparator and merged by another emits out-of-order rows *with no error*.

**Consequence for 04:** the packed-int composite lane is a genuine byte-level key
path and survives. Everything else deforms first. A packed tuple's raw bytes
would key `1` and `1.0` differently — the exact bug class `canonicalNumericKey`
exists to prevent — so **hashing and comparison are consumers of deformed values
and stay that way.** This is not a limitation of the design; PostgreSQL does the
same (`SortTuple.datum1` hoists one deformed key, 01 §6).

### 8.4 Aggregation is Datum-native throughout

`aggRuntime` (`operators_join_agg.go:1868-1913`) holds `value Datum`, `sum
int64`, `numericSum Datum`, `count int64`, `floatSpecial`, `distinct
map[string]struct{}`, `strAccum []byte`, `arrayElems []string`, `arrayElemKeys
[][]Datum`, and more. `evalGroupExprs` (`:2262`) evaluates each group expression
via `evalExprSlot`, calls `v.MaterializeArena()` at `:2278` (an arena retention
boundary) and builds `datumKey` parts joined by `'|'` in `setGroupKey` (`:2287`).
`applyAgg` `:2566`; `finalizeGroup` `:2310`.

Aggregation is a *consumer* of deformed values, so it does not obstruct a packed
producer — but nothing in it can run on bytes. Only its stored group
representative (`groupValues`, `passthroughVals`) is retention, and that is §3's
row.

---

## 9. Type metadata — the weak seam

This is where 04's feasibility is decided, so it is stated precisely.

`internal/optimizer/plan.go:25,38-42`:

```go
type Schema []SchemaColumn

type SchemaColumn struct {
	Name           string
	Type           catalog.Type
	SourceTableIdx int16
}
```

`internal/catalog/catalog.go:188-198`:

```go
type Type struct {
	Name    string
	Args    []int64   // typmod args
	IsArray bool
}
```

**The type is a string.** Every width and alignment decision in the executor is a
`strings.ToLower(t.Name)` followed by a switch on string literals. What exists,
against PostgreSQL's `TupleDesc`:

| PG concept | goopg equivalent | location |
|---|---|---|
| `attalign` | `catalog.PhysicalTypeAlign(t, tname) int` → 1/2/4/8 | `internal/catalog/physical_align.go:18`, wrapper `:70`; executor `codec.go:1464` |
| `attlen == -1` | `pgPhysicalTypeIsVarlena(t) bool` | `codec.go:1476-1502` |
| `attlen > 0` (the width) | **no function** — appears only as comments on the varlena switch arms (`"interval", // typlen 16, not varlena`) | — |
| `attbyval` | **absent** | — |
| `HEAP_HASVARWIDTH` | `pgRowHasVarWidth(cols, row)` | `codec.go:1511` |

`pgPhysicalTypeIsVarlena`'s comment (`:1468-1475`) is explicit that it *is* the
`attlen == -1` predicate, must agree with `encodeValuePG`, and that PG18's
`nocachegetattr` fast-path walker (`heaptuple.c:642`, `Assert(j > attnum)`) trips
if they disagree.

**The complete descriptor table already exists, and is already reachable by
name.** `userTypeAttrs{TypLen int16; TypByVal bool; TypAlign byte; TypStorage
byte; TypCollation uint32}` and `userTypeAttrsForOID(oid uint32)` —
`internal/executor/pg18_user_catalog_rows.go:118-127`, `:135-377`: **104 `case`
arms over ~102 OIDs** transcribed from
`postgres/src/include/catalog/pg_type.dat`, with an unknown-OID fallback to a
varlena-text descriptor (`:376`).

An earlier draft of this document said *"nothing on the execution path consults
it"*. **That is wrong.** `columnTypeStorageCode`
(`internal/executor/operators_ddl.go:1883`) ends
`return userTypeAttrsForOID(typOID).TypStorage` (`:1915`), deriving a
freshly-declared column's `attstorage` at CREATE TABLE — and it reaches the
table **by name**, through `catalog.TypeNameToOID`
(`internal/catalog/codec.go:1715`).

So a name→OID→descriptor bridge exists and is in use inside the executor
package. 03 §5's TD-1 is correspondingly cheaper and safer: **reuse the bridge,
do not transcribe `pg_type.dat` a second time.**

**Two conclusions 04 must build on:**

1. Fixed width per column is **not** derivable from `optimizer.Schema` today. The
   alignment half is centralised and reusable (`physical_align.go`); the length
   and by-value halves exist only as `pg_type.dat`-transcribed comments plus the
   display-only OID table.
2. Scans hold `[]catalog.Column` (`o.cols`); **intermediate operators — join,
   aggregate, sort output — carry only `Schema`**. A packed format for scan
   output is close to reach. A packed format for *intermediate* rows, which is
   where §3's retention actually lives, needs a type descriptor goopg does not
   carry. `colTypeInfo` (`coltypeinfo.go`) is the natural home: it already has the
   lifecycle, the resolution site and the DDL-staleness rule.

---

## 10. Parallelism has no serialisation boundary

`operators_gather.go`: `rowBatch{ rows []Row; worker int }` (`:42-45`),
`gatherBatchRows = 256` (`:34`), `gatherChanDepth = 2` (`:38`), `ch chan
rowBatch`. Channel capacity *is* the flow control (`:35-37`): *"PG needs an
explicit credit scheme because shm_mq is a fixed pre-allocated 64 KiB per worker;
a Go channel of slices needs none."*

Rows crossing the channel are fully materialised and must be arena-independent
(`:40-41`; ownership contract in `parallel_runtime.go`). Each worker builds its
own operator tree over the shared read-only plan with its own `mmgr.Context`.

So Gather is a **retention-and-transfer** boundary that currently pays `48 ×
columns` per row across the channel, with no encode step to replace. It is a
natural beneficiary and carries no compatibility constraint — but it is also
where a Datum-safety bug becomes a wrong answer in a worker
(`parallel_substrate_test.go:26-80` is the existing guard).

Shared state that must keep working: `pscan *parallelScanState` (without it every
worker tree scans the whole relation and Gather returns N copies of every row,
`:92-97`), `pbm`, `pidx`, `ctx.SharedHashBuilds`. The attach helpers are the
recursive type switches at `parallel_scan.go:115/165/250` (§8.1).

---

## 11. Summary — reuse and gaps

**Reusable as-is** (no new format needs writing):

1. `TupleSlot`/`SlotView` — a real slot abstraction with `Row()` as the single
   materialisation choke point.
2. `VirtualSlot` — proof a non-`[]Datum` slot flows through the pipeline today.
3. `EncodeRowPG` + `NullBitmapPG` + `storage.NewHeapTupleWithNulls` — a
   PG-faithful packer.
4. `DecodeRowRangeIntoMctxPGTupleStyled` — a resumable partial deform citing
   `slot_getsomeattrs`.
5. `seqScanOp`'s two-phase deform — a working end-to-end demonstration on Q6.
6. `colTypeInfo` — the `TupleDesc`-shaped memoization slot with its DDL contract
   already stated.
7. `spill.go`'s `WriteRowHashed` framing — already `ExecHashJoinSaveTuple`'s.
8. `pglz` + the varlena header emitters/readers.

**Gaps** (03 and 04 own these):

1. **No `MinimalTuple` equivalent** — no 15-byte header, no `MINIMAL_TUPLE_OFFSET`
   constants. (Go cannot do negative-offset pointer arithmetic safely, so the
   "point 8 bytes before" trick needs a `hoff`-relative accessor instead.)
2. **Nominal-only alignment on both encode and decode** (03 §3) — the single
   largest byte-format divergence.
3. **No `attcacheoff` fast path** — `decodeRowRangeInfo` recomputes alignment for
   every column of every row.
4. **No owner for the `(nvalid, off)` watermark** across operator calls (§5.3).
5. **No type descriptor on intermediate rows** (§9) — the feasibility crux.
6. **TOAST pointers are goopg-private** (03 §4).
7. **A second retention encoding** in `spill.go` (§6).
8. Six type-switch sites that fail silently on an unknown slot kind (§8.1).

(End of file)
