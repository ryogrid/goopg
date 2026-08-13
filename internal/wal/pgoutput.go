// `pgoutput` output plugin for the M0008 logical-decoding
// pipeline.
//
// PgOutput implements the `OutputPlugin` interface
// (`Begin` / `Change` / `Commit`) and emits messages whose byte
// shape matches upstream PostgreSQL's pgoutput v1 protocol. The
// per-message layouts are pinned in
// docs/design/0008-0002-pgoutput-plugin.md.
//
// v0 covers the message types the M0008 DoD requires:
//
//   - `B` Begin    (per-xact prologue)
//   - `C` Commit   (per-xact epilogue)
//   - `R` Relation (one per relation per session, lazy-emitted
//                   on the first change touching that rel)
//   - `I` Insert
//   - `D` Delete   (no pre-image in v0; tuple body is 0 attrs)
//
// `U` Update is deferred: v0's executor emits UPDATE as a paired
// HeapDelete + HeapInsert and the reorder buffer doesn't yet
// fold them. TRUNCATE / TYPE / MESSAGE / 2PC / streaming-mode
// messages are out of scope for this milestone.

package wal

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgarray"
	"github.com/goopg/goopg/internal/pgdatetime"
	"github.com/goopg/goopg/internal/pglz"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/storage"
)

// pgoutput message kinds. Mirror upstream's
// `LOGICAL_REP_MSG_*` constants from
// postgres/src/include/replication/logicalproto.h.
const (
	pgoBegin    = 'B'
	pgoCommit   = 'C'
	pgoRelation = 'R'
	pgoInsert   = 'I'
	pgoDelete   = 'D'
	pgoUpdate   = 'U'
	pgoTruncate = 'T'
)

// pgoutput TRUNCATE option-bit constants. Mirror upstream's
// `TRUNCATE_CASCADE` / `TRUNCATE_RESTART_SEQS` in
// postgres/src/include/replication/logicalproto.h. The byte is a
// bitmask: bit 0 = CASCADE, bit 1 = RESTART IDENTITY.
const (
	pgoTruncateCascade     byte = 0x01
	pgoTruncateRestartSeqs byte = 0x02
)

// pgoutput tuple-column status bytes. Mirror upstream's
// `LOGICALREP_COLUMN_*` constants.
const (
	pgoColNull = 'n'
	pgoColText = 't'
)

// RelationFilter decides whether a given (relation, change kind)
// pair should be emitted to the wire. Used by PgOutput to apply
// the publication-membership rules a real subscriber expects:
// `CREATE PUBLICATION p FOR TABLE t1, t2` means changes to t1 /
// t2 land on the wire and changes to other tables don't, with
// the publish=insert,update,delete flags filtering at the
// change-kind level. nil filter = pass everything.
type RelationFilter interface {
	Allows(rel *RelationDef, kind ChangeKind) bool
}

// PgOutput emits logical-replication messages to an io.Writer in
// upstream pgoutput's wire format. One PgOutput per active
// logical slot; not goroutine-safe (the `SlotDecoder` loop is
// sequential by design).
type PgOutput struct {
	snap *CatalogSnapshot
	w    io.Writer

	// emittedRel tracks which relation OIDs have already had
	// their `R` message sent in this session. The first Change
	// touching a relation emits `R` ahead of the per-row
	// message; subsequent changes skip it. Mirrors upstream's
	// `relsynced` set.
	emittedRel map[uint32]struct{}

	// filter, when non-nil, gates every Change emission against
	// publication-membership rules. SetFilter installs it; nil
	// (the default) means "ship every change for every relation
	// in the snapshot" — useful for tests and for the
	// catalog-only initial scaffolding before publications
	// existed.
	filter RelationFilter
}

