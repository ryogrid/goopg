# 01 — PostgreSQL's tuple representation (the oracle)

**Source:** PostgreSQL 18.3, read-only at `./postgres/`. Every claim below
carries `src/...:line` or `:function`. Verified 2026-09-03.

This document exists so 03 and 04 can diff against something exact. It is the
byte-level half of the picture; take3 `10-executor-pg-design.md` §3 and §13
cover the same slot family at the architectural level and are not repeated here.

**The one-sentence summary:** PostgreSQL stores essentially *nothing* in bulk as
a `Datum` array. Every bulk store — hash join, tuplestore, tuplesort, Memoize,
the aggregate hash table — holds a packed `MinimalTuple`. The only `Datum`-array
residents are the live `TupleTableSlot.tts_values` (one row per slot) and
`SortTuple.datum1` (one hoisted sort key).

---

## 1. `HeapTupleHeaderData` — the on-disk header

`src/include/access/htup_details.h:153-181`. The struct is deliberately packed
so no compiler padding appears (`:51-52`).

| offset | size | field | note |
|---:|---:|---|---|
| 0 | 12 | `t_choice` | union: `HeapTupleFields{t_xmin, t_xmax, t_field3}` (`:122-132`) **or** `DatumTupleFields{datum_len_, datum_typmod, datum_typeid}` (`:134-151`) |
| 12 | 6 | `t_ctid` | `BlockIdData{bi_hi, bi_lo}` + `ip_posid` |
| 18 | 2 | `t_infomask2` | natts + flags |
| 20 | 2 | `t_infomask` | flags |
| 22 | 1 | `t_hoff` | header size including bitmap and padding |
| 23 | var | `t_bits[]` | null bitmap, present only when `HEAP_HASNULL` |

`SizeofHeapTupleHeader = offsetof(HeapTupleHeaderData, t_bits)` = **23**
(`:185`). The physical order is: fixed fields → null bitmap → alignment padding
→ user data (`:65-71`). The `t_choice` union overlay — in-memory row-type Datums
reusing the space that on-disk tuples spend on xmin/xmax — is explained at
`:54-63`.

**Storage-relevant `t_infomask` bits** (`:190-193`). Everything at or above bit 4
is visibility state (`HEAP_XACT_MASK 0xFFF0`, `:219`) and is not part of the
value encoding:

| bit | name | meaning |
|---|---|---|
| `0x0001` | `HEAP_HASNULL` | a null bitmap is present at `t_bits` |
| `0x0002` | `HEAP_HASVARWIDTH` | at least one varlena or cstring attribute |
| `0x0004` | `HEAP_HASEXTERNAL` | at least one TOAST pointer (`VARATT_IS_EXTERNAL`) |
| `0x0008` | `HEAP_HASOID_OLD` | retired (`:193`) |

**`t_infomask2`** (`:291-296`): `HEAP_NATTS_MASK 0x07FF` (11 bits — this is where
`natts` lives), `0x1800` free, `HEAP_KEYS_UPDATED 0x2000`,
`HEAP_HOT_UPDATED 0x4000`, `HEAP_ONLY_TUPLE 0x8000`. Note
`HEAP_TUPLE_HAS_MATCH` is an *alias* of `HEAP_ONLY_TUPLE`, reused during hash
joins (`:306`) — a tuple in a hash table is never simultaneously a HOT tuple.

**Null bitmap.** `BITMAPLEN(NATTS) = (NATTS + 7) / 8` (`:598-603`). The
convention is **bit set = NOT NULL** (see `fill_val`, §3). Read with
`att_isnull(attnum, bp)`.

**`t_hoff`.** `MAXALIGN(SizeofHeapTupleHeader + BITMAPLEN)`, computed in
`heap_form_tuple` (`src/backend/access/common/heaptuple.c:1151-1156`). It must be
a multiple of MAXALIGN (`htup_details.h:118-119`). With no nulls,
`t_hoff = MAXALIGN(23) = 24`. `t_hoff` being a `uint8` bounds the attribute count
at "a little over 1700"; PostgreSQL then pins the round number
`MaxTupleAttributeNumber = 1664` (`8 × 208`) deliberately below it, *"so that
alterations in `HeapTupleHeaderData` layout won't change the supported max number
of columns"* (`:24-34`).

---

## 2. `MinimalTupleData` — the in-memory executor tuple

