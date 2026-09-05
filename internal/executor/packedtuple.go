package executor

// packedtuple.go — D-03 (MD-03): the retained-row packed format.
//
// `docs/design/not_ralph/minimize_datum/04-target-design.md` §2.1 and
// `03-byte-format-fidelity.md` §7.1 (TD-3) specify a retained executor row in
// PostgreSQL's MinimalTuple layout: one allocation holding header, null bitmap
// and column data, owning its bytes, so nothing it references can be
// invalidated by a producer's next Next() or by an arena reset.
//
// THIS FILE HAS NO PRODUCER. D-03 lands the format and the slot deliberately
// unreachable from the pipeline (TODO_ALL D-03: "**No producer.**"), so that
// R-0's six type-switch arms and their tests exist BEFORE any operator can
// emit one. The conversion slices (MD-04 onward) supply the producers.

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// MinimalTuple constants (postgres/src/include/access/htup_details.h:674-704,
// transcribed in 01-pg-tuple-representation.md §2 with the arithmetic already
// evaluated for a 64-bit build: MAXIMUM_ALIGNOF = 8,
// offsetof(HeapTupleHeaderData, t_infomask2) = 18).
// ---------------------------------------------------------------------------

const (
	// maximumAlignof is PG's MAXIMUM_ALIGNOF on every platform goopg targets.
	maximumAlignof = 8

	// minimalTupleOffset is MINIMAL_TUPLE_OFFSET = (18 - 4) / 8 * 8. It is the
	// distance PG shifts a HeapTupleData.t_data pointer to make heap routines
	// work on a minimal tuple unchanged. goopg does NOT port that trick (03
	// §7.1: "Do not port the negative-offset trick") — but the constant still
	// matters, because t_hoff is stored on the HEAP scale and therefore
	// INCLUDES this distance (htup_details.h:668, heaptuple.c:1516
	// `tuple->t_hoff = hoff + MINIMAL_TUPLE_OFFSET`). Keeping the stored value
	// byte-identical to PG's is what lets 06 §5's golden test compare bytes
	// rather than fields; the accessors below subtract it instead of
	// offsetting a pointer.
	minimalTupleOffset = 8

	// minimalTuplePadding is MINIMAL_TUPLE_PADDING = (18 - 4) % 8. Six bytes
	// Go does not need, kept so every downstream offset equals PG's.
	minimalTuplePadding = 6

	// minimalTupleDataOffset is MINIMAL_TUPLE_DATA_OFFSET =
	// offsetof(MinimalTupleData, t_infomask2): the offset of the first
	// non-pad byte, which is what tuplestore's writetup_heap skips.
	minimalTupleDataOffset = 4 + minimalTuplePadding

	// sizeofMinimalTupleHeader is SizeofMinimalTupleHeader =
	// offsetof(MinimalTupleData, t_bits) (htup_details.h:704).
	//
	//	t_len(4) | mt_padding(6) | t_infomask2(2) | t_infomask(2) | t_hoff(1)
	sizeofMinimalTupleHeader = 15

	// offsets within the minimal tuple.
	mtOffLen        = 0
	mtOffInfomask2  = 10
	mtOffInfomask   = 12
	mtOffHoff       = 14
	mtOffBits       = sizeofMinimalTupleHeader
	mtNattsMaskBits = 0x07FF

	// packedHashPrefixLen is the width of the 4-byte join-hash prefix stored
	// immediately ahead of the tuple in the SAME allocation (03 §7.1,
	// 01 §6). PG's hash join reserves MAXALIGN(sizeof(HashJoinTupleData)) =
	// 16 there because it also needs a `next` link; goopg's retention is a Go
	// slice of tuples, so the link has no analogue and only the hash value is
	// stored. Four little-endian bytes ahead of the payload is exactly the
	// framing `spill.go`'s WriteRowHashed already writes (spill.go:104,
	// citing nodeHashjoin.c:1414) — the in-memory side adopts the framing its
	// own spill path already has, so a spilled and a resident tuple differ in
	// the payload encoding only, never in the framing.
	packedHashPrefixLen = 4
)

// maxAlign is PG's MAXALIGN.
func maxAlign(n int) int { return (n + (maximumAlignof - 1)) &^ (maximumAlignof - 1) }

// bitmapLenPG is PG's BITMAPLEN(natts).
func bitmapLenPG(natts int) int { return (natts + 7) / 8 }

// ---------------------------------------------------------------------------
// TupleDesc — 04 §3 (D-1)
// ---------------------------------------------------------------------------

