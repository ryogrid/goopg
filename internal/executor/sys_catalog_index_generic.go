package executor

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// Generic catalog-index maintenance (M0149-0001g slice 2).
//
// PostgreSQL inserts every catalog row into every index of its catalog
// (CatalogTupleInsert → CatalogIndexInsert, postgres/src/backend/catalog/
// indexing.c). goopg grew one hand-written inserter per index and some
// catalogs were never covered: an audit after a DDL workload (leaf tuples vs
// live heap rows) found pg_constraint, pg_inherits, pg_auth_members,
// pg_statistic, pg_db_role_setting, pg_statistic_ext, pg_publication_rel and
// pg_depend rows missing from their indexes. Once pg_class.relhasindex is true
// (M0149-0001d/g) a PostgreSQL 18.3 node's planner and syscache trust those
// indexes, so a missing entry is a row that PG cannot see.
//
// catalogIndexDefs lists, per catalog relation, the indexes NOT already kept
// by an explicit inserter, with each key column's position in the PG18 row
// (the order the writers build) and its fixed-width key type.
// writeHeapRowCanonical calls maintainCatalogIndexes after every successful
// write, so every writer of a registered catalog is covered.

type catKeyType int

const (
	catKeyOid  catKeyType = iota // oid_ops: 4 bytes, align 4, unsigned compare
	catKeyInt4                   // int4_ops: 4 bytes, align 4, signed compare
	catKeyInt2                   // int2_ops: 2 bytes, align 2, signed compare
	catKeyChar                   // char_ops: 1 byte, unsigned compare
	catKeyBool                   // bool_ops: 1 byte
	catKeyName                   // name_ops (C collation): 64 bytes NUL-padded, byte compare
)

type catIdxKey struct {
	pos int
	typ catKeyType
}

type catIdxDef struct {
	oid  uint32
	keys []catIdxKey
}

var catalogIndexDefs = map[uint32][]catIdxDef{
	// pg_constraint (2606): oid 0, conname 1, connamespace 2, conrelid 8,
	// contypid 9, conparentid 11.
	2606: {
		{2667, []catIdxKey{{0, catKeyOid}}},                                  // pg_constraint_oid_index
		{2665, []catIdxKey{{8, catKeyOid}, {9, catKeyOid}, {1, catKeyName}}}, // conrelid, contypid, conname
		{2666, []catIdxKey{{9, catKeyOid}}},                                  // contypid
		{2664, []catIdxKey{{1, catKeyName}, {2, catKeyOid}}},                 // conname, connamespace
		{2579, []catIdxKey{{11, catKeyOid}}},                                 // conparentid
	},
	// pg_inherits (2611): inhrelid 0, inhparent 1, inhseqno 2.
	2611: {
		{2680, []catIdxKey{{0, catKeyOid}, {2, catKeyInt4}}}, // pg_inherits_relid_seqno_index
		{2187, []catIdxKey{{1, catKeyOid}}},                  // pg_inherits_parent_index
	},
	// pg_auth_members (1261, shared): oid 0, roleid 1, member 2, grantor 3.
	1261: {
		{6303, []catIdxKey{{0, catKeyOid}}},
		{2694, []catIdxKey{{1, catKeyOid}, {2, catKeyOid}, {3, catKeyOid}}},
		{2695, []catIdxKey{{2, catKeyOid}, {1, catKeyOid}, {3, catKeyOid}}},
		{6302, []catIdxKey{{3, catKeyOid}}},
	},
	// pg_statistic (2619): starelid 0, staattnum 1, stainherit 2.
	2619: {
		{2696, []catIdxKey{{0, catKeyOid}, {1, catKeyInt2}, {2, catKeyBool}}},
	},
	// pg_db_role_setting (2964, shared): setdatabase 0, setrole 1.
	2964: {
		{2965, []catIdxKey{{0, catKeyOid}, {1, catKeyOid}}},
	},
	// pg_statistic_ext (3381): oid 0, stxrelid 1, stxname 2, stxnamespace 3.
	3381: {
		{3380, []catIdxKey{{0, catKeyOid}}},
		{3379, []catIdxKey{{1, catKeyOid}}},
		{3997, []catIdxKey{{2, catKeyName}, {3, catKeyOid}}},
	},
	// pg_publication_rel (6106): prpubid 1 (oid and prrelid/prpubid are kept
	// by their explicit inserters).
	6106: {
		{6116, []catIdxKey{{1, catKeyOid}}},
	},
	// pg_class (1259) is deliberately NOT registered. Maintaining
	// pg_class_tblspc_relfilenode_index (3455) from goopg's runtime made a
	// PG 18.3 node promoted from a goopg primary trip
	// Assert("tuplesz <= BTMaxItemSize") (nbtdedup.c) on a user-index insert in
	// TestE2E_FailoverGoopgToPG (2 of 5 runs; 0 of 10 at the previous commit and
	// 0 of 5 without this entry). Until that is understood 3455 carries only
	// its bootstrap rows, so goopg-created relations are missing from it
	// (M0149-0001g, ledger 2026-11-16).
	// pg_am (2601): oid 0, amname 1.
	2601: {
		{2652, []catIdxKey{{0, catKeyOid}}},  // pg_am_oid_index
		{2651, []catIdxKey{{1, catKeyName}}}, // pg_am_name_index
	},
	// pg_amop (2602) / pg_amproc (2603): oid 0 (the family-keyed indexes keep
	// their explicit inserters).
	2602: {{2756, []catIdxKey{{0, catKeyOid}}}},
	// pg_largeobject_metadata_oid_index (M0149-0001h1a: lo_create rows).
	2995: {{2996, []catIdxKey{{0, catKeyOid}}}},
	// pg_largeobject_loid_pn_index (M0149-0001h1a slice 2: data pages).
	2613: {{2683, []catIdxKey{{0, catKeyOid}, {1, catKeyInt4}}}},
	2603: {{2757, []catIdxKey{{0, catKeyOid}}}},
	// pg_depend (2608): classid 0, objid 1, objsubid 2, refclassid 3,
	// refobjid 4, refobjsubid 5.
	2608: {
		{2673, []catIdxKey{{0, catKeyOid}, {1, catKeyOid}, {2, catKeyInt4}}},
		{2674, []catIdxKey{{3, catKeyOid}, {4, catKeyOid}, {5, catKeyInt4}}},
	},
}

