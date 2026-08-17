// pgoutput message decoder for the M0008 apply worker.
//
// Inverse of `internal/wal/pgoutput.go::PgOutput`'s encoder. Reads
// one pgoutput v1 message off a byte slice and returns a typed
// event the apply worker can dispatch on. See
// docs/design/0008-0004-apply-worker-and-tablesync.md.
//
// The byte shapes mirror upstream PG's `logicalrep_read_*` family
// in postgres/src/backend/replication/logical/proto.c.

package xlog

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ErrTruncatedMessage is returned when a pgoutput payload ends
// before the per-kind shape's required bytes are present.
var ErrTruncatedMessage = errors.New("pgoutput: truncated message")

// DecodedMessage is one parsed pgoutput v1 wire message. Kind
// discriminates which fields are populated.
type DecodedMessage struct {
	Kind byte

	// Begin / Commit fields.
	XID         storage.TransactionID
	CommitLSN   uint64
	EndLSN      uint64
	CommitTime  uint64

	// Relation field — populated for kind == 'R'.
	Relation *DecodedRelation

	// Insert / Delete fields.
	RelOID   uint32
	NewTuple []DecodedColumn // populated for 'I'
	OldTuple []DecodedColumn // populated for 'D' / 'U'

	// Truncate fields — populated for kind == 'T'. TruncateRels
	// holds the publisher OIDs of every relation in the TRUNCATE
	// statement (a single TRUNCATE may target many tables);
	// TruncateOption is the upstream option-bit byte (bit 0
	// CASCADE, bit 1 RESTART IDENTITY) — the apply worker uses
	// this to mirror upstream's `apply_handle_truncate` policy.
	TruncateRels   []uint32
	TruncateOption byte
}

// DecodedRelation is the parsed `R` message body.
type DecodedRelation struct {
	OID       uint32
	Schema    string
	Name      string
	Replident byte
	Columns   []DecodedAttr
}

// DecodedAttr is one per-column entry in a Relation message.
type DecodedAttr struct {
	Flags   byte
	Name    string
	TypeOID uint32
	TypeMod int32
}

// DecodedColumn is one tuple-body column entry. Status is one
// of pgoColNull ('n'), pgoColText ('t'), or upstream's
// "unchanged TOAST" ('u'). Bytes is empty for 'n' / 'u'.
type DecodedColumn struct {
	Status byte
	Bytes  []byte
}

// DecodeMessage parses one pgoutput message off `payload`. The
// kind byte at `payload[0]` selects the per-kind parser. Errors
// are returned for short payloads, unknown kinds, or
// out-of-bounds field reads.
func DecodeMessage(payload []byte) (*DecodedMessage, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("%w: empty payload", ErrTruncatedMessage)
	}
	kind := payload[0]
	r := &reader{buf: payload[1:]}
	out := &DecodedMessage{Kind: kind}
	switch kind {
	case pgoBegin:
		// kind | final_lsn(8) | commit_time(8) | xid(4) = 21
		lsn, err := r.u64()
		if err != nil {
			return nil, err
		}
		ts, err := r.u64()
		if err != nil {
			return nil, err
		}
		xid, err := r.u32()
		if err != nil {
			return nil, err
		}
		out.CommitLSN = lsn
		out.CommitTime = ts
		out.XID = storage.TransactionID(xid)
		return out, nil
	case pgoCommit:
		// kind | flags(1) | commit_lsn(8) | end_lsn(8) | commit_time(8) = 26
		if _, err := r.u8(); err != nil {
			return nil, err
		}
		commitLSN, err := r.u64()
		if err != nil {
			return nil, err
		}
		endLSN, err := r.u64()
		if err != nil {
			return nil, err
		}
		ts, err := r.u64()
		if err != nil {
			return nil, err
		}
		out.CommitLSN = commitLSN
		out.EndLSN = endLSN
		out.CommitTime = ts
		return out, nil
	case pgoRelation:
		rel, err := decodeRelation(r)
		if err != nil {
			return nil, err
		}
		out.Relation = rel
		out.RelOID = rel.OID
		return out, nil
	case pgoInsert:
		oid, err := r.u32()
		if err != nil {
			return nil, err
		}
		action, err := r.u8()
		if err != nil {
			return nil, err
		}
		if action != 'N' {
			return nil, fmt.Errorf("pgoutput: insert action=%q want N", action)
		}
		cols, err := decodeTupleBody(r)
		if err != nil {
			return nil, err
		}
		out.RelOID = oid
		out.NewTuple = cols
		return out, nil
	case pgoDelete:
		oid, err := r.u32()
		if err != nil {
			return nil, err
		}
		action, err := r.u8()
		if err != nil {
			return nil, err
		}
		if action != 'K' && action != 'O' {
			return nil, fmt.Errorf("pgoutput: delete action=%q want K or O", action)
		}
		cols, err := decodeTupleBody(r)
		if err != nil {
			return nil, err
		}
		out.RelOID = oid
		out.OldTuple = cols
		return out, nil
	case pgoUpdate:
		// 'U' | rel_oid(4) | ['K'|'O' + OldTupleData]? | 'N' | NewTupleData
		//
		// Upstream omits the old-tuple section entirely when the
		// row's REPLICA IDENTITY is DEFAULT and no replica-identity
		// columns were changed by the update (logicalrep_write_update
		// in proto.c: writes 'O' only for REPLICA IDENTITY FULL, 'K'
		// only when key columns were modified, neither otherwise).
		// In that case the byte after rel_oid is 'N' directly.
		oid, err := r.u32()
		if err != nil {
			return nil, err
		}
		marker, err := r.u8()
		if err != nil {
			return nil, err
		}
		var oldCols []DecodedColumn
		if marker == 'K' || marker == 'O' {
			oldCols, err = decodeTupleBody(r)
			if err != nil {
				return nil, err
			}
			marker, err = r.u8()
			if err != nil {
				return nil, err
			}
		}
		if marker != 'N' {
			return nil, fmt.Errorf("pgoutput: update new-tuple type=%q want N (after optional K/O)", marker)
		}
		newCols, err := decodeTupleBody(r)
		if err != nil {
			return nil, err
		}
		out.RelOID = oid
		out.OldTuple = oldCols
		out.NewTuple = newCols
		return out, nil
	case pgoTruncate:
		// 'T' | nrelids(4) | option_bits(1) | relids(4*nrelids)
		// See upstream `logicalrep_write_truncate` in proto.c.
		// nrelids must be > 0 in well-formed messages (the publisher
		// only emits 'T' when at least one published relation is
		// affected) but the apply worker tolerates an empty list as
		// a no-op rather than rejecting the frame.
		nrelids, err := r.u32()
		if err != nil {
			return nil, err
		}
		option, err := r.u8()
		if err != nil {
			return nil, err
		}
		rels := make([]uint32, 0, nrelids)
		for i := uint32(0); i < nrelids; i++ {
			rel, err := r.u32()
			if err != nil {
				return nil, err
			}
			rels = append(rels, rel)
		}
		out.TruncateRels = rels
		out.TruncateOption = option
		return out, nil
	}
	return nil, fmt.Errorf("pgoutput: unknown message kind %q", kind)
}