// NewPgOutput constructs a plugin that writes pgoutput messages
// to `w`. `snap` is the slot-creation-time HISTORIC catalog
// view from `BuildCatalogSnapshot`; the plugin uses it to
// resolve column descriptors when encoding tuples and to emit
// relation metadata on the first reference.
func NewPgOutput(snap *CatalogSnapshot, w io.Writer) *PgOutput {
	return &PgOutput{
		snap:       snap,
		w:          w,
		emittedRel: map[uint32]struct{}{},
	}
}

// SetFilter installs a publication-membership filter. Call once
// at construction time — the SlotDecoder loop is sequential, so
// changing the filter mid-stream isn't supported. Pass nil to
// clear an existing filter.
func (p *PgOutput) SetFilter(f RelationFilter) {
	p.filter = f
}

// Begin emits the per-xact `B` prologue. final_lsn = commitLSN
// matches what upstream's reorder buffer passes (the WAL
// position of the commit record). commit_time is captured at
// emission time — v0 doesn't yet stamp xact-end records with
// the commit timestamp, so the plugin uses time.Now(). Apply
// workers don't depend on commit_time being the exact original.
func (p *PgOutput) Begin(xid storage.TransactionID, commitLSN uint64) error {
	buf := make([]byte, 0, 21)
	buf = append(buf, pgoBegin)
	buf = appendUint64(buf, commitLSN)
	buf = appendUint64(buf, pgoTimestamp(time.Now()))
	buf = appendUint32(buf, uint32(xid))
	_, err := p.w.Write(buf)
	return err
}

// Commit emits the per-xact `C` epilogue. flags is fixed at 0
// (upstream documents the field as "unused for now").
// commit_lsn and end_lsn carry the same value in v0 — there's
// no separate flushed-vs-applied notion at the encoder layer.
func (p *PgOutput) Commit(_ storage.TransactionID, commitLSN uint64) error {
	buf := make([]byte, 0, 26)
	buf = append(buf, pgoCommit)
	buf = append(buf, 0) // flags
	buf = appendUint64(buf, commitLSN)
	buf = appendUint64(buf, commitLSN) // end_lsn
	buf = appendUint64(buf, pgoTimestamp(time.Now()))
	_, err := p.w.Write(buf)
	return err
}

// Change emits `R` (if not yet sent) followed by `I` or `D`.
// Unknown relations (not present in the snapshot) are skipped —
// they were created after slot creation and are out of v0's
// scope.
func (p *PgOutput) Change(c Change) error {
	rel, ok := p.snap.Lookup(c.Rel)
	if !ok {
		// Future loop: surface this as an error once schema
		// changes are honoured. For now skip silently — the
		// classifier already filtered everything actionable.
		return nil
	}
	if p.filter != nil && !p.filter.Allows(rel, c.Kind) {
		// Out of the slot's publication set (or excluded by
		// publish=... flags). No `R` is emitted either —
		// subscribers never need a relation descriptor for
		// changes they'll never receive.
		return nil
	}
	if _, ok := p.emittedRel[rel.OID]; !ok {
		if err := p.writeRelation(rel); err != nil {
			return err
		}
		p.emittedRel[rel.OID] = struct{}{}
	}
	switch c.Kind {
	case ChangeInsert:
		return p.writeInsert(rel, c.NewTuple)
	case ChangeDelete:
		return p.writeDelete(rel, c.OldTuple)
	case ChangeUpdate:
		return p.writeUpdate(rel, c.OldTuple, c.NewTuple)
	}
	return fmt.Errorf("pgoutput: unknown change kind %d", c.Kind)
}