// TupleDesc is the executor-local descriptor a PackedTuple is undecodable
// without: the packed bytes are typed by the descriptor, not by the value
// (03 §6). It is 04 §3's Decision D-1, built here because D-01 landed only the
// per-column payload (`colTypeInfo.attLen/attByVal/attStorage`) and left the
// descriptor that carries it to this slice.
//
// R-7 — WHO OWNS THE DESCRIPTOR PAST THE OPERATOR (04 §9.8). The risk is that
// MD-08 (CTERowCache) and MD-10 (conn_tx.Rows) retain packed tuples whose
// lifetime is the CURSOR or the TRANSACTION, not the operator `Open` that
// built the descriptor — and a packed tuple without its descriptor is
// undecodable bytes.
//
// Decision, and it is a property of this type rather than a convention:
//
//  1. A TupleDesc is DERIVED ONLY FROM A PLAN ARTEFACT (`optimizer.Schema`, or
//     a `[]catalog.Column` snapshot) and is IMMUTABLE after construction. It
//     holds no pointer into live catalog state, no relation handle and no
//     session state, so retaining one cannot make catalog state stale. That is
//     what makes it legal to hold past the operator at all; `colTypeInfo`'s
//     staleness contract (coltypeinfo.go:12-25) is honoured because the
//     descriptor is re-derived wherever the SCHEMA is re-derived, and an ALTER
//     TABLE that changes a column's type invalidates the PLAN, which is what
//     the retained rows hang off.
//  2. Therefore the rule for retention is a LIFETIME rule, not an
//     invalidation rule: **whatever owns a buffer of PackedTuples must own the
//     *TupleDesc beside it**, for at least as long. A `[]PackedTuple` field
//     without an adjacent `*TupleDesc` field is a bug at the DEFINITION, and
//     that is the shape MD-08 and MD-10 must adopt before they start.
//  3. A PackedTuple deliberately does NOT carry a descriptor pointer. One
//     pointer per retained row is exactly the per-row overhead this bundle
//     exists to remove, and it would also let two rows in one buffer disagree
//     about their type. The descriptor is held once, by the owner of the
//     buffer, and reaches the bytes only through a PackedSlot.
//
// What this decision does NOT resolve, and MD-08/MD-10 still must: a cursor
// that survives a DDL on a table its plan reads. That is plan invalidation,
// which goopg does not do today for open cursors, and it is a pre-existing
// hazard for `[]Row` retention too — packing neither creates it nor fixes it.
type TupleDesc struct {
	// cols is what the existing codec entry points take. Only three fields of
	// catalog.Column are ever read by the codec (04 §3): Type (load-bearing),
	// Name (error text) and MissingValue (the ALTER TABLE ADD COLUMN fast
	// default). A schema-derived descriptor leaves MissingValue nil, which is
	// correct: an intermediate row has no ALTER TABLE history.
	cols []catalog.Column
	// info is the per-column resolution D-01 landed; passing it to
	// decodeRowRangeInfo is the whole point of not using the exported wrapper
	// (see PackedSlot.deformTo).
	info []colTypeInfo

	// attCacheOff is PG's attcacheoff (01 §4, 04 §6 D-4), precomputed once per
	// descriptor: the fixed byte offset of column i within the data area, or
	// -1 when no fixed offset exists. PG's rule, transcribed: an offset is
	// cacheable only while every preceding attribute is fixed-width, so the
	// cache dies at the first `attlen <= 0` and never revives.
	//
	// The per-TUPLE half of PG's rule (the cache is also unusable once a NULL
	// has been seen) is not encoded here because it is not a property of the
	// descriptor; `hasNulls` on the tuple answers it (see fixedOffset).
	attCacheOff []int32

	// hasVarWidth is true when any column is a varlena, i.e. whether a formed
	// tuple can carry HEAP_HASVARWIDTH. Derived from attLen == -1.
	hasVarWidth bool
	// mayBeToasted is true when any column's attstorage permits out-of-line
	// storage ('e' external, 'x' extended, 'm' main); a 'p' (plain) column can
	// never hold a TOAST pointer. Used to reject a KindToastPointer datum
	// packed into a column that cannot hold one, which would otherwise be a
	// silent retyping on read.
	mayBeToasted bool
}