func decodeRelation(r *reader) (*DecodedRelation, error) {
	oid, err := r.u32()
	if err != nil {
		return nil, err
	}
	schema, err := r.cstring()
	if err != nil {
		return nil, err
	}
	name, err := r.cstring()
	if err != nil {
		return nil, err
	}
	replident, err := r.u8()
	if err != nil {
		return nil, err
	}
	natts, err := r.u16()
	if err != nil {
		return nil, err
	}
	cols := make([]DecodedAttr, natts)
	for i := range cols {
		flags, err := r.u8()
		if err != nil {
			return nil, err
		}
		colName, err := r.cstring()
		if err != nil {
			return nil, err
		}
		typOID, err := r.u32()
		if err != nil {
			return nil, err
		}
		typMod, err := r.u32()
		if err != nil {
			return nil, err
		}
		cols[i] = DecodedAttr{
			Flags:   flags,
			Name:    colName,
			TypeOID: typOID,
			TypeMod: int32(typMod),
		}
	}
	return &DecodedRelation{
		OID:       oid,
		Schema:    schema,
		Name:      name,
		Replident: replident,
		Columns:   cols,
	}, nil
}

func decodeTupleBody(r *reader) ([]DecodedColumn, error) {
	natts, err := r.u16()
	if err != nil {
		return nil, err
	}
	cols := make([]DecodedColumn, natts)
	for i := range cols {
		status, err := r.u8()
		if err != nil {
			return nil, err
		}
		switch status {
		case pgoColNull, 'u':
			cols[i] = DecodedColumn{Status: status}
		case pgoColText:
			ln, err := r.u32()
			if err != nil {
				return nil, err
			}
			body, err := r.bytes(int(ln))
			if err != nil {
				return nil, err
			}
			cols[i] = DecodedColumn{Status: status, Bytes: body}
		default:
			return nil, fmt.Errorf("pgoutput: unknown column status %q", status)
		}
	}
	return cols, nil
}

// reader is a tiny big-endian field cursor over a byte slice.
type reader struct {
	buf []byte
	off int
}

func (r *reader) need(n int) error {
	if r.off+n > len(r.buf) {
		return fmt.Errorf("%w: need %d bytes at offset %d, have %d", ErrTruncatedMessage, n, r.off, len(r.buf)-r.off)
	}
	return nil
}

func (r *reader) u8() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	b := r.buf[r.off]
	r.off++
	return b, nil
}

func (r *reader) u16() (uint16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint16(r.buf[r.off:])
	r.off += 2
	return v, nil
}

func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint32(r.buf[r.off:])
	r.off += 4
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint64(r.buf[r.off:])
	r.off += 8
	return v, nil
}

func (r *reader) bytes(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, r.buf[r.off:r.off+n])
	r.off += n
	return out, nil
}

func (r *reader) cstring() (string, error) {
	for i := r.off; i < len(r.buf); i++ {
		if r.buf[i] == 0 {
			out := string(r.buf[r.off:i])
			r.off = i + 1
			return out, nil
		}
	}
	return "", fmt.Errorf("%w: unterminated C string at offset %d", ErrTruncatedMessage, r.off)
}
