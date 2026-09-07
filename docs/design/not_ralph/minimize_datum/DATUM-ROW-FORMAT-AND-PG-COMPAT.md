# goopg Datum / row formats and on-disk PG 18.3 compatibility

Survey as of `650a87542` (2026-09-07). **Code inspection only** — nothing here
was re-measured on a live cluster. Written in response to a question about how
Datum and row data are represented in memory versus on disk, how the
conversions happen, and whether the persisted files are compatible with
PostgreSQL 18.3 in both directions.

---

## 0. Conclusions first

1. **On disk, goopg writes PG 18.3's heap format.** There is no longer a
   goopg-private row format: `EncodeRow` does not exist as a function. Only
   `codec.go`'s comment still tells readers to prefer it — that comment is
   **stale**.
2. **Both directions are pinned by E2E tests**: real PG starting on a
   goopg-authored data directory, and goopg starting on a PG-authored one
   (cold and crash variants, four tests).
3. **One real incompatibility remains** for `'d'`-aligned varlena types
   (`polygon`, the range types): goopg's alignment table returns 4 where PG
   says 8. Known and ledgered, not fixed. See §6.3.

---

## 1. In-memory — `Datum`

`internal/executor/datum.go`. **Exactly 48 bytes**, enforced at compile time:

```go
const _ uintptr = 48 - unsafe.Sizeof(Datum{})   // datum.go:187
```

| field | width @ offset | purpose |
|---|---|---|
| `Kind` | 1B @0 | discriminates the value carrier |
| `Flags` | 1B @1 | `flagBigNumeric`, rest reserved |
| `ArenaID` | 2B @2 | mctx arena id (0 = no arena payload) |
| `Scale` | 2B @4 | numeric digits after the point |
| `TimeSub` | 1B @6 | which SQL type a `KindTime` datum is |
| `Int` | 8B @8 | integers, bit patterns, arena `(offset<<32\|len)` |
| `Buf` | 24B @16 | inline payload; **aliased on copy, treat read-only** |
| `Hi` | 8B @40 | interval sub-day micros / UUID high half |

`DatumKind` has nine members (`KindNull` … `KindEnum`) plus a sentinel,
`datumKindCount`. The sentinel is not decoration: it exists so a test can
assert the **spill codec has an arm for every declared kind** rather than for
the ones someone happened to think of. `KindEnum` and `KindToastPointer` had no
arm at all, so a query that had to spill an enum column simply failed.

### 1.1 Where the carrier does not equal the SQL type

This is the main source of conversion work.

- **`KindTime` carries five SQL types** (timestamp, date, timestamptz, time,
  timetz). This used to be one bit on `Flags`, which made the distinction look
  like an adornment a serializer could forget — and `encodeDatum` did forget
  it, so **every DATE that passed through a hash-join spill came back a bare
  timestamp**. TPC-DS Q72's `d3.d_date > d1.d_date + 5` failed while the same
  query at `work_mem='2GB'` answered correctly. It survived because a spilled
  date still *compares* correctly; only the type is lost, so a value-checking
  round-trip test stayed green. It is now a structural field, `TimeSub`.
- **`KindNumeric` is an int64 mantissa plus `Scale`**, which is a completely
  different representation from the on-disk PG numeric (§4.1).

### 1.2 Arenas

When `ArenaID != 0` the payload lives in an mctx arena and `Buf` is nil.
`StringValue()` returns a zero-copy `unsafe.String` view, so **a reference must
not outlive the arena's next `Reset()`**. The GC benefit is that `Buf` being
nil leaves no pointer to trace.

---

## 2. Rows and slots

- `Row` is `[]Datum`.
- `TupleSlot` / `SlotView` are the slot abstractions.
- `PackedSlot` (`packedslot.go`) is an on-demand deform slot modelled on PG's
  `tts_nvalid` + `HeapTupleTableSlot.off`. **It currently has no producer** —
  see §5.2.

---

## 3. On-disk format

### 3.1 Pages

- `BlockSize = 8192` (`storage/page.go:15`)
- `SizeOfPageHeaderData = 24`
- `pd_lsn` is read and written in PG 18's two-`uint32` `PageXLogRecPtr` layout
- Checksums via `PageChecksum(page, blkno)` (`storage/checksum.go`)

### 3.2 Heap tuples

`internal/storage/heap.go`, transcribed from PG's `htup_details.h`:

| constant | value | PG counterpart |
|---|---|---|
| `SizeOfHeapTupleHeaderData` | 23 | `HeapTupleHeaderData` fixed part |
| `DefaultHeapTupleHoff` | 24 | MAXALIGN(23) |
| `MaxHeapTuplesPerPage` | 291 | same-named macro |
| `MaxHeapTupleSize` | 8160 | same-named macro |
| `HeapDefaultFillfactor` | 100 | `HEAP_DEFAULT_FILLFACTOR` |

