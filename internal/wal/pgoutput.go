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
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
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
		return p.writeDelete(rel)
	case ChangeUpdate:
		// Deferred: v0 executor emits UPDATE as paired
		// HeapDelete + HeapInsert. Caller treats this Kind
		// as a no-op for now.
		return nil
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
		buf = appendUint32(buf, pgoTypeOIDFor(col.Type.Name))
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

func (p *PgOutput) writeDelete(rel *RelationDef) error {
	// v0's HeapDelete record carries no pre-image; the apply
	// worker resolves the row by (rel, block, slot). Emit a
	// 0-attribute tuple body so the wire shape is well-formed
	// — `'K' | nliveatts=0`.
	buf := make([]byte, 0, 8)
	buf = append(buf, pgoDelete)
	buf = appendUint32(buf, rel.OID)
	buf = append(buf, 'K')
	buf = appendUint16(buf, 0)
	_, err := p.w.Write(buf)
	return err
}

// encodePgoTuple parses a v0 heap-tuple body (per
// internal/executor/codec.go::DecodeRow) and re-emits each
// column in upstream's pgoutput tuple format
// (`logicalrep_write_tuple`). The format is intentionally
// duplicated rather than imported from `executor` because that
// package depends on `wal` and inverting the import direction
// would create a cycle. See
// docs/design/0008-0002-pgoutput-plugin.md.
func encodePgoTuple(cols []ColumnDef, raw []byte) ([]byte, error) {
	tup, err := storage.ParseHeapTuple(raw)
	if err != nil {
		return nil, err
	}
	body := tup.Data
	out := make([]byte, 0, 2+len(cols)*8)
	out = appendUint16(out, uint16(len(cols)))

	off := 0
	for _, col := range cols {
		if off >= len(body) {
			// ALTER TABLE ADD COLUMN appended a column the
			// stored tuple predates: emit NULL for the
			// trailing column. Mirrors DecodeRow.
			out = append(out, pgoColNull)
			continue
		}
		flag := body[off]
		off++
		if flag == 1 {
			out = append(out, pgoColNull)
			continue
		}
		val, n, derr := pgoDecodeValue(col.Type, body[off:])
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

// pgoDecodeValue reads the column value bytes per v0's on-disk
// codec frame (mirror of `executor/codec.go::decodeValue`) and
// returns the canonical text representation upstream's
// pgoutput emits with `LOGICALREP_COLUMN_TEXT`.
func pgoDecodeValue(t catalog.Type, data []byte) ([]byte, int, error) {
	switch t.Name {
	case "int4", "integer", "int":
		if len(data) < 4 {
			return nil, 0, fmt.Errorf("int4: short read len=%d", len(data))
		}
		v := int32(binary.BigEndian.Uint32(data[:4]))
		return []byte(strconv.FormatInt(int64(v), 10)), 4, nil
	case "int8", "bigint":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("int8: short read len=%d", len(data))
		}
		v := int64(binary.BigEndian.Uint64(data[:8]))
		return []byte(strconv.FormatInt(v, 10)), 8, nil
	case "bool", "boolean":
		if len(data) < 1 {
			return nil, 0, fmt.Errorf("bool: short read")
		}
		if data[0] != 0 {
			return []byte("t"), 1, nil
		}
		return []byte("f"), 1, nil
	case "timestamp", "timestamptz", "date":
		if len(data) < 8 {
			return nil, 0, fmt.Errorf("timestamp: short read len=%d", len(data))
		}
		nanos := int64(binary.BigEndian.Uint64(data[:8]))
		ts := time.Unix(0, nanos).UTC().Format("2006-01-02 15:04:05.000000")
		return []byte(ts), 8, nil
	}
	// Variable-length text-like fallback (text / varchar /
	// numeric / unknown): 4-byte big-endian length + raw
	// bytes. Mirrors `executor/codec.go::encodeVarlen`.
	if len(data) < 4 {
		return nil, 0, fmt.Errorf("varlen: short header len=%d", len(data))
	}
	ln := int(binary.BigEndian.Uint32(data[:4]))
	if 4+ln > len(data) {
		return nil, 0, fmt.Errorf("varlen: truncated body len=%d want %d", len(data), 4+ln)
	}
	out := make([]byte, ln)
	copy(out, data[4:4+ln])
	return out, 4 + ln, nil
}

// pgoTypeOIDFor maps a v0 catalog type name to the upstream
// PostgreSQL type OID. Apply workers cast the wire bytes back
// to typed values via this OID, so the mapping must match
// `pg_type` for the v0 type set. Unknown types fall back to
// 25 (text) — the apply worker still gets a string.
func pgoTypeOIDFor(name string) uint32 {
	switch name {
	case "bool", "boolean":
		return 16
	case "bytea":
		return 17
	case "int8", "bigint":
		return 20
	case "int2", "smallint":
		return 21
	case "int4", "integer", "int":
		return 23
	case "text":
		return 25
	case "varchar":
		return 1043
	case "char":
		return 1042
	case "date":
		return 1082
	case "timestamp":
		return 1114
	case "timestamptz":
		return 1184
	case "numeric", "decimal":
		return 1700
	}
	return 25 // text
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