`src/include/access/htup_details.h:681-701`; rationale at `:644-673`.

| offset | size | field |
|---:|---:|---|
| 0 | 4 | `t_len` — actual length of the minimal tuple |
| 4 | 6 | `mt_padding[MINIMAL_TUPLE_PADDING]` |
| 10 | 2 | `t_infomask2` |
| 12 | 2 | `t_infomask` |
| 14 | 1 | `t_hoff` |
| 15 | var | `t_bits[]` |

Constants (`:674-679`), with the arithmetic evaluated for a 64-bit build
(`MAXIMUM_ALIGNOF = 8`, `offsetof(HeapTupleHeaderData, t_infomask2) = 18`):

- `MINIMAL_TUPLE_OFFSET = (18 - 4) / 8 * 8` = **8**
- `MINIMAL_TUPLE_PADDING = (18 - 4) % 8` = **6**
- `MINIMAL_TUPLE_DATA_OFFSET = offsetof(MinimalTupleData, t_infomask2)` = **10**
- `SizeofMinimalTupleHeader = offsetof(MinimalTupleData, t_bits)` = **15** (`:704`)

**What it drops.** The 12-byte `t_choice` and the 6-byte `t_ctid` — 18 bytes of
transaction and location state — replaced by a 4-byte length and 6 bytes of
padding. Net saving over `SizeofHeapTupleHeader`: **8 bytes**, exactly
`MINIMAL_TUPLE_OFFSET`.

**Why the padding is 6 and not 0.** So that `offsetof(t_infomask2)` is congruent
mod `MAXIMUM_ALIGNOF` in both structs (`:657-661`). That congruence buys the
trick at `:663-666`: set `HeapTupleData.t_data` to point `MINIMAL_TUPLE_OFFSET`
bytes *before* the minimal tuple, and every heap access routine — including the
deform loop in §4 — works on it unchanged, because `t_hoff` and all downstream
alignment computations land identically.

Two easy-to-get-wrong details (`:668-669`): `t_hoff` **includes** the
`MINIMAL_TUPLE_OFFSET` distance; `t_len` **does not**.

`heap_form_minimal_tuple` — `heaptuple.c:1453-1528`. Identical to
`heap_form_tuple` except that `len` starts at `SizeofMinimalTupleHeader`, the
allocation is a bare `palloc0(len + extra)` with no `HeapTupleData` management
struct, and `tuple->t_hoff = hoff + MINIMAL_TUPLE_OFFSET` (`:1516`). The `extra`
argument is a maxaligned zeroed prefix so a caller can prepend its own header in
the same allocation — that is what the hash join uses (§6).

Siblings: `heap_free_minimal_tuple` `:1530`, `heap_copy_minimal_tuple` `:1542`,
`heap_tuple_from_minimal_tuple` `:1560`, `minimal_tuple_from_heap_tuple` `:1587`.

---

## 3. `heap_form_tuple` / `heap_fill_tuple` — Datum array → packed bytes

`heap_form_tuple` — `heaptuple.c:1116-1194`:

1. Reject `natts > MaxTupleAttributeNumber` (`:1129`).
2. Scan `isnull[]` for `hasnull` (`:1136-1144`).
3. `len = offsetof(HeapTupleHeaderData, t_bits)`; `if (hasnull) len += BITMAPLEN(natts)`; `hoff = len = MAXALIGN(len)` (`:1149-1155`).
4. `data_len = heap_compute_data_size(...)`; `len += data_len` (`:1157-1159`).
5. `palloc0(HEAPTUPLESIZE + len)` — one chunk for the `HeapTupleData` management
   struct and the tuple body. **The zeroing is load-bearing**, not hygiene:
   `heap_fill_tuple` relies on it (`:1165-1167`, and the note at `:396`).
6. Stamp `t_len`, invalid `t_self`, `InvalidOid` tableOid, the Datum fields
   (`SetDatumLength`/`SetTypeId`/`SetTypMod`, `:1174-1183`), invalidate `t_ctid`,
   `SetNatts`, `td->t_hoff = hoff`.
7. `heap_fill_tuple(desc, values, isnull, (char *) td + hoff, data_len, &td->t_infomask, hasnull ? td->t_bits : NULL)` (`:1187-1193`).

**`heap_compute_data_size`** — `:219-266`. Per non-null attribute:

- `COMPACT_ATTR_IS_PACKABLE(atti) && VARATT_CAN_MAKE_SHORT(val)` → add
  `VARATT_CONVERTED_SHORT_SIZE(val)` and **charge no alignment** (`:238-246`);
- `attlen == -1 && VARATT_IS_EXTERNAL_EXPANDED` → nominal-align, add
  `EOH_get_flat_size` (`:247-255`);
- otherwise → `att_datum_alignby` then `att_addlength_datum` (`:256-262`).

**`heap_fill_tuple`** — `:401-449`. Initialises `bitP = &bit[-1]`,
`bitmask = HIGHBIT`, clears `HEAP_HASNULL|HEAP_HASVARWIDTH|HEAP_HASEXTERNAL`
from `*infomask` (`:427`), calls `fill_val` per attribute, and asserts
`(data - start) == data_size` (`:446`).

**`fill_val`** — `:275-388`, the actual per-attribute encoder. This is the
specification 03 diffs goopg against:

- *Null bitmap.* Rotate `bitmask`, advancing `*bit` and zeroing the new byte on
  wrap (`:290-299`). If the value is null: `*infomask |= HEAP_HASNULL` and
  return, leaving the bit **clear** and consuming **zero data bytes**
  (`:301-306`). Otherwise `**bit |= *bitmask`.
- *`attbyval`* (`:312-318`): `att_nominal_alignby` the pointer,
  `store_att_byval`, advance by `attlen`.
- *`attlen == -1`, varlena* (`:319-361`) — always sets `HEAP_HASVARWIDTH`:
  - `VARATT_IS_EXTERNAL_EXPANDED` → nominal-align, `EOH_flatten_into`;
  - `VARATT_IS_EXTERNAL` (a TOAST pointer) → **also sets `HEAP_HASEXTERNAL`**,
    **no alignment** ("short by definition"), memcpy `VARSIZE_EXTERNAL(val)`;
  - `VARATT_IS_SHORT` (already a 1-byte header) → **no alignment**, memcpy
    `VARSIZE_SHORT(val)`;
  - `att->attispackable && VARATT_CAN_MAKE_SHORT(val)` → **no alignment**,
    `SET_VARSIZE_SHORT(data, len)` then copy `VARDATA(val)` at `data+1`. This is
    the 4-byte→1-byte header conversion; `attispackable` derives from
    `attstorage != 'p'`;
  - else → nominal-align, memcpy the full `VARSIZE(val)` with its 4-byte header.
- *`attlen == -2`, cstring* (`:362-369`): `HEAP_HASVARWIDTH`, never aligned,
  length `strlen + 1`.
- *fixed-length by-reference* (`:370-377`): nominal-align, memcpy `attlen`.

**The rule that matters most for 03:** *a value written with a short (1-byte)
varlena header is never preceded by alignment padding.* Three of the five varlena
arms skip alignment. A format that always aligns is still *readable* by
PostgreSQL (§3.1) but is not byte-identical, and — more seriously — a decoder
that always aligns cannot read PostgreSQL's output.

### 3.1 The alignment macros

`src/include/access/tupmacs.h`:

| macro | line | behaviour |
|---|---|---|
| `att_align_nominal(off, attalign)` | `:150-159` | unconditional align by the `TYPALIGN_*` char |
| `att_nominal_alignby(off, attalignby)` | `:165` | `TYPEALIGN(attalignby, off)` — PG18's `CompactAttribute.attalignby` form |
| `att_align_pointer` / `att_pointer_alignby` | `:114-120` / `:129-134` | **the peeking read-side variant**: if `attlen == -1` and `VARATT_NOT_PAD_BYTE(attptr)` (first byte non-zero), do *not* align; otherwise align |
| `att_align_datum` / `att_datum_alignby` | `:91` / `:98-103` | the write-side analogue, keyed on `VARATT_IS_SHORT` |
| `att_addlength_pointer(off, attlen, attptr)` | `:185-201` | `attlen > 0` → `+attlen`; `-1` → `+VARSIZE_ANY(attptr)`; `-2` → `+strlen+1` |
| `att_addlength_datum` | `:173-174` | same, via `DatumGetPointer` |
| `fetchatt` / `fetch_att` | `:47-75` | byval → load 1/2/4/8 by `attlen`; byref → `PointerGetDatum(T)`, **no copy** |
| `store_att_byval` | `:210-` | the byval inverse |