func (p *PgOutput) writeRelation(rel *RelationDef) error {
	buf := make([]byte, 0, 64)
	buf = append(buf, pgoRelation)
	buf = appendUint32(buf, rel.OID)
	buf = appendCString(buf, rel.Schema)
	buf = appendCString(buf, rel.Name)
	// Replica identity: v0 always reports DEFAULT. Replica-
	// identity catalog tracking lands with 0008-0003.
	buf = append(buf, 'd')
	buf = appendUint16(buf, uint16(len(rel.Columns)))
	for _, col := range rel.Columns {
		// flag byte: bit 0 = LOGICALREP_IS_REPLICA_IDENTITY.
		// v0 marks every column as part of REPLICA IDENTITY
		// DEFAULT — close enough that the apply worker's
		// row-resolution path works for tables with primary
		// keys; refines once 0008-0003 lands.
		buf = append(buf, 1)
		buf = appendCString(buf, col.Name)
		buf = appendUint32(buf, pgoColumnTypeOID(col.Type))
		// atttypmod: v0 doesn't track typmod separately;
		// upstream emits -1 (the "no modifier" sentinel)
		// for that case.
		buf = appendUint32(buf, ^uint32(0))
	}
	_, err := p.w.Write(buf)
	return err
}

func (p *PgOutput) writeInsert(rel *RelationDef, tuple []byte) error {
	body, err := encodePgoTuple(rel.Columns, tuple)
	if err != nil {
		return fmt.Errorf("pgoutput: encode insert tuple for %q: %w", rel.Name, err)
	}
	buf := make([]byte, 0, 6+len(body))
	buf = append(buf, pgoInsert)
	buf = appendUint32(buf, rel.OID)
	buf = append(buf, 'N')
	buf = append(buf, body...)
	_, err = p.w.Write(buf)
	return err
}

func (p *PgOutput) writeDelete(rel *RelationDef, oldTuple []byte) error {
	buf := make([]byte, 0, 64)
	buf = append(buf, pgoDelete)
	buf = appendUint32(buf, rel.OID)
	if len(oldTuple) > 0 {
		body, err := encodePgoTuple(rel.Columns, oldTuple)
		if err != nil {
			return fmt.Errorf("pgoutput: encode delete old-tuple for %q: %w", rel.Name, err)
		}
		buf = append(buf, 'O') // full old tuple
		buf = append(buf, body...)
	} else {
		// No pre-image available: emit an empty K body so the
		// wire shape is well-formed. Apply workers skip empty
		// old-tuple deletes.
		buf = append(buf, 'K')
		buf = appendUint16(buf, 0)
	}
	_, err := p.w.Write(buf)
	return err
}

func (p *PgOutput) writeUpdate(rel *RelationDef, oldTuple, newTuple []byte) error {
	// 'U' | rel_oid(4) | ['K'|'O' + OldTupleData]? | 'N' | NewTupleData
	//
	// Upstream `logicalrep_write_update` (proto.c) emits the K/O block
	// only when the old slot is non-NULL. With REPLICA IDENTITY DEFAULT
	// and no replica-identity column modified, the byte after rel_oid
	// is 'N' directly — there is NO 'K' + natts=0 placeholder. Emitting
	// one (the goopg pre-2026-05-14 behaviour) was rejected by libpq
	// subscribers as malformed since natts=0 violates the relation's
	// declared column count.
	newBody, err := encodePgoTuple(rel.Columns, newTuple)
	if err != nil {
		return fmt.Errorf("pgoutput: encode update new-tuple for %q: %w", rel.Name, err)
	}
	buf := make([]byte, 0, 6+len(newBody)+64)
	buf = append(buf, pgoUpdate)
	buf = appendUint32(buf, rel.OID)
	if len(oldTuple) > 0 {
		oldBody, err := encodePgoTuple(rel.Columns, oldTuple)
		if err != nil {
			return fmt.Errorf("pgoutput: encode update old-tuple for %q: %w", rel.Name, err)
		}
		buf = append(buf, 'O')
		buf = append(buf, oldBody...)
	}
	buf = append(buf, 'N')
	buf = append(buf, newBody...)
	_, err = p.w.Write(buf)
	return err
}