// NewTupleDesc builds a descriptor from a plan node's output schema. This is
// the intermediate-row case — join, aggregate and sort outputs carry only an
// optimizer.Schema (02 §9) — and it is where the retention lives.
//
// Call it once, from the operator's Open, exactly as resolveColTypeInfo's
// contract requires (coltypeinfo.go:12-25), and hand it to whatever owns the
// retained rows (see R-7 above).
func NewTupleDesc(s optimizer.Schema) *TupleDesc {
	cols := make([]catalog.Column, len(s))
	for i, sc := range s {
		cols[i] = catalog.Column{Name: sc.Name, Type: sc.Type, Ordinal: i}
	}
	return NewTupleDescFromColumns(cols)
}

// NewTupleDescFromColumns builds a descriptor over a column list a scan
// already holds. The slice is retained, not copied: callers pass the same
// snapshot they pass to the codec, and both must stay immutable for the
// descriptor's lifetime.
func NewTupleDescFromColumns(cols []catalog.Column) *TupleDesc {
	d := &TupleDesc{cols: cols, info: resolveColTypeInfo(cols)}
	d.attCacheOff = make([]int32, len(cols))

	// PG's attcacheoff walk (heaptuple.c / execTuples.c:1053-1082): keep a
	// running offset while every attribute so far is fixed-width; the first
	// varlena (attlen == -1) ends caching for it and for everything after it.
	off := 0
	caching := true
	for i := range cols {
		in := &d.info[i]
		if in.attLen == -1 {
			d.hasVarWidth = true
		}
		switch in.attStorage {
		case 'e', 'x', 'm':
			d.mayBeToasted = true
		}
		if !caching || in.attLen <= 0 {
			// attLen <= 0 covers both the varlena case (-1) and the
			// cstring/undetermined case (0, which goopg's descriptor uses as
			// its fail-safe for a type it cannot name — coltypeinfo.go). Both
			// make every later offset value-dependent.
			caching = false
			d.attCacheOff[i] = -1
			continue
		}
		off = alignPhysicalPGOffset(off, in.align)
		d.attCacheOff[i] = int32(off)
		off += int(in.attLen)
	}
	return d
}

// Width returns the descriptor's column count.
func (d *TupleDesc) Width() int { return len(d.cols) }

// Columns exposes the descriptor's column list for the codec entry points.
// The slice is the descriptor's own and must not be mutated.
func (d *TupleDesc) Columns() []catalog.Column { return d.cols }

// fixedOffset answers PG's attcacheoff question for column col: the fixed byte
// offset of the column within the tuple's data area, and whether that offset is
// usable at all. hasNulls is the tuple's HEAP_HASNULL bit — PG's cache is
// unusable the moment a NULL has been seen (01 §4), and since a null bitmap
// makes the position of every following column value-dependent, a tuple with
// any NULL declines the cache wholesale rather than tracking where the first
// NULL fell.
//
// CONSUMPTION IS DEFERRED, DELIBERATELY. The place a fixed offset would pay is
// the per-column loop inside `decodeRowRangeInfo` (codec.go:1356), which is
// SHARED with every live scan; threading the cache into it changes a path that
// ships, and D-03 is specified to be unreachable and behaviour-free. So this
// slice lands the cache and its test (the descriptor half of 04 §6 D-4) and
// the deform loop keeps recomputing alignment; the consuming half moves with
// the first slice that has a producer to measure it. 04 §9.10 (R-9) already
// records that the cache cannot help this bundle's own witness — the Q9 build
// side is varlena from column 0 or 1 — so nothing downstream is waiting on it.
func (d *TupleDesc) fixedOffset(col int, hasNulls bool) (int, bool) {
	if hasNulls || col < 0 || col >= len(d.attCacheOff) {
		return 0, false
	}
	o := d.attCacheOff[col]
	if o < 0 {
		return 0, false
	}
	return int(o), true
}

// ---------------------------------------------------------------------------
// PackedTuple — 04 §2.1, 03 §7.1 (TD-3)
// ---------------------------------------------------------------------------

