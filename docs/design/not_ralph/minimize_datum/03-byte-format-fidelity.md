# 03 — Byte-format fidelity: how close is goopg to `heap_fill_tuple`?

**Purpose.** This bundle has two axes. 04 is the memory axis (stop retaining
`[]Datum`). This document is the **byte axis**: make the packed bytes
PostgreSQL's bytes. They are separable — §7 says exactly how — and conflating
them is the fastest way to make the memory work hostage to a compatibility
project.

Diff base: 01 (PostgreSQL) against 02 §5 (goopg's existing codec). Verified in
the tree 2026-09-03.

---

## 1. Verdict first

goopg's on-disk row is a **real PostgreSQL heap tuple**, and has been the only
on-disk format since M0111-0002. The header is exact. The null bitmap is exact.
The varlena short/long headers are exact and byte-pinned by a test. PGLZ is
upstream-validated.

There are **five** divergences. One is structural and affects every tuple; the
others are bounded.

| # | divergence | severity | affects the memory work (04)? |
|---|---|---|---|
| D1 | **Nominal-only alignment on both encode and decode** — no `att_align_datum` / `att_align_pointer` | structural, every short varlena in every tuple | **no** (§7) |
| D2 | TOAST pointers are a goopg-private 12-byte big-endian struct, not `varatt_external` | bounded to TOASTed columns | no |
| D3 | No `attlen == -2` (cstring) arm | none in practice — goopg has no cstring column type | no |
| D4 | No expanded-object (`VARATT_IS_EXTERNAL_EXPANDED`) arm | none in practice — goopg has no expanded-datum concept | no |
| D5 | No `attcacheoff` fast path; alignment recomputed per column per row | performance, not correctness | **yes** — 04 §6 |

Everything in 01 §8's checklist except items 6 and 8 is already satisfied.

---

## 2. What is already byte-exact

### 2.1 The header

`internal/storage/heap.go:372-382` mirrors `HeapTupleHeaderData` field for field.
`MarshalBinary` (`:472-509`) writes:

```
[0:4] Xmin  [4:8] Xmax  [8:12] Xvac  [12:16] CTID.Block  [16:18] CTID.Offset
[18:20] Infomask2  [20:22] Infomask  [22] hoff
[23 : 23+len(bitmap)] bitmap        [hoff:] data
```

all little-endian — including `t_infomask2` **before** `t_infomask`, which is the
ordering most reimplementations get wrong. `SizeOfHeapTupleHeaderData = 23`
(`:14`) and `DefaultHeapTupleHoff = 24` (`:18`) match 01 §1 exactly and are
pinned by `heap_test.go:754,758`.

`HeapHotUpdated`/`HeapOnlyTuple` are in `t_infomask2`, with a comment
(`storage/heap.go:150-172`) recording the 2026-08-11 bug in which they lived in
`t_infomask` and made HOT chains mutually unreadable between the two engines.
That bug is the best evidence available that this format is genuinely
cross-checked against PostgreSQL rather than merely intended to be.

### 2.2 The null bitmap

`NullBitmapPG` (`codec.go:64-82`): returns nil when the row has no NULL; else
`bmLen = (len(row)+7)/8` and `bm[i/8] |= 1 << (i%8)` for every **non-null**
column. Bit set = NOT NULL, little-endian within the byte. That is
`fill_val`'s convention (01 §3) and `BITMAPLEN` (01 §1).

`encodeRowPGCtx` (`codec.go:93-95`) `continue`s on a null column, consuming zero
data bytes — the other half of the same rule.

`NewHeapTupleWithNulls` (`storage/heap.go:445-467`) stamps `HeapHasNull` and
computes `hoff = maxAlign8(23 + len(bitmap))` = `MAXALIGN(SizeofHeapTupleHeader +
BITMAPLEN)`.

Decode: `decodeRowRangeInfo` (`codec.go:1368-1371`) — *"bit i = 0 means column i
is NULL"*. Correct.

### 2.3 Varlena headers

`varlenaBytes` / `varlenaTextBytes` (`codec.go:1084-1128`): `total = len + 1`; if
`total <= 127` emit `byte(total<<1)|1` then the payload; else a 4-byte LE
`uint32(total)<<2`. That is `SET_VARSIZE_SHORT` and `SET_VARSIZE`.

Pinned byte-for-byte by `canonical_tuple_bytes_test.go:39-60`: `'bootstrap'`
encodes to `0x15` followed by 9 bytes, in a tuple with `hoff = 24`.
(`0x15 = (10<<1)|1`, `total = 10 = 9 + 1`.) 

Read side, `decodePhysicalPGVarlena` (`codec.go:1999-2027`), handles all four
shapes:

| first byte test | interpretation |
|---|---|
| `== 0x01` | external — **currently rejected** (D2) |
| `& 0x01 == 0x01` | short: `total = header >> 1`, payload `data[1:total]` |
| `& 0x03 == 0x02` | `VARATT_IS_4B_C`, PGLZ → `pglz.DecodeInlineCompressed` |
| else | 4-byte uncompressed: `total = LE32 >> 2`, payload `data[4:total]` |

PGLZ itself (`internal/access/common/pglz/pglz.go`) is validated against upstream
in `upstream_golden_test.go`.

### 2.4 `natts` and fast defaults

`SetNatts` (`storage/heap.go:399`) writes into `t_infomask2` under
`HeapNattsMask`. `decodeRowRangeInfo` (`codec.go:1360-1367`) returns
`c.MissingValue` for `i >= storedNatts` — PostgreSQL's `attmissingval` rule
(01 §4), so `ALTER TABLE ADD COLUMN … DEFAULT <const>` avoids a rewrite in both
engines and the tuples stay mutually readable.

---

## 3. D1 — the alignment divergence

**This is the one that matters.** Both directions are affected.

**Encode.** `encodeRowPGCtx` (`codec.go:118-119`):

```go
align := physicalPGTypeAlign(c.Type)
off = alignPhysicalPGOffset(off, align)
```

Unconditional. `physicalPGTypeAlign` returns 4 for `text` (the `default` arm of
`catalog.PhysicalTypeAlign`, `internal/catalog/physical_align.go:65`). So a
short-header `text` value is preceded by up to 3 pad bytes.

PostgreSQL's `fill_val` (01 §3) takes the `att->attispackable &&
VARATT_CAN_MAKE_SHORT(val)` arm and emits **no alignment at all**. Three of its
five varlena arms skip alignment; goopg's single arm never does.

**Decode.** `decodeRowRangeInfo` (`codec.go:1383`) — the same unconditional
`alignPhysicalPGOffset(off, align)`. PostgreSQL uses the *peeking*
`att_pointer_alignby` (01 §3.1): a non-zero byte at the cursor means no
alignment adjustment is needed — because it is either a 1-byte length word or
the first byte of an *already correctly aligned* 4-byte one. **The peek decides
only whether to align, never which header form it is** (01 §3.1 quotes PG's
comment in full; getting this backwards produces a corrupting decoder).

### 3.1 The two consequences, which are not symmetric

**(a) goopg's tuples are still readable by PostgreSQL.** The pad bytes are zero
(`encodeRowPGCtx` grows `out` with `append(out, 0)`), and PostgreSQL's peek rule
aligns on a zero byte — which is exactly right whether that zero is a pad byte or
the first byte of a 4-byte length word (01 §3.1). So the standby scenario works
today, and D1 has never produced a wrong answer.

**(b) goopg cannot read a PostgreSQL-authored tuple** in which a short varlena
sits at an unaligned offset. Its decoder would skip forward onto the payload.

So D1 is a **byte-identity** gap and a **read-PG's-bytes** gap. It is not a
correctness gap for a self-consistent format, which is why it has survived.

### 3.2 goopg already has the fix, in one place

`internal/catalog/codec.go:1693-1695`, inside `decodeTextArray`:

```go
if off < len(blob) && blob[off] == 0 {
	off = (off + 3) &^ 3
}
```

with a comment explaining the bug it closed. This is `att_align_pointer` for the
4-byte case. It is the only implementation of PostgreSQL's conditional alignment
in the tree, and it is on the catalog path, not the executor path.

### 3.3 Cost of closing D1

Two functions and their two call sites:

- **encode**: `att_align_datum` — skip the align when the value about to be
  written will carry a short header. `encodeValuePGCtx` already decides short vs
  long inside `varlenaBytes`; the decision has to be hoisted so
  `encodeRowPGCtx` can see it before aligning. Requires `attstorage` (the
  packability gate, `attstorage != 'p'`) — §5.
- **decode**: `att_align_pointer` — generalise `catalog/codec.go:1693` for all
  four alignments and apply it in `decodeRowRangeInfo` when
  `pgPhysicalTypeIsVarlena(c.Type)`.

**This is a format change and every existing on-disk tuple was written the old
way.** See §7.2.

---

## 4. D2 — TOAST pointers

goopg (`internal/executor/toast.go:308-321`, embedded at `codec.go:97-112`):
`0x01` then **12 bytes big-endian** — `toast_oid(4) | total_len(4) |
num_chunks(4)`, with the compressed flag in the high bit of the third word.
Total **13 bytes**.

PostgreSQL (`postgres/src/include/varatt.h:32-39`): `va_header = 0x01`, then a
`va_tag` byte, then `varatt_external{ va_rawsize int32; va_extinfo uint32;
va_valueid Oid; va_toastrelid Oid }` — a **16-byte** struct. `VARHDRSZ_EXTERNAL =
offsetof(varattrib_1b_e, va_data)` = 2 (`varatt.h:253`), so
`VARSIZE_EXTERNAL = 2 + 16` = **18 bytes** total (`varatt.h:285`). The tag value
`VARTAG_ONDISK` is *also* 18, which PG's own comment explains as a fossil
(`varatt.h:56-59`: *"a previous notion that the tag field was the pointer datum's
length"*) — do not read the tag value as a struct size.

Two constraints a decoder must honour (`varatt.h:27-30`): the struct is stored
**unaligned** inside the tuple and must be `memcpy`'d into a local before its
fields are read, and it is written in **native** byte order.

The leading `0x01` matches (`VARATT_IS_1B_E`), and goopg's comment at
`codec.go:101-105` shows the marker was chosen deliberately to avoid the `0x1B`
collision with a 12-character string. Everything after it differs: no tag byte, a
different struct, the wrong endianness.

**This is the one place a goopg tuple is not PostgreSQL-readable.** Its
counterpart `decodePhysicalPGVarlena` returns `"external varlena not supported"`
for PostgreSQL's form.

Closing it means changing the on-disk TOAST pointer *and* the chunk table
layout it references (`va_valueid`/`va_toastrelid` name a TOAST relation and a
value OID; goopg's `toast_oid`/`num_chunks` name a different scheme). That is a
storage project of its own, and it is **out of scope for this bundle** — recorded
here so the byte-fidelity claim is not overstated. 06 §5's golden test must
exclude TOASTed columns and say so.

---

## 5. The type descriptor D1 needs, and 04 needs anyway

01 §3's `fill_val` is parameterised on `attlen`, `attbyval`, `attalign` and
`attstorage` (via `attispackable`). goopg has one of the four as a function
(02 §9):

| | goopg |
|---|---|
| `attalign` | `catalog.PhysicalTypeAlign` — **complete**, `internal/catalog/physical_align.go:18-66` |
| `attlen == -1` | `pgPhysicalTypeIsVarlena` — **a predicate, not the value**, `codec.go:1476` |
| `attlen > 0` | **absent** — exists only as comments on the varlena switch arms |
| `attbyval` | **absent** |
| `attstorage` | **present** — `columnTypeStorageCode` (`operators_ddl.go:1883-1915`) resolves it by name via `catalog.TypeNameToOID` → `userTypeAttrsForOID` |

The complete table already exists: `userTypeAttrs{TypLen, TypByVal, TypAlign,
TypStorage, TypCollation}` and `userTypeAttrsForOID(oid uint32)` —
`internal/executor/pg18_user_catalog_rows.go:118-127`, `:135-377`, 104 arms
transcribed from `pg_type.dat`. An earlier draft called it unreachable from the
execution path; that was wrong (02 §9): `columnTypeStorageCode`
(`operators_ddl.go:1883-1915`) already consults it **by name**, through
`catalog.TypeNameToOID` (`internal/catalog/codec.go:1715`).

**Decision (TD-1), revised.** Promote a full descriptor onto `colTypeInfo`
(`internal/executor/coltypeinfo.go:26-36`), which already holds `{lower, align,
isTSTZ}`, is built by `resolveColTypeInfo` at operator `Open`, and carries the
DDL-staleness contract *"never cached against a table across DDL"* (`:12-25`).
Add `len int16`, `byVal bool`, `storage byte`, and **populate them through the
existing `TypeNameToOID` → `userTypeAttrsForOID` bridge.**

**A second transcription is the thing to avoid, and the earlier draft's framing
invited one.** One table, two lookups. Drift is `pgPhysicalTypeIsVarlena`'s
documented failure mode (`codec.go:1468-1475`: PostgreSQL's `nocachegetattr`
`Assert(j > attnum)` trips when the varlena predicate disagrees with the
encoder) — and note `catalog.PhysicalTypeAlign` is *already* a by-name
transcription of the same upstream data, so TD-1 should reconcile it rather than
add a third.

TD-1 is a **precondition for D1 and for 04's intermediate-row packing alike**
(02 §9), which is why it is item one in the TODO.

---

## 6. Mapping goopg's `Datum` kinds onto PostgreSQL types

A packed tuple is typed by its descriptor, not by the Datum. Round-tripping is
therefore lossless only where `catalog.Type → PG type` is total and the value
survives `encodeValuePG` / `decodePhysicalPGValue`. The audit:

| `DatumKind` | packs as | round-trip risk |
|---|---|---|
| `KindNull` | null bitmap, zero bytes | none |
| `KindBool` | `bool`, 1 byte | none |
| `KindInt` | the column's integer type | none, **given the descriptor** — the same `Int` slot backs int2/4/8, so the width comes from the type, not the value |
| `KindString` | text/varchar/bpchar/name varlena | none |
| `KindBytes` | bytea varlena | none |
| `KindTime` | timestamp / date / timestamptz / time / timetz | **`TimeSub` must come from the column type.** 02 §1 records the production failure when a discriminator was dropped |
| `KindInterval` | `interval`, 16 bytes | `Hi` (sub-day micros) must be carried; `spill.go` reconstructs it from `IntervalMicrosValue()` rather than copying `Hi` |
| `KindNumeric` | `numeric` varlena | two lanes (`flagBigNumeric`); `spill.go` deliberately **does not** serialise `Flags` and re-derives the lane from the mantissa (`spill.go:~370`) |
| `KindEnum` | the enum's underlying storage | carries a sort order **and** a label; `spill.go` writes both |
| `KindToastPointer` | external varlena | D2 |

**One divergence in the encoder makes this table conditional.**
`encodeValuePGCtx` (`codec.go:440`) dispatches on the lowercased *type name*,
about 60 arms, each with a `Kind` check and an error default. Its **outer**
default (`codec.go:1039-1046`) does not error — it packs as text via
`varlenaTextBytes`, and the decoder's matching default (`codec.go:1981-1993`)
returns a `KindString` Datum, with a comment saying the symmetry is deliberate.
So a value whose type name is outside the 60 arms **round-trips as a different
`DatumKind`, silently**. The table above holds only for types with a named arm;
04 §3.1 makes that an explicit allow-list rather than an assumption.

The three items that are not mechanical — `TimeSub`, the numeric lane, and enum
— are **already solved in `spill.go`** (02 §6), and solved with the right
discipline: `TestSpillDatumRoundTripCoversEveryKind` walks `0..datumKindCount`
and `TestSpillDatumRoundTripCoversEveryTimeSubtype` walks `0..timeSubtypeCount`,
so adding a kind without a codec arm fails a test rather than degrading
silently. `decodeDatum`'s `default` returns an error and an out-of-range
`TimeSubtype` is **rejected, not clamped**, with an in-code justification.

**Decision (TD-2).** The exhaustiveness pattern moves to the PG-format codec
along with the format. `EncodeRowPG`/`decodeRowRangeInfo` today have **no**
kind-exhaustiveness test; `spill.go` is "the one place in the repo where the
Datum kind space is enforced as a closed set". If the PG format replaces the
spill format (§7.3) without carrying that guard across, the migration deletes
the only thing standing between this codebase and the Q72-class bug it already
paid for once.

---

## 7. The target format, and the separability that makes this tractable

### 7.1 The in-memory header

**Decision (TD-3): a MinimalTuple-shaped header, not `HeapTupleHeaderData`.**

A retained executor tuple needs `natts`, the null-bitmap flag, `t_hoff` and a
length. It does not need xmin/xmax/cid or `t_ctid` — that is precisely
PostgreSQL's argument for `MinimalTupleData` (01 §2), and it saves 8 bytes per
retained row against the 23-byte heap header.

Layout: 01 §2's, unchanged — `t_len(4) | mt_padding(6) | t_infomask2(2) |
t_infomask(2) | t_hoff(1) | t_bits[] | pad | data`, with
`SizeofMinimalTupleHeader = 15`.

Keep the 6 pad bytes even though Go does not need them, so `t_hoff` and every
downstream offset are identical to PostgreSQL's and 06 §5's golden test can
compare bytes rather than fields.

**Do not port the negative-offset trick.** PostgreSQL points `t_data` 8 bytes
*before* the minimal tuple so heap routines work unchanged (01 §2). Go cannot do
that safely. Use a `hoff`-relative accessor instead: goopg's decoder already
takes `(data, bitmap, storedNatts)` as separate arguments
(`DecodeRowRangeIntoMctxPGTupleStyled`, `codec.go:1331`), so it never needed the
trick.

**Hash-value prefix.** For the hash join, store a 4-byte hash immediately ahead
of the tuple, as `HashJoinTupleData` does (01 §6) — so a probe rejects on the
hash without touching the tuple. `spill.go:104 WriteRowHashed` already uses
exactly this framing and cites `nodeHashjoin.c:1414` (02 §6); the in-memory side
adopts the framing its own spill path already has.

### 7.2 D1 is separable from 04, and must be separated

The memory work (04) needs *a* packed format. The byte work (this document)
needs *PostgreSQL's* packed format. They meet only in the encoder.

**Decision (TD-4): land 04 on today's nominal-alignment encoder, then close D1
as its own change.** Three reasons:

1. **04's format is in-memory and self-consistent.** Nothing outside the process
   reads a hash-table entry. D1 costs 04 nothing but a few pad bytes per short
   varlena — measurable, and 06 §4 measures it, but not a blocker.
2. **D1 changes the on-disk format**, because `EncodeRowPG` is shared with the
   heap writer. Every existing tuple was written nominal-aligned. Under the
   peeking decoder an old tuple still reads correctly (that is the whole point of
   the peek rule — see 01 §3.1: a zero byte is safe to align on), so the change
   is backward-compatible in the read direction. But that argument needs to be
   *tested*, not asserted, and it should not be tested inside a commit whose
   subject is hash-join memory.
3. **One variable per commit** (the standing rule). A commit that changes both
   the retention format and the byte layout cannot attribute a values-diff.

If D1 lands **first** instead, 04 inherits it for free and the goldens are
written once. That ordering is also acceptable and is the TODO's default,
because D1 is small (§3.3) once TD-1 exists. What is not acceptable is one
commit.

### 7.3 Converging the two encodings

goopg will have three encodings mid-migration: the PG on-disk format, the
`spill.go` private TLV, and 04's in-memory packed rows. The end state is **two**:
the PG format for both storage and retention, and none private.

`spill.go`'s framing survives unchanged (§7.1); its payload encoder/decoder pair
swaps. `estimatedRowBytes` (`spill.go:541`) and `hashsize.DatumBytes` (`= 48`)
must move together — `hashsize.go:~45` already says the two must not drift.

**Decision (TD-5): the spill payload converts last, after every in-memory
retention site.** It is the one path with a persistence format, an exhaustiveness
test suite and three recorded production bugs; it has nothing to gain from going
first, and going last means its tests can be pointed at the new codec as the
migration's final gate.

---

## 8. What this document commits to

| id | decision |
|---|---|
| TD-1 | Promote `attlen`/`attbyval`/`attstorage` onto `colTypeInfo`, sharing one `pg_type.dat` transcription with `userTypeAttrsForOID`. Precondition for D1 and for 04. |
| TD-2 | Carry `spill.go`'s kind- and subtype-exhaustiveness tests onto the PG-format codec before anything depends on it. |
| TD-3 | In-memory header is `MinimalTupleData`-shaped (15 bytes, 6 pad bytes kept), `hoff`-relative accessors, no negative-offset trick; 4-byte hash prefix for the hash join. |
| TD-4 | D1 (conditional alignment) is its own commit, before or after 04's first slice, never inside it. |
| TD-5 | `spill.go`'s payload converts last. |
| — | D2 (PG-format TOAST pointers) is **out of scope**; 06 §5's golden test excludes TOASTed columns and says why. D3/D4 are non-issues in goopg's type system. |

(End of file)