// catalogIndexDefByOID indexes catalogIndexDefs by index OID (split metadata).
var catalogIndexDefByOID = func() map[uint32]catIdxDef {
	m := map[uint32]catIdxDef{}
	for _, defs := range catalogIndexDefs {
		for _, d := range defs {
			m[d.oid] = d
		}
	}
	return m
}()

func catKeyLenAlign(t catKeyType) (int, int) {
	switch t {
	case catKeyOid, catKeyInt4:
		return 4, 4
	case catKeyInt2:
		return 2, 2
	case catKeyName:
		return 64, 1
	default: // char, bool
		return 1, 1
	}
}

// catKeyLayout returns each key column's offset within the key payload and the
// MAXALIGNed IndexTuple size, following PG's att_align_nominal rules.
func catKeyLayout(keys []catIdxKey) ([]int, int) {
	offs := make([]int, len(keys))
	off := 0
	for i, k := range keys {
		l, a := catKeyLenAlign(k.typ)
		off = (off + a - 1) / a * a
		offs[i] = off
		off += l
	}
	size := sysIndexTupleHoff + off
	size = (size + 7) / 8 * 8
	return offs, size
}

// buildCatalogIndexTuple builds the IndexTuple for one catalog row.
func buildCatalogIndexTuple(d catIdxDef, row Row, tid storage.ItemPointer) ([]byte, error) {
	offs, size := catKeyLayout(d.keys)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(tid.Block>>16))
	le.PutUint16(out[2:4], uint16(tid.Block&0xFFFF))
	le.PutUint16(out[4:6], tid.Offset)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	for i, k := range d.keys {
		if k.pos >= len(row) {
			return nil, fmt.Errorf("index %d: key column %d beyond row width %d", d.oid, k.pos, len(row))
		}
		v := row[k.pos]
		p := out[sysIndexTupleHoff+offs[i]:]
		switch k.typ {
		case catKeyOid, catKeyInt4:
			le.PutUint32(p[:4], uint32(v.Int))
		case catKeyInt2:
			le.PutUint16(p[:2], uint16(int16(v.Int)))
		case catKeyName:
			copy(p[:63], v.StringValue())
		case catKeyChar:
			if s := v.StringValue(); len(s) > 0 {
				p[0] = s[0]
			}
		case catKeyBool:
			if v.BoolValue() {
				p[0] = 1
			}
		}
	}
	return out, nil
}

// catalogIndexKeyCmp compares two key payloads column by column.
func catalogIndexKeyCmp(d catIdxDef) keyCompareFn {
	offs, _ := catKeyLayout(d.keys)
	return func(a, b []byte) int {
		for i, k := range d.keys {
			o := offs[i]
			var c int
			switch k.typ {
			case catKeyOid:
				c = cmpU32(binary.LittleEndian.Uint32(a[o:]), binary.LittleEndian.Uint32(b[o:]))
			case catKeyInt4:
				c = cmpI64(int64(int32(binary.LittleEndian.Uint32(a[o:]))), int64(int32(binary.LittleEndian.Uint32(b[o:]))))
			case catKeyInt2:
				c = cmpI64(int64(int16(binary.LittleEndian.Uint16(a[o:]))), int64(int16(binary.LittleEndian.Uint16(b[o:]))))
			case catKeyName:
				c = bytes.Compare(a[o:o+64], b[o:o+64])
			default:
				c = cmpI64(int64(a[o]), int64(b[o]))
			}
			if c != 0 {
				return c
			}
		}
		return 0
	}
}

func cmpU32(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpI64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// maintainCatalogIndexes inserts row (just written at tid into rel) into every
// registered index of its catalog, in the same database as the heap row
// (shared catalogs: DBOid 0, global/).
func maintainCatalogIndexes(ctx *Context, rel storage.RelFileNode, row Row, tid storage.ItemPointer) error {
	if rel.Fork != storage.MainFork {
		return nil
	}
	defs := catalogIndexDefs[rel.RelOid]
	for _, d := range defs {
		tup, err := buildCatalogIndexTuple(d, row, tid)
		if err != nil {
			return err
		}
		if err := insertCanonicalSysBtreeLeafInDB(ctx, rel.DBOid, d.oid, tup, catalogIndexKeyCmp(d)); err != nil {
			return fmt.Errorf("catalog index %d: %w", d.oid, err)
		}
	}
	return nil
}