// PackedTuple is a retained row in PG MinimalTuple layout. It owns its bytes;
// nothing it references can be invalidated by a producer's next Next() or by
// an arena reset (04 §7: packing COPIES the bytes in, so a packed tuple is
// arena-independent by construction).
//
// One allocation per retained row, holding header, bitmap and data, as
// heap_form_minimal_tuple does:
//
//	[hash prefix] t_len(4) | mt_padding(6) | t_infomask2(2) | t_infomask(2) |
//	              t_hoff(1) | t_bits[] | pad | data
//
// The optional hash prefix sits in the same allocation ahead of the tuple,
// which is `heap_form_minimal_tuple`'s `extra` argument and PG's hash join's
// only use of it (01 §6).
//
// Accessors are hoff-relative. There is no negative-offset trick (03 §7.1):
// t_hoff is stored on PG's HEAP scale, so the data area starts at
// `t_hoff - minimalTupleOffset` bytes into the tuple, and every accessor here
// does that subtraction rather than shifting a pointer.
type PackedTuple struct {
	// buf is the whole allocation: the tuple, preceded by `extra` bytes of
	// caller prefix.
	buf []byte
	// extra is the prefix width in bytes — 0 for a bare tuple,
	// packedHashPrefixLen for a hashed one. A byte, not a slice header, so a
	// retained row costs one slice plus one word.
	extra uint8
}

// Valid reports whether t holds a tuple at all. The zero PackedTuple does not.
func (t PackedTuple) Valid() bool { return len(t.buf) >= int(t.extra)+sizeofMinimalTupleHeader }

// tuple returns the minimal tuple, prefix excluded.
func (t PackedTuple) tuple() []byte { return t.buf[t.extra:] }

// Bytes returns the whole allocation, prefix included. It is the form
// `spill.go`'s frame writer wants and the form a golden test compares.
func (t PackedTuple) Bytes() []byte { return t.buf }

// TupleBytes returns the minimal tuple alone, prefix excluded — `t_len` bytes,
// which is exactly what ExecHashJoinSaveTuple writes (nodeHashjoin.c:1443).
func (t PackedTuple) TupleBytes() []byte { return t.tuple() }

// Len is t_len: the length of the minimal tuple, prefix EXCLUDED and the
// MINIMAL_TUPLE_OFFSET distance NOT included (htup_details.h:668).
func (t PackedTuple) Len() int {
	return int(binary.LittleEndian.Uint32(t.tuple()[mtOffLen:]))
}

// Infomask2 is t_infomask2; its low 11 bits are natts.
func (t PackedTuple) Infomask2() uint16 {
	return binary.LittleEndian.Uint16(t.tuple()[mtOffInfomask2:])
}

// Infomask is t_infomask.
func (t PackedTuple) Infomask() uint16 {
	return binary.LittleEndian.Uint16(t.tuple()[mtOffInfomask:])
}

// Hoff is the STORED t_hoff, i.e. PG's value, which includes
// MINIMAL_TUPLE_OFFSET. Use dataOffset for the offset within the tuple.
func (t PackedTuple) Hoff() uint8 { return t.tuple()[mtOffHoff] }

// natts is HeapTupleHeaderGetNatts. A tuple whose natts is below the
// descriptor's width is the ALTER TABLE ADD COLUMN case, and the decoder
// resolves the shortfall through MissingValue exactly as it does for a heap
// tuple (codec.go:1361, 03 §2.4).
func (t PackedTuple) natts() int { return int(t.Infomask2() & mtNattsMaskBits) }

// Natts exports natts for callers outside the deform path.
func (t PackedTuple) Natts() int { return t.natts() }

// hasNulls is the HEAP_HASNULL bit.
func (t PackedTuple) hasNulls() bool { return t.Infomask()&storage.HeapHasNull != 0 }

// dataOffset is the offset of the data area within the minimal tuple. This is
// the hoff-relative accessor 03 §7.1 asks for, and the one place the stored
// t_hoff's PG scale is undone.
func (t PackedTuple) dataOffset() int { return int(t.Hoff()) - minimalTupleOffset }

// bitmap is t_bits, or nil when the tuple has no NULLs. The convention is
// NullBitmapPG's and PG's: bit i SET means column i is NOT null.
func (t PackedTuple) bitmap() []byte {
	if !t.hasNulls() {
		return nil
	}
	tup := t.tuple()
	return tup[mtOffBits : mtOffBits+bitmapLenPG(t.natts())]
}

// data is the column-data area, the argument decodeRowRangeInfo calls `data`.
func (t PackedTuple) data() []byte {
	tup := t.tuple()
	return tup[t.dataOffset():t.Len()]
}

// HashValue returns the 4-byte join hash stored ahead of the tuple, and
// whether one is present. A probe rejects on this without touching the tuple
// (01 §6).
func (t PackedTuple) HashValue() (uint32, bool) {
	if t.extra != packedHashPrefixLen {
		return 0, false
	}
	return binary.LittleEndian.Uint32(t.buf[:packedHashPrefixLen]), true
}