The peeking rule's correctness argument is at `tupmacs.h:104-112`, and it is
worth quoting exactly, because the obvious paraphrase is wrong:

> A zero byte must be either a pad byte, or the first byte of a correctly
> aligned 4-byte length word; in either case we can align safely. A non-zero
> byte must be either a 1-byte length word, **or the first byte of a correctly
> aligned 4-byte length word**; in either case we need not align.

**The peek decides only whether to align. It does not decide short-versus-long.**
On a little-endian host the first byte of a 4-byte header (`VARSIZE << 2`) is
non-zero whenever the low six bits of the length are non-zero — which is most of
the time. A decoder that reads "non-zero ⇒ short header" corrupts the tuple; the
header form is a separate test on the low bits (`VARATT_IS_1B`, `varatt.h:302`).

This is what makes the format self-describing without an offset array.

---

## 4. `slot_deform_heap_tuple` — packed bytes → `tts_values`, lazily

`src/backend/executor/execTuples.c:1122-1207` (declared `:75`,
`pg_attribute_always_inline`).

- `natts = Min(HeapTupleHeaderGetNatts(tuple->t_data), natts)` (`:1131`) — never
  deform past the tuple's stored natts. (Attributes beyond it are `ALTER TABLE
  ADD COLUMN` fast defaults, filled by `slot_getmissingattrs`.)
- **The watermark** (`:1137-1150`): `attnum = slot->tts_nvalid`. If it is 0,
  start fresh with `off = 0, slow = false`. Otherwise **resume**: `off = *offp`
  — the per-slot saved byte offset, `HeapTupleTableSlot.off`
  (`src/include/executor/tuptable.h:266`) — and `slow = TTS_SLOW(slot)`
  (`TTS_FLAG_SLOW = 1<<3`, `tuptable.h:102-104`).
- Dispatch (`:1161-1183`): when `!slow`, call
  `slot_deform_heap_tuple_internal` with `slow` and `hasnulls` as
  compile-time constants from two distinct call sites, so the compiler
  specialises the loop and deletes the dead branches.
- If work remains (`attnum < natts`, `:1186`), re-enter with `slow = true`.
- Save state (`:1202-1206`): `tts_nvalid = attnum`, `*offp = off`, set or clear
  `TTS_FLAG_SLOW`.

**`slot_deform_heap_tuple_internal`** — `:1019-1105`. `tp = (char *) tup +
tup->t_hoff` (`:1030`); `bp = tup->t_bits`. Per attribute:

- `hasnulls && att_isnull(attnum, bp)` → `values = 0, isnull = true`; if not yet
  slow, set `*slowp = true` and **return immediately** so the caller re-enters in
  slow mode (`:1035-1048`).
- Offset resolution (`:1053-1082`):
  - fast path with `attcacheoff >= 0` → `*offp = attcacheoff`;
  - `attlen == -1` → the offset may be cached **only** if `*offp` is already
    `att_nominal_alignby`-aligned (in which case no pad bytes can exist either
    way); otherwise use the peeking `att_pointer_alignby(*offp, attalignby, -1,
    tp + *offp)` and set `slownext`;
  - otherwise → `att_nominal_alignby` and cache into `attcacheoff`.
- `values[attnum] = fetchatt(thisatt, tp + *offp)` (`:1085`) — **by-reference
  Datums alias the tuple buffer**; nothing is copied, and their validity is
  bounded by the tuple's lifetime.
- `*offp = att_addlength_pointer(*offp, attlen, tp + *offp)` (`:1087`).
- Switch to slow (`:1090-1103`) if `slownext` or `attlen <= 0`.

So **"slow" means "the `attcacheoff` fixed-offset cache is unusable"**, which
becomes true the moment a NULL or a variable-length attribute is seen. Note the
asymmetry this creates: the *fixed-width prefix* of a tuple is O(1) to reach; the
suffix after the first varlena is O(n) from that point. There is no offset array.

**`slot_getsomeattrs`** — inline in `tuptable.h`, `slot_getsomeattrs_int` at
`execTuples.c:2091`. The inline (`tuptable.h:359-362`) is a plain `if (slot->tts_nvalid < attnum)
slot_getsomeattrs_int(slot, attnum)` — the `unlikely()` hints live inside `_int`
(`execTuples.c:2097`, `:2107`). `_int` calls `tts_ops->getsomeattrs`, then
fills any shortfall via `slot_getmissingattrs` and bumps `tts_nvalid` to `attnum`
(`:2107-2110`).

**`slot_getattr`** — inline (`tuptable.h:398-404`): return `tts_values[attnum-1]`
when `attnum <= tts_nvalid`, else `slot_getsomeattrs` first. It opens with
`Assert(attnum > 0)` — **it does not handle system attributes.** Those go through
the separate inline `slot_getsysattr` (`tuptable.h:~451`), which answers
`TableOidAttributeNumber` and `SelfItemPointerAttributeNumber` locally and only
then calls `tts_ops->getsysattr`.

Per-slot-kind wrappers: `tts_heap_getsomeattrs` `:346-353`,
`tts_minimal_getsomeattrs` `:544-551`, `tts_buffer_heap_getsomeattrs`
`:751-758` — all three delegate to `slot_deform_heap_tuple`.
`tts_virtual_getsomeattrs` `:130` **`elog(ERROR)`s**: a virtual slot's values are
already present by construction, so being asked to deform one is a bug.

---

## 5. `TupleTableSlot` and the four ops tables

**`TupleTableSlot`** — `tuptable.h:114-131`: `type`, `tts_flags` (uint16),
`tts_nvalid` (`AttrNumber`), `const TupleTableSlotOps *const tts_ops`,
`tts_tupleDescriptor`, `Datum *tts_values`, `bool *tts_isnull`, `tts_mcxt`,
`tts_tid`, `tts_tableOid`.

Flags (`:88-108`): `TTS_FLAG_EMPTY`, `TTS_FLAG_SHOULDFREE`,
`TTS_FLAG_SLOW (1<<3)`, `TTS_FLAG_FIXED (1<<4)`.

**`TupleTableSlotOps`** — `:133-225`: `base_slot_size`, `init`, `release`,
`clear`, `getsomeattrs(slot, natts)`, `getsysattr`, `is_current_xact_tuple`,
`materialize`, `copyslot(dst, src)`, `get_heap_tuple`, `get_minimal_tuple`,
`copy_heap_tuple`, `copy_minimal_tuple(slot, extra)`.

**What `materialize` means** — `:174-178`, verbatim: *"Make the contents of the
slot solely depend on the slot, and not on underlying resources (like another
memory context, buffers, etc)."* Concretely:

- `tts_virtual_materialize` `execTuples.c:176` — walks `tts_values`, sums the
  by-reference sizes, allocates **one** `VirtualTupleTableSlot.data` block in
  `tts_mcxt`, copies every by-reference Datum into it and repoints
  `tts_values`. **No tuple header is built** — a materialized virtual slot is
  still a Datum array, just a self-contained one.
- `tts_heap_materialize` `:399` — builds an owned `HeapTuple` (via
  `heap_form_tuple` from values, or `heap_copy_tuple`) in `tts_mcxt`; sets
  `TTS_FLAG_SHOULDFREE`.
- `tts_minimal_materialize` `:587` — the same, producing an owned
  `MinimalTuple`.
- `tts_buffer_heap_materialize` `:804` — copies the tuple out of the shared
  buffer into local memory and **drops the buffer pin**. This is the case that
  most literally justifies the name.

**The four tables** — `execTuples.c:1210-1284`:

| | `TTSOpsVirtual` `:1210` | `TTSOpsHeapTuple` `:1231` | `TTSOpsMinimalTuple` `:1249` | `TTSOpsBufferHeapTuple` `:1267` |
|---|---|---|---|---|
| backing struct | `VirtualTupleTableSlot` (`tuptable.h:243-249`) | `HeapTupleTableSlot` (`:251-261`) | `MinimalTupleTableSlot` (`:275-`) | `BufferHeapTupleTableSlot` (`:264-273`) |
| `getsomeattrs` | errors — values already valid | `tts_heap_getsomeattrs` | `tts_minimal_getsomeattrs` | `tts_buffer_heap_getsomeattrs` |
| `get_heap_tuple` | **NULL** | `:452` | **NULL** | present |
| `get_minimal_tuple` | **NULL** | **NULL** | `:649` | **NULL** |
| `copy_heap_tuple` | yes | yes | yes | yes |
| `copy_minimal_tuple` | yes | yes | yes | yes |

`MinimalTupleTableSlot` (`tuptable.h:275-`) keeps a `HeapTuple tuple` pointing at
an embedded `minhdr` whose `t_data` is `mintuple - MINIMAL_TUPLE_OFFSET`. Its
comment (`tuptable.h:292-297`): *"This allows column extraction to treat the case
identically to regular physical tuples."* This is the §2 offset trick in its one
load-bearing use.

Fetch helpers: `ExecFetchSlotHeapTuple` `:1833`, `ExecFetchSlotMinimalTuple`
`:1881`, `ExecFetchSlotHeapTupleDatum` `:1912`; inlines `ExecMaterializeSlot`
`tuptable.h:476`, `ExecCopySlotMinimalTuple` `:496`,
`ExecCopySlotMinimalTupleExtra` `:508`.

**The asymmetry worth naming.** `TTSOpsVirtual` is the only kind with no packed
form, and it is also the only kind whose `getsomeattrs` is an error. The four
kinds are not four equivalent representations: three are *packed with a Datum
scratch array*, and one is *a Datum array with no packed form*. Producers use the
virtual kind; every bulk store uses a packed kind.

---

## 6. Where the executor actually stores tuples

| site | representation | packed? |
|---|---|---|
| **Hash join, in memory** — `HashJoinTupleData`, `src/include/executor/hashjoin.h:78-92` | `{ union next { unshared ptr \| dsa_pointer shared }; uint32 hashvalue; }` then, per the comment at `:86`, *"Tuple data, in MinimalTuple format, follows on a MAXALIGN boundary"*. `HJTUPLE_OVERHEAD = MAXALIGN(sizeof(HashJoinTupleData))` `:90`; `HJTUPLE_MINTUPLE(hjtup) = (MinimalTuple)((char *) hjtup + HJTUPLE_OVERHEAD)` `:91-92`. Built by `ExecCopySlotMinimalTupleExtra(slot, HJTUPLE_OVERHEAD)` — this is what `heap_form_minimal_tuple`'s `extra` argument exists for | **packed**, header and tuple in one allocation |
| **Hash join, spilled** — `ExecHashJoinSaveTuple`, `src/backend/executor/nodeHashjoin.c:1414-1445` | `BufFileWrite(file, &hashvalue, sizeof(uint32))` then `BufFileWrite(file, tuple, tuple->t_len)` (`:1443-1444`) — the raw MinimalTuple bytes, `t_len` and padding included | **packed** |
| **Tuplestore** — `tuplestore_puttupleslot`, `src/backend/utils/sort/tuplestore.c` | `ExecCopySlotMinimalTuple` then `tuplestore_puttuple_common`. On spill, `writetup_heap` `:1599-1605` writes from `(char *) tuple + MINIMAL_TUPLE_DATA_OFFSET` for `t_len - MINIMAL_TUPLE_DATA_OFFSET` bytes — since `MINIMAL_TUPLE_DATA_OFFSET` is 10 that skips **the 4-byte `t_len` and the 6 pad bytes**, not the padding alone; `readtup_heap` `:1620-1625` reconstructs both, `t_len` from the record length | **packed** |
| **Tuplesort** — `SortTuple`, `src/include/utils/tuplesort.h:147-154` | `{ void *tuple; Datum datum1; bool isnull1; int srctape; }`. `tuple` is a separately-palloc'd MinimalTuple (heap sort) or IndexTuple; `datum1` caches **the first sort key**, to avoid a `heap_getattr` per comparison (`:128-133`) — with two overrides at `:135-146`: under abbreviated keys it holds the *abbreviated* key in place of column 1, and in the single-Datum sort case it *is* the value with `tuple` possibly NULL, so it is not unconditionally "column 1". `tuplesort_puttupleslot` `tuplesortvariants.c:751-770` reconstructs a fake `HeapTupleData` with `t_len += MINIMAL_TUPLE_OFFSET` and `t_data = (char *) tuple - MINIMAL_TUPLE_OFFSET` to call `heap_getattr` | **packed** payload + exactly one hoisted Datum |
| **Memoize** — `src/backend/executor/nodeMemoize.c` | `MemoizeTuple{ MinimalTuple mintuple; ... }` `:96`; cache key `MemoizeKey{ MinimalTuple params; }` `:107`. Stored via `ExecCopySlotMinimalTuple` (`:558`, `:637`), read back via `ExecStoreMinimalTuple` (`:230`, `:328`, `:760`, `:858`); every slot is `&TTSOpsMinimalTuple` (`:981`, `:987`, `:999`, `:1028`) | **packed** — both the cached rows and the parameter key |
| **Aggregate hash table** — `TupleHashEntryData`, `src/include/nodes/execnodes.h:843-848` | `{ MinimalTuple firstTuple; uint32 status; uint32 hash; }`; simplehash with `SH_KEY_TYPE MinimalTuple` (`:852`). (`AggState.grp_firstTuple` `:2561` is a full `HeapTuple`, but that is the *sorted* grouping path, one row at a time) | **packed** as both key and stored representative |

Note the recurring shape in the two hash cases: **a 4-byte hash value stored
immediately ahead of the packed tuple**, so a probe can reject on the hash
without touching the tuple at all. goopg's `spill.go:104 WriteRowHashed` already
mirrors this framing and cites `nodeHashjoin.c:1414` (see 02 §6).

---

## 7. What PostgreSQL's `Datum` actually is

- `typedef uintptr_t Datum;` — `src/include/postgres.h:69`.
- `#define SIZEOF_DATUM SIZEOF_VOID_P` — `postgres.h:86`;
  `#define SIZEOF_VOID_P 8` — `src/include/pg_config.h:659`.

