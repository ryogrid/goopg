package catalog

import "strings"

// PhysicalTypeAlign returns the PG storage alignment (typalign, expressed in
// bytes) of a column's physical on-disk representation. tname must be the
// lowercased type name; PhysicalTypeAlignName does that for callers that do not
// already hold it.
//
// This is the single source of truth for physical alignment. It used to be
// duplicated: the executor's decoder owned the full table while
// xlog/pgoutput.go carried a hand-copied subset that had drifted (it was
// missing pg_lsn, xid8, serial2/smallserial, serial8 and anyarray), so a
// logical-replication tuple containing any of those columns was decoded one or
// more bytes off — corrupting that column AND every column after it on the
// wire. Both callers now share this function so the two paths cannot drift
// again (review/260831-2, new finding XL-5).
func PhysicalTypeAlign(t Type, tname string) int {
	// All array columns store a varlena ArrayType blob → PG 'i' (4-byte) align.
	// M0118-0002.
	if t.IsArray {
		return 4
	}
	switch tname {
	case "bool", "boolean":
		return 1
	case "char":
		// Single-byte internal "char" type: alignment 1.
		// char(N) with length modifier is bpchar (varlena): alignment 4.
		if len(t.Args) == 0 {
			return 1
		}
		return 4
	case "int2", "smallint", "smallserial", "serial2":
		return 2
	case "int4", "integer", "int", "serial", "serial4", "oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid", "float4", "real", "date", "xid":
		return 4
	case "int8", "bigint", "bigserial", "serial8", "pg_lsn", "float8", "double precision", "double", "timestamp", "timestamptz", "time", "timetz",
		// interval is typalign 'd' (pg_type OID 1186) even though its 16 bytes
		// exceed a Datum — the struct's leading field is an int64.
		"interval",
		// xid8 is typalign 'd' (pg_type OID 5069, typlen 8) — it had been
		// falling through to the default 4 while its 4-byte encode hid the
		// consequence. M0119-0006 (54th slice); `xid` (OID 28) stays 'i' above.
		"xid8":
		return 8
	case "name",
		// uuid is pg_type OID 2950: typlen 16, typalign 'c'. Its 16 bytes
		// exceed a Datum but carry no field wider than a byte. M0119-0006.
		"uuid":
		return 1 // PG 'c' alignment (fixed-size, 1-byte aligned)
	case "aclitem[]", "_aclitem", "text[]", "_text", "oid[]", "_oid", "int2[]", "_int2", "char[]", "_char", "float4[]", "_float4", "pg_node_tree", "oidvector", "int2vector":
		return 4 // PG 'i' alignment for varlena ArrayType / pg_node_tree / oidvector / int2vector
	case "anyarray":
		// anyarray (OID 2277) is typalign='d' — 8 bytes, NOT the 'i' every
		// other varlena array uses (postgres/src/include/catalog/pg_type.dat:573).
		// Its two catalog users are pg_attribute.attmissingval and
		// pg_statistic.stavalues1..5; a hosted PG deforms both with its own
		// compiled descriptor, so 4-byte padding here put every following
		// byte one word early. Sibling of initdb.pgTypeAlignChar(2277), which
		// declares the same 'd' in the nailed self-description. M0131-S14.2.
		return 8
	default:
		return 4
	}
}

// PhysicalTypeIsVarlena reports whether the PG18 on-disk representation
// for t uses the varlena (variable-length) layout — i.e. PG's TupleDesc
// would store attlen == -1 for the column.
//
// Single source of truth (moved from executor, D-09): the heap codec,
// pgoutput's walker, and the catalog statistic paths must agree on which
// columns are varlena, or one path's alignment walk desynchronises from
// another's bytes. Mirrors the default branch of the executor's
// encodeValuePG: text, varchar, bpchar, numeric, unknown, and all varlena
// arrays / oidvector / int2vector / pg_node_tree fall through to varlena.
//
// Approximation, stated: default-true misclassifies a few fixed types
// (`tid` typlen 6, `money`, `macaddr`/`macaddr8`) as varlena. Harmless
// everywhere it is consumed: their storage is 'p' so the D-09 pack rule
// never fires, and the decode peek is offset-sound regardless (pad zeros
// align; aligned offsets noop). Tightening the list is a catalog-table
// edit, not a codec change.
func PhysicalTypeIsVarlena(t Type) bool {
	switch strings.ToLower(t.Name) {
	case "char":
		// Single-byte internal "char": fixed-length (not varlena).
		// char(N) with length modifier = bpchar (varlena).
		return len(t.Args) > 0
	case "bool", "boolean",
		"int2", "smallint", "smallserial", "serial2",
		"int4", "integer", "int", "serial", "serial4",
		"int8", "bigint", "bigserial", "serial8",
		"pg_lsn",
		"oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid",
		"timestamp", "timestamptz", "date", "time", "timetz",
		"interval", // typlen 16, not varlena
		"uuid",     // typlen 16, typalign 'c', typstorage 'p' — not varlena
		"name",
		"float4", "real",
		"float8", "double precision", "double",
		"xid", "xid8":
		return false
	default:
		return true
	}
}

// AttAlignPointer is PG's `att_align_pointer` peeking rule for the heap
// path (D-09): the peek decides only WHETHER to align, never the header
// form (REVIEW M-pg-1). A non-zero byte is a 1-byte length word or the
// first byte of an already correctly aligned multi-byte word — either
// way no alignment is needed (`tupmacs.h:104-112`). A zero byte is pad
// (or a 4-byte word's low byte at an unaligned cursor) — align. Shared
// by the executor's heap codec and pgoutput's walker so the two paths
// cannot disagree about where a column starts (same bug class as the
// PhysicalTypeAlign drift above). OOB cursor returns unchanged; the
// caller owns bounds handling.
func AttAlignPointer(data []byte, off, align int, isVarlena bool) int {
	if off < 0 || off >= len(data) {
		return off
	}
	if !isVarlena {
		return alignPhysical(off, align)
	}
	if data[off] != 0 {
		return off
	}
	return alignPhysical(off, align)
}

func alignPhysical(off, align int) int {
	if align <= 1 {
		return off
	}
	mask := align - 1
	return (off + mask) &^ mask
}

// PhysicalTypeAlignName is PhysicalTypeAlign for callers that do not already
// hold the lowercased type name.
func PhysicalTypeAlignName(t Type) int {
	return PhysicalTypeAlign(t, strings.ToLower(t.Name))
}