// FormPackedTuple packs one row into a PackedTuple, the analogue of
// heap_form_minimal_tuple with extra = 0.
//
// ctx may be nil; it is threaded only so a reg*[] column resolves its element
// names through the session catalog, exactly as EncodeRowPGCtx documents.
func FormPackedTuple(desc *TupleDesc, row Row, ctx *Context) (PackedTuple, error) {
	return formPackedTuple(desc, row, ctx, 0, 0)
}

// FormPackedTupleHashed packs one row with its 32-bit join hash immediately
// ahead of it in the same allocation — 03 §7.1's hash-value prefix, and
// heap_form_minimal_tuple's `extra` argument put to the one use PG puts it to.
func FormPackedTupleHashed(desc *TupleDesc, row Row, hash uint32, ctx *Context) (PackedTuple, error) {
	return formPackedTuple(desc, row, ctx, packedHashPrefixLen, hash)
}

func formPackedTuple(desc *TupleDesc, row Row, ctx *Context, extra int, hash uint32) (PackedTuple, error) {
	if desc == nil {
		return PackedTuple{}, fmt.Errorf("FormPackedTuple: nil descriptor")
	}
	if len(row) != len(desc.cols) {
		return PackedTuple{}, fmt.Errorf("FormPackedTuple: %d cols vs %d datums", len(desc.cols), len(row))
	}
	if len(row) > mtNattsMaskBits {
		return PackedTuple{}, fmt.Errorf("FormPackedTuple: %d columns exceeds the %d the natts field can hold", len(row), mtNattsMaskBits)
	}

	// R-1/R-2 (04 §9.2, §9.3): validate on the ENCODE side so decode is
	// total. A tuple only exists if every column encoded, which is what makes
	// a deform failure an invariant violation rather than a data-dependent
	// error the value-less Get(col) signature cannot report.
	var infomask uint16
	for i := range row {
		d := row[i]
		if d.IsNull() {
			continue
		}
		if desc.info[i].attLen == -1 {
			infomask |= storage.HeapHasVarWidth
		}
		if d.Kind == KindToastPointer {
			// PER-COLUMN, not descriptor-wide. `desc.mayBeToasted` is true
			// if ANY column can be toasted, so testing it here accepted a
			// TOAST pointer in a plain `int4` column of an `{int4, text}`
			// descriptor — which is every realistic intermediate row — while
			// the error message asserted a per-column fact. Found by review;
			// the original test missed it because both fixtures were
			// single-column. R-2's "encode-side validation makes decode
			// total" argument depends on this being exact.
			if desc.info[i].attStorage == 'p' {
				return PackedTuple{}, fmt.Errorf(
					"FormPackedTuple: column %d (%s) has attstorage 'p' and cannot hold a TOAST pointer",
					i, desc.cols[i].Name)
			}
			infomask |= storage.HeapHasExternal | storage.HeapHasVarWidth
		}
	}

	data, err := EncodeRowPGCtx(desc.cols, row, ctx, 0)
	if err != nil {
		return PackedTuple{}, err
	}
	bitmap := NullBitmapPG(row)
	if bitmap != nil {
		infomask |= storage.HeapHasNull
	}

	// heap_form_minimal_tuple (heaptuple.c:1487-1516), transcribed.
	hlen := sizeofMinimalTupleHeader
	if bitmap != nil {
		hlen += bitmapLenPG(len(row))
	}
	hoff := maxAlign(hlen)
	tlen := hoff + len(data)

	buf := make([]byte, extra+tlen)
	tup := buf[extra:]
	binary.LittleEndian.PutUint32(tup[mtOffLen:], uint32(tlen))
	binary.LittleEndian.PutUint16(tup[mtOffInfomask2:], uint16(len(row))&mtNattsMaskBits)
	binary.LittleEndian.PutUint16(tup[mtOffInfomask:], infomask)
	// t_hoff is stored on the HEAP scale: PG writes `hoff +
	// MINIMAL_TUPLE_OFFSET` (heaptuple.c:1516) and every reader subtracts it
	// back. Keeping the stored byte identical to PG's is the whole reason the
	// 6 pad bytes above are kept too.
	tup[mtOffHoff] = uint8(hoff + minimalTupleOffset)
	copy(tup[mtOffBits:], bitmap)
	copy(tup[hoff:], data)
	if extra == packedHashPrefixLen {
		binary.LittleEndian.PutUint32(buf[:packedHashPrefixLen], hash)
	}
	return PackedTuple{buf: buf, extra: uint8(extra)}, nil
}