// encodePgoTuple parses a heap-tuple body and re-emits each column in
// upstream's pgoutput tuple format (`logicalrep_write_tuple`). The codec is
// intentionally duplicated rather than imported from `executor` because that
// package depends on `wal` and inverting the import direction would create a
// cycle. See docs/design/0008-0002-pgoutput-plugin.md.
//
// M0111-0002: goopg has a single on-disk heap-tuple format (PG-physical); the
// legacy [flag][value] walk was removed. Decode uses the tuple header (natts +
// null bitmap).
func encodePgoTuple(cols []ColumnDef, raw []byte) ([]byte, error) {
	tup, err := storage.ParseHeapTuple(raw)
	if err != nil {
		return nil, err
	}
	natts := int(tup.Header.Infomask2 & storage.HeapNattsMask)
	return encodePgoTuplePhysical(cols, tup.Data, tup.Bitmap, natts)
}

// encodePgoTuplePhysical walks a PG-physical body using the null bitmap and
// per-type alignment, re-emitting each column as the canonical pgoutput text
// the subscriber's apply worker parses. M0111-0002.
func encodePgoTuplePhysical(cols []ColumnDef, body, bitmap []byte, storedNatts int) ([]byte, error) {
	out := make([]byte, 0, 2+len(cols)*8)
	out = appendUint16(out, uint16(len(cols)))

	off := 0
	for i, col := range cols {
		// Columns beyond stored natts were added via ALTER TABLE ADD COLUMN.
		if i >= storedNatts {
			out = append(out, pgoColNull)
			continue
		}
		// Null bitmap: bit i = 0 means column i is NULL.
		if len(bitmap) > 0 && (bitmap[i/8]>>(uint(i)%8))&1 == 0 {
			out = append(out, pgoColNull)
			continue
		}
		off = pgoPhysicalAlign(off, col.Type)
		if off >= len(body) {
			out = append(out, pgoColNull)
			continue
		}
		val, n, derr := pgoDecodePhysicalValue(col.Type, body[off:])
		if derr != nil {
			return nil, fmt.Errorf("col %q: %w", col.Name, derr)
		}
		off += n
		out = append(out, pgoColText)
		out = appendUint32(out, uint32(len(val)))
		out = append(out, val...)
	}
	return out, nil
}

// pgoPhysicalAlign rounds off up to the PG storage alignment for t's physical
// type. Mirrors executor.alignPhysicalPGOffset + physicalPGTypeAlign. Type
// names here are the lowercase canonical catalog names (same as pgoDecodeValue).
func pgoPhysicalAlign(off int, t catalog.Type) int {
	align := 4
	// A user array column is catalog.Type{Name:<ELEMENT type>, IsArray:true} —
	// Name is the ELEMENT's name, so every arm below would claim the array and
	// impose the ELEMENT's alignment (e.g. 8 for `interval[]`). On disk an array
	// is always a varlena ArrayType blob, i.e. PG 'i' / 4-byte alignment, so the
	// IsArray test has to come first. M0119-0006.
	if t.IsArray {
		return (off + 3) &^ 3
	}
	switch t.Name {
	case "bool", "boolean", "name",
		// uuid: pg_type OID 2950 is typalign 'c', typlen 16 (M0119-0006).
		"uuid":
		align = 1
	case "char":
		if len(t.Args) == 0 {
			align = 1
		} else {
			align = 4
		}
	case "int2", "smallint":
		align = 2
	case "int8", "bigint", "bigserial", "float8", "double precision", "double",
		"timestamp", "timestamptz", "time", "timetz",
		// interval: pg_type OID 1186 is typalign 'd', typlen 16.
		"interval":
		align = 8
	}
	if align <= 1 {
		return off
	}
	mask := align - 1
	return (off + mask) &^ mask
}