`t_infomask` / `t_infomask2` bit assignments follow PG, and the code
explicitly documents that `HeapHotUpdated` and `HeapKeysUpdated` live in
**`t_infomask2`** — that pair has been mixed up before.

The null bitmap follows `heap_fill_tuple`: **bit i set means column i is NOT
NULL**, little-endian within each byte, stored *between* the fixed header and
the `t_hoff`-aligned data area rather than inline with the data.

### 3.3 Column data

One funnel: `EncodeRowPGCtx` (`codec.go:89`). It takes a `Context` because a
`reg*[]` column resolves its element names through the session catalog.

---

## 4. Conversions (representation differs between memory and disk)

### 4.1 numeric

- **memory**: `KindNumeric` = int64 mantissa + `Scale` (big values spill to mctx)
- **disk**: PG's **base-10000 `NumericData` varlena**
  (`nodes.NumericBodyFromText` → `varlenaBytes`)

This arm used to store a **decimal string**. Because `pg_type` says numeric is
a varlena, the descriptor never caught it — but any reader that trusts the
*type* (a PG 18.3 standby, `pg_amcheck`'s heap tier, a logical-replication
subscriber) hands the payload to `numeric_out` as a `NumericData` and reads the
first two ASCII characters as `n_header`. It also **mis-ordered** PG-format
index tuples.

### 4.2 Others

| type | memory | disk |
|---|---|---|
| `interval` | `Int` = months<<32\|days, `Hi` = micros | 16-byte struct, typalign `'d'` |
| `uuid` | 16 bytes (`Int` + `Hi`) | typlen 16, **typalign `'c'` (1)** |
| TOAST | `KindToastPointer` (12-byte pointer) | external reference, threshold ≈2 KB |
| compression | — | PGLZ (`internal/access/common/pglz`) |

---

## 5. Does the scan decode only the columns that are needed?

**Yes — but there are two mechanisms and only one of them is live.**

### 5.1 EX1-01 / EX1-02 — the deform bound (LIVE)

`EX1-01/02/02b` are `[x]` in TODO_ALL and marked "Confirmed" in the dependency
table. Two stages:

**(a) Prefilter** (`scan_prefilter.go`, call site `operators_storage.go:2085`)

Applies the predicate of the `Filter` directly above the scan *before* the row
is fully deformed: `MaxCols` is the exclusive upper bound on the column indexes
the predicate reads, so `[0, MaxCols)` is deformed, the predicate is evaluated,
and only survivors pay for the rest. The file states the motivation:

> goopg previously deformed all 16 lineitem columns and deep-copied every one
> of the 6 M rows TPC-H Q6 scans, to hand 98 % of them to a filterOp that threw
> them away.

`prefilterSafeExpr` is a deliberate **whitelist**; an expression node it does
not name disables the prefilter entirely, so a walker with a missing arm costs
performance rather than correctness.

**(b) Deform bound** (`scan_deform.go`, `operators_storage.go:1041`)

A Build-time consumer walk proves no consumer reads past `deformBound`, and the
survivor path deforms only `[0, deformBound)`. `deformBoundBelow` folds the
referenced columns for `Filter`, `Sort`, `Limit`, `LockRows`, `Result` and
`Project`. Zero means unset and falls back to full width — the safe default for
scans built outside `buildNode`/`buildRec`, e.g. COPY's.

**Constraint, shared with PG**: a physical tuple has no column offset array and
goopg has no `attcacheoff`, so **a suffix may be skipped but a prefix may not**.
`Get(5)` deforms columns 0..5, not column 5 alone. "Only the needed columns" is
really "only up to the last needed column".

#### 5.1.1 Is the second evaluation necessary? The comment overstates it

The design comment justifies safety two ways: `filterOp` stays in place and
re-evaluates the same predicate, making the prefilter a pure pre-rejection; and
the partially deformed row never escapes the scan. Reading the implementation,
that account needs qualifying.

**(a) `filterOp` cannot be deleted — but not because of the double
evaluation.** There are two paths where the prefilter *abstains* and `filterOp`
becomes the only evaluator (`operators_storage.go:2095-2107`):

- `needsDetoastPrefix` true — a toasted value in the prefix would be judged
  un-detoasted and could differ from what `filterOp` sees, so the prefilter is
  skipped: *"finish the row and let filterOp decide alone"*.
- `perr != nil` — an evaluation error deliberately falls through, *"let
  filterOp raise it, so the error surfaces from exactly where it did before"*.

**(b) On ordinary survivors the second evaluation is redundant.** If the
prefix bound is right and the predicate is deterministic and side-effect free,
a row the prefilter admitted will be admitted by `filterOp` too.

**(c) The safety net points the wrong way.** This is the substantive point:

| prefilter error | outcome |
|---|---|
| wrongly says **true** | `filterOp` catches it → correct |
| wrongly says **false** | the row is already dropped by `continue`; **`filterOp` never sees it** → **wrong answer** |

The dangerous direction is the second one, and `filterOp` only guards the
first — which the comment itself calls harmless ("one that wrongly said 'true'
costs nothing but time"). What actually guards the wrong-answer direction is
the whitelist, the `MaxCols` bound, and `poisonDeformTail` (which panics if a
consumer reads past the deformed prefix).

**(d) There is no selectivity gate.** Waste is bounded in one respect:
`planScanPrefilter` declines when `need >= ncols`, because *"the prefilter
would then be a pure second evaluation"*. But it is called as
`planScanPrefilter(p.Predicate, len(so.cols))` and **never consults
selectivity**. For a predicate that reads few columns yet admits most rows, the
deform saving is zero (every column gets deformed anyway) and the cost is one
extra predicate evaluation per row — a **net loss**. Not measured.

**(e) This is not PG's shape, despite the comment.** `scan_prefilter.go` says
"The shape is PostgreSQL's", but PG pushes the qual **into the scan node**
(`SeqScan.qual`, evaluated by `ExecScan`); there is no separate Filter node and
the qual is evaluated **once**. goopg keeps `filterOp` as its own node and
duplicates the predicate ahead of it, which is where the second evaluation
comes from. Filed as **E-17 / EX3-08**.

### 5.2 D-03 `PackedSlot` — on-demand deform (INERT)

`packedslot.go` opens with:

> **THIS FILE HAS NO PRODUCER (TODO_ALL D-03).** Nothing in the pipeline
> constructs a PackedSlot.

The TODO_ALL row agrees: `[x] D-03 MD-03 PackedTuple + PackedSlot,
**unreachable**`. The implementation and the six required type-switch arms
exist; reachability is deliberately withheld.

The reason is **D-04's prototype verdict, "STOP, THE MODEL IS WRONG"**:

| metric | result |
|---|---|
| batches | **4 → 4, unchanged** — the stopping rule's own trigger |
| retained bytes | −14.2 % (join accounting) / −24.4 % (live heap) |
| wall time | **+6.8 %** (n=7 per arm, distributions barely overlap) |
| allocations | **+39 %** (`EncodeRowPGCtx` ≈6 allocs per packed row vs ≈1) |

Two measured reasons the model was wrong: `avgVarBytes` was ~62 % too high —
**correcting it alone takes nbatch 4 → 2 with no packing at all** — and the
model priced rows while ignoring the hash table, where peak live heap was
506 MB of buckets against 296 MB of rows. Every conversion site (D-05 onward)
is consequently blocked, and D-11 records that its "one retention format"
condition is *trivially true because none has landed*.

**Summary**: the goal — decode only what is needed — is **met by the EX1 path**.
The further PackedSlot laziness is **deliberately stopped on measurement**.

---

## 6. On-disk compatibility with PG 18.3

### 6.1 Bidirectional E2E tests (`internal/testport/`)

| test | direction |
|---|---|
| `e2e_pg_coldstart_on_goopgdata_test.go` | **real PG boots** a goopg-authored directory |
| `e2e_pg_crashstart_on_goopgdata_test.go` | same, after a crash |
| `e2e_goopg_coldstart_on_pgdata_test.go` | **goopg boots** a PG-authored directory |
| `e2e_goopg_crashstart_on_pgdata_test.go` | same, after a crash |

The reverse test's own header: upstream `initdb` builds the directory, an
upstream backend writes the catalog and heap pages, and goopg then opens it
**with no conversion step**. Its preconditions — goopg's GUC registry accepting
a PG-18 `postgresql.conf` unedited, and `LoadOrCreateSystemID` reading
`pg_control` first so goopg adopts the directory's identity — are both landed.

A whole-database `pg_amcheck` run is also clean
(`TestPort_PgAmcheckAllTables`).

### 6.2 D-09 — the only task that changes the persisted format

**D-09 (MD-1x conditional alignment) is the only TODO_ALL item that changes the
on-disk format.** Landed 2026-09-06 (`016f67b`). The change is **column-data
padding only**; headers, null bitmap and `t_hoff` are unchanged.

It ports two PG rules:

- encode — `fill_val`: a packable varlena carrying a short header skips
  alignment
- decode — `att_align_pointer`'s **one-byte peek**
  (`catalog.AttAlignPointer`: `data[off] != 0` means do not align)

**This did not break compatibility; it fixed a one-way incompatibility.** As
`TestAlignForwardPGBytes` records, the pre-D-09 unconditional-align decoder
*"skipped onto the payload"* on PG-authored packed varlena — it read past the
header.

| test | what it pins |
|---|---|
| `TestAlignForwardPGBytes` | goopg reads PG-authored **unpadded** short text |
| `TestAlignBackwardOldBytes` | new goopg reads old goopg's **padded** bytes |
| `TestAlignLivePGGolden` | a 5-column 328-byte tuple is **byte-identical to live PG 18.3** |

**Direction caveat, stated on the row**: upgrade-direction compatible,
**downgrade unsupported**. A new binary reads old files; an old binary cannot
read new ones.

### 6.3 A real incompatibility — `'d'`-aligned varlena

**This is an incompatibility, not merely a byte difference.**

Oracle (`postgres/src/include/catalog/pg_type.dat`):

- `polygon`: `typlen => '-1'`, **`typalign => 'd'`**, `typstorage => 'x'`
- `tsrange`: `typlen => '-1'`, **`typalign => 'd'`**, `typstorage => 'x'`

goopg's `PhysicalTypeAlign` (`catalog/physical_align.go`) has neither in its
switch, so both fall to the **default of 4** where PG says 8.

Whether the peek rescues it:

| case | outcome |
|---|---|
| short header (1 byte, always non-zero) | both sides choose "do not align" → **safe** |
| **PG writes, goopg reads** (long header) | PG pads to the 8-boundary. goopg sees a zero pad byte and aligns — but **to 4**. If the cursor is already a multiple of 4 it does not move and **reads padding as the header** → **breaks** |
| **goopg writes, PG reads** (long header) | a non-zero first byte means PG does not align either, so it usually reads. But when the varlena length is a multiple of 64, `(len<<2)&0xFF == 0`, PG aligns to 8 and **skips 4 bytes past the header** → **breaks** |

It is asymmetric: **goopg reading PG-authored data is the more fragile
direction** (it breaks whenever padding is present, regardless of length). The
failure mode misaligns **that column and every column after it** — the same
class of bug the file's own header records for logical-replication tuples.

**Reachability**: `polygon` has codec and DDL arms, and `rangetypes.go` defines
`int8range` / `daterange` / `tsrange` / `tstzrange`, so these **can be declared
as columns and written to disk**. This is not hypothetical.

**Scope is narrow**: only `'d'`-aligned varlena types. None appear in TPC-H,
TPC-DS, pgbench, or the PG-standby validations, which is why it has not
surfaced.

**Known and recorded**: D-09's review excluded it explicitly rather than
missing it. `TestAlignLivePGGolden`'s comment reads *"'d'-align varlena
(polygon/tsrange) excluded: the physical align TABLE defaults them to 4 vs
PG's 8"*, ledger row `take3-D-09-noted`, classified as a **different mechanism**
(a gap in the alignment table) from the conditional-alignment rule.

**Fix size**: add `polygon` and the range types to the alignment table as 8 —
far smaller than D-09 itself.

### 6.4 Other known gaps (not heap-format)

- `pg_attrdef`: a PG standby cannot use it for DEFAULT evaluation (non-nailed
  tupledesc, missing index 2656)
- views: `pg_get_viewdef` succeeds but `SELECT * FROM v` on the standby returns
  42809, because the rewriter reads the relcache's `rd_rules` rather than
  scanning `pg_rewrite` as `pg_get_viewdef` does
- `pg_stat_*` views do not exist on a goopg catalog

---

## 7. A third format — spill files

`internal/executor/spill.go` uses a **goopg-private uvarint framing**, not the
heap format: a uvarint column count followed by datums.

D-10 (converting the spill payload to PG format) is still blocked, but spill
files are transient rather than persisted heap, so they **do not bear on §6**.

Separately, `hashsize.MapSlotBytes = 48` is documented in-code as **2× low
against a measured 96.1 B/slot**.

---

## 8. Pitfalls found while reading the code

1. **`codec.go`'s "Most goopg code should continue using EncodeRow
   (goopg-internal format)" is wrong.** No such function exists; every path
   goes through `EncodeRowPG*`.
2. **`PhysicalTypeIsVarlena` is a default-true approximation.** It
   misclassifies `tid` (typlen 6), `money`, `macaddr`/`macaddr8` as varlena.
   The comment argues this is harmless: their storage is `'p'` so D-09's pack
   rule never fires, and the decode peek is offset-sound regardless.
3. **The alignment table used to be duplicated and had drifted.**
   `xlog/pgoutput.go` carried a hand-copied subset missing `pg_lsn`, `xid8`,
   `serial2`, `serial8` and `anyarray`, so a logical-replication tuple
   containing any of them was decoded one or more bytes off, **corrupting that
   column and every column after it**. Both callers now share
   `catalog.PhysicalTypeAlign`.
4. **`anyarray` is `'d'` (8)**, unlike every other varlena array at `'i'` (4).
   Its users are `pg_attribute.attmissingval` and `pg_statistic.stavalues1..5`;
   padding them at 4 puts every following byte one word early.