So `sizeof(Datum) == 8` **on this build**, and the "8 bytes" figure quoted
throughout the goopg documents comes from nothing more than the platform's
pointer width — on a 32-bit build it is 4.

How a value is stored and read is decided entirely by `attbyval`. `fetch_att`
(`tupmacs.h:53-75`) loads a `char`/`int16`/`int32`/`Datum` directly out of the
tuple for by-value types; the `sizeof(Datum)` arm is `#if SIZEOF_DATUM == 8`
guarded — the same test that sets `FLOAT8PASSBYVAL` (`c.h:600-604`) and hence
`pg_type.typbyval`, which is why `int8`/`float8`/`timestamp` are pass-by-value
only on 64-bit builds. `fetch_att`'s guard is a sibling consequence of that test,
not its cause. For by-reference types it returns `PointerGetDatum(T)` — a
bare pointer *into the tuple buffer*. **The Datum owns nothing**; its validity is
bounded by the tuple's lifetime.

A Datum array therefore costs `8 × natts` of directly addressable memory *plus*
whatever the by-reference pointers reference — which is exactly why the executor
spends the machinery of §4 avoiding materialising one.

---

## 8. The checklist this document hands to 03 and 04

A goopg in-memory packed format that wants PostgreSQL's bytes must reproduce:

1. The 15-byte `MinimalTupleData` header shape, or a header carrying the same
   four fields with the same meanings (§2).
2. `t_hoff = MAXALIGN(header + BITMAPLEN)`, and the invariant that `t_hoff`
   includes `MINIMAL_TUPLE_OFFSET` while `t_len` does not (§2).
3. Null bitmap with **bit set = NOT NULL**, little-endian within the byte, and
   NULL columns consuming **zero data bytes** (§3).
4. `HEAP_HASNULL` / `HEAP_HASVARWIDTH` / `HEAP_HASEXTERNAL` stamped per the
   `fill_val` rules (§3).
5. Per-attribute `attalign` / `attlen` / `attbyval` handling (§3).
6. **Conditional alignment on both sides**: `att_align_datum` on write (skip
   alignment when emitting a short varlena) and `att_align_pointer` on read (skip
   alignment when the cursor byte is non-zero) (§3.1). Nominal-only alignment is
   readable-by-PG but is neither byte-identical nor able to read PG's output.
7. Varlena short (1-byte) vs long (4-byte) headers, and the `attstorage != 'p'`
   packability gate (§3).
8. A resumable `(nvalid, offset)` watermark held **across operator calls**, with
   the slow/fast distinction or an equivalent (§4).
9. `natts` in the header, and the "stored natts may be less than descriptor
   natts" rule that makes `ALTER TABLE ADD COLUMN` fast defaults work (§4).

Items 1–5, 7 and 9 already exist in goopg (02 §5). Item 6 exists in exactly one
place and not on the executor path (03 §3). Item 8 exists as a function but has
no owner (02 §5.3).

(End of file)