// pgoDecodePhysicalValue decodes one PG-physical column value and returns its
// pgoutput text form. Mirrors executor.decodePhysicalPGValueMctx, producing the
// same text pgoDecodeValue would for the same logical value so the subscriber's
// apply worker parses it identically. M0111-0002.
func pgoDecodePhysicalValue(t catalog.Type, data []byte) ([]byte, int, error) {
	// Array columns FIRST, for the reason spelled out in pgoPhysicalAlign: Name
	// holds the ELEMENT type, so the switch below would decode a `uuid[]`
	// column's ArrayType blob as a bare 16-byte pg_uuid_t — emitting garbage to
	// the subscriber AND mis-advancing the offset of every following column in
	// the tuple. The blob is a varlena; the text rendering is the very same
	// internal/pgarray.RenderText the heap decoder
	// (executor.decodeArrayValuePG) uses, so a replicated array reaches the
	// subscriber spelled exactly as a local SELECT spells it. M0119-0006.
	if t.IsArray {
		payload, n, err := pgoDecodePhysicalVarlena(data)
		if err != nil {
			return nil, 0, err
		}
		text, err := pgarray.RenderText(t.Name, payload)
		if err != nil {
			return nil, 0, err
		}
		return []byte(text), n, nil
	}
	switch t.Name {
	case "bool", "boolean":
		if len(data) < 1 {
			return nil, 0, fmt.Errorf("bool: short read")
		}
		if data[0] != 0 {
			return []byte("t"), 1, nil
		}
		return []byte("f"), 1, nil
	case "int2", "smallint":
		if len(data) < 2 {
			return nil, 0, fmt.Errorf("int2: short read")
		}
		return []byte(strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(data[:2]))), 10)), 2, nil
	case "int4", "integer", "int", "serial":
		if len(data) < 4 {
			return nil, 0, fmt.Errorf("int4: short read")
		}
		return []byte(strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(data[:4]))), 10)), 4, nil
	case "int8", "bigint", "bigserial":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("int8: short read")
		}
		return []byte(strconv.FormatInt(int64(binary.LittleEndian.Uint64(data[:8])), 10)), 8, nil
	case "oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid", "xid":
		// Sibling of executor.encodeValuePG's oid arm: regclasssend/regtypesend/
		// regproceduresend/regprocsend/regrolesend/regcollationsend/cidsend are
		// ALL oidsend upstream — pq_sendint32 over the 4-byte OID (regproc.c,
		// oid.c). Before the M0119-0006 (reg* + cid 4-byte storage) heap change
		// these columns were varlena text and the fall-through below returned the
		// stored text; the heap image is now 4 fixed bytes, so that fall-through
		// would read a varlena header off the OID and emit garbage. Hard-won Rule
		// #2. (67th slice: regrole/regcollation joined the arm.)
		if len(data) < 4 {
			return nil, 0, fmt.Errorf("oid: short read")
		}
		return []byte(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[:4])), 10)), 4, nil
	case "xid8":
		// M0119-0006 (54th slice): xid8 rode the 4-byte oid/xid arm here too.
		// pg_type OID 5069 is typlen 8 (typalign 'd'), so this decoder read half
		// a FullTransactionId and then handed the next column a 4-byte-short
		// offset. Third twin of the heap encode/decode pair in
		// internal/executor/codec.go — Hard-won Rule #2 (encode↔decode, and every
		// decoder that walks the same physical tuple).
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("xid8: short read")
		}
		return []byte(strconv.FormatUint(binary.LittleEndian.Uint64(data[:8]), 10)), 8, nil
	case "float4", "real":
		// M0111-0002: fixed 4-byte IEEE-754 LE (was varlena text).
		if len(data) < 4 {
			return nil, 0, fmt.Errorf("float4: short read")
		}
		f := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[:4])))
		return []byte(catalog.PGFloatOut(f, 32)), 4, nil
	case "float8", "double precision", "double":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("float8: short read")
		}
		f := math.Float64frombits(binary.LittleEndian.Uint64(data[:8]))
		return []byte(catalog.PGFloatOut(f, 64)), 8, nil
	case "name":
		if len(data) < 64 {
			return nil, 0, fmt.Errorf("name: short read")
		}
		end := 64
		for i := 0; i < 64; i++ {
			if data[i] == 0 {
				end = i
				break
			}
		}
		return append([]byte(nil), data[:end]...), 64, nil
	case "timestamp", "timestamptz":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("timestamp: short read")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		ts := pgoPhysEpoch().Add(time.Duration(micros) * time.Microsecond)
		return []byte(ts.UTC().Format("2006-01-02 15:04:05.000000")), 8, nil
	case "date":
		if len(data) < 4 {
			return nil, 0, fmt.Errorf("date: short read")
		}
		days := int32(binary.LittleEndian.Uint32(data[:4]))
		ts := pgoPhysEpoch().AddDate(0, 0, int(days))
		return []byte(ts.UTC().Format("2006-01-02 15:04:05.000000")), 4, nil
	case "time":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("time: short read")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		return []byte(time.UnixMicro(micros).UTC().Format("15:04:05.000000")), 8, nil
	case "timetz":
		if len(data) < 12 {
			return nil, 0, fmt.Errorf("timetz: short read")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		return []byte(time.UnixMicro(micros).UTC().Format("15:04:05.000000")), 12, nil
	case "interval":
		// Sibling of executor.encodeValuePG's "interval" arm: PG's fixed
		// 16-byte {time int64, day int32, month int32}. Before interval columns
		// gained that layout the varlena fall-through below returned the stored
		// text and happened to be right; it would now emit 16 raw bytes read as
		// a varlena header. The text is rendered by the same
		// pgdatetime.FormatInterval the executor's Datum.Format calls, so a
		// replicated interval reaches the subscriber spelled exactly as a local
		// SELECT spells it.
		if len(data) < 16 {
			return nil, 0, fmt.Errorf("interval: short read")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		days := int32(binary.LittleEndian.Uint32(data[8:12]))
		months := int32(binary.LittleEndian.Uint32(data[12:16]))
		return []byte(pgdatetime.FormatInterval(months, days, micros)), 16, nil
	case "uuid":
		// Sibling of executor.encodeValuePG's "uuid" arm: PG's fixed 16-byte
		// pg_uuid_t. Before uuid columns gained that layout the varlena
		// fall-through below returned the stored 36-char text and happened to
		// be right; it would now read the first raw byte as a varlena header.
		// Rendered by uuid_out's rule (lowercase hex, hyphens after bytes
		// 4/6/8/10) so the subscriber sees exactly what a local SELECT prints.
		// M0119-0006.
		if len(data) < 16 {
			return nil, 0, fmt.Errorf("uuid: short read")
		}
		const hexdigits = "0123456789abcdef"
		out := make([]byte, 0, 36)
		for i := 0; i < 16; i++ {
			if i == 4 || i == 6 || i == 8 || i == 10 {
				out = append(out, '-')
			}
			out = append(out, hexdigits[data[i]>>4], hexdigits[data[i]&0x0f])
		}
		return out, 16, nil
	case "numeric", "decimal":
		// Sibling of executor.encodeValuePG's "numeric" arm: PG's base-10000
		// NumericData behind a varlena header. Before numeric columns gained
		// that layout the varlena fall-through below returned the stored
		// decimal string and happened to be right; it would now ship the raw
		// digit array to the subscriber as if it were text, WITHOUT erroring —
		// the same silent-garbage shape the uuid slice found here.
		// pgnodes.NumericTextFromStoredPayload renders numeric_out's text and
		// also accepts the pre-flip payload, so replication out of a cluster
		// that predates the flip keeps working. M0119-0006.
		payload, n, err := pgoDecodePhysicalVarlena(data)
		if err != nil {
			return nil, 0, err
		}
		s, err := pgnodes.NumericTextFromStoredPayload(payload)
		if err != nil {
			return nil, 0, err
		}
		return []byte(s), n, nil
	}
	// External on-disk TOAST pointer: logical replication of toasted values is
	// not supported in v0 — fail loudly rather than emit a garbled value.
	if len(data) >= 1 && data[0] == 0x1B {
		return nil, 0, fmt.Errorf("%s: external TOAST pointer in logical change not supported", t.Name)
	}
	// Varlena types (text / varchar / bpchar / char / numeric / decimal /
	// float4 / float8 / bytea / unknown). goopg stores numeric and float as
	// varlena text, so the payload bytes are already the canonical text.
	payload, n, err := pgoDecodePhysicalVarlena(data)
	if err != nil {
		return nil, 0, err
	}
	// A bpchar column's heap image is trimmed (executor's coerceTextLikeDatum),
	// where upstream's is blank-padded to the declared width; a real PG
	// publisher therefore emits all N characters in the change message and this
	// one emitted only the significant ones. Same catalog.PadBpchar the DataRow
	// and COPY renderers call, so the four boundaries cannot drift (Hard-won
	// Rule #2). No-op for every other varlena type. M0119-0006 (57th slice).
	return []byte(catalog.PadBpchar(t, string(payload))), n, nil
}

// pgoDecodePhysicalVarlena reads a PG varlena (short 1-byte or 4-byte header)
// and returns the payload bytes. Mirrors executor.decodePhysicalPGVarlena.
func pgoDecodePhysicalVarlena(data []byte) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("truncated varlena")
	}
	header := data[0]
	if header&0x01 == 0x01 {
		if header == 0x01 {
			return nil, 0, fmt.Errorf("external varlena not supported")
		}
		total := int(header >> 1)
		if total < 1 || total > len(data) {
			return nil, 0, fmt.Errorf("truncated short varlena")
		}
		return data[1:total], total, nil
	}
	if len(data) < 4 {
		return nil, 0, fmt.Errorf("truncated 4-byte varlena header")
	}
	if header&0x03 == 0x02 {
		// VARATT_IS_4B_C: inline PGLZ-compressed varlena. Decompress to the
		// original payload (mirrors executor.decodePhysicalPGVarlena).
		return pglz.DecodeInlineCompressed(data)
	}
	total := int(binary.LittleEndian.Uint32(data[:4]) >> 2)
	if total < 4 || total > len(data) {
		return nil, 0, fmt.Errorf("truncated 4-byte varlena")
	}
	return data[4:total], total, nil
}

// pgoPhysEpoch is the PostgreSQL epoch (2000-01-01 UTC) used to reconstruct
// absolute instants from PG-physical micros/days offsets.
func pgoPhysEpoch() time.Time {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
}

// pgoTypeOIDFor maps a v0 catalog type name to the upstream PostgreSQL type
// OID used in pgoutput Relation messages. Delegates to catalog.TypeNameToOID
// (M0030-0005) so the mapping is authoritative and in sync with pg_type.
// Unknown types fall back to 25 (text) — the apply worker still gets a string.
func pgoTypeOIDFor(name string) uint32 {
	return catalog.TypeNameToOID(name)
}

// pgoColumnTypeOID resolves the pg_type OID a Relation message must advertise
// for a column. An array column is catalog.Type{Name:<ELEMENT>, IsArray:true},
// so Name alone yields the ELEMENT OID (23 for `int4[]`) and the subscriber is
// told the column is a plain int4 while the values on the wire are array text
// like "{1,2}". Fold through the element→array map for the real OID (_int4 =
// 1007). M0119-0006.
func pgoColumnTypeOID(t catalog.Type) uint32 {
	base := pgoTypeOIDFor(t.Name)
	if !t.IsArray {
		return base
	}
	if arr := catalog.ArrayOIDForBase(base); arr != 0 {
		return arr
	}
	return base
}

// pgoTimestamp returns upstream's "Postgres epoch microseconds"
// representation of t. Postgres counts microseconds from
// 2000-01-01 UTC.
func pgoTimestamp(t time.Time) uint64 {
	pgEpoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	return uint64(t.Sub(pgEpoch).Microseconds())
}

func appendUint16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendUint32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendUint64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendCString(b []byte, s string) []byte {
	b = append(b, []byte(s)...)
	return append(b, 0)
}
