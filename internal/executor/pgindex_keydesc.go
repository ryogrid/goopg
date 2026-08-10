package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2b — the catalog → key-descriptor mapper.
//
// Slice 3b-1 defined `btree.PGIndexKeyDesc` (the physical layout of each key
// attribute plus the three ordering properties `_bt_compare` consults) and
// 3b-2a supplied the opclass comparators. Neither could build a descriptor:
// `internal/access/btree` does not import `internal/catalog` on purpose — the
// on-disk layer must not depend on the type layer — so the mapper has to live
// on this side of that line. This file is upstream's `_bt_mkscankey` minus
// everything that belongs to a scan: it reads what `pg_index` records about an
// index (indkey → the key columns' types, indoption → DESC / NULLS FIRST,
// indclass → the opclass, indcollation → the collation) and hands back the
// descriptor the codec and comparator take.
//
// It is deliberately CONSERVATIVE: an index whose keys this layer cannot order
// exactly the way PostgreSQL does yields an ERROR, never a descriptor with a
// nil comparator. A nil `Compare` means "bytewise" (`CompareKeys`), which is
// correct only while the on-page key is one of goopg's order-preserving
// encodings; once the writer stores real datums (the flip that now lands with
// 3b-2c), bytewise is silently WRONG for nearly every type — a mis-ordered
// index, not a failed query. Erroring here keeps that failure mode
// unreachable: a caller that cannot get a descriptor keeps the pre-S11.4 path
// instead of writing a corrupt tree.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// pgIndexComparatorForOID returns the btree opclass support-function-1
// (BTORDER_PROC) for a built-in type OID, over the binary image
// DeformPGIndexTuple hands back. It returns nil for every type whose
// comparator 3b-2a did not implement (arrays, enum, bit/varbit, inet/cidr/
// macaddr, interval, money, tsvector/tsquery, json/jsonb, range/multirange) —
// callers must treat nil as "this index cannot be described", not as
// "bytewise".
//
// date/time/timestamp/timestamptz reuse the int4/int8 comparators, exactly as
// upstream does (pg_amproc.dat maps date_cmp/time_cmp/timestamp_cmp onto the
// same integer comparison of their by-value representations).
func pgIndexComparatorForOID(oid uint32) btree.PGAttrComparator {
	switch oid {
	case catalog.OIDBool:
		return btree.PGCompareBool
	case catalog.OIDChar:
		return btree.PGCompareCharType
	case catalog.OIDName:
		return btree.PGCompareName
	case catalog.OIDInt2:
		return btree.PGCompareInt2
	case catalog.OIDInt4, catalog.OIDDate:
		return btree.PGCompareInt4
	case catalog.OIDInt8, catalog.OIDTime, catalog.OIDTimestamp, catalog.OIDTimestampTZ:
		return btree.PGCompareInt8
	case catalog.OIDOID:
		return btree.PGCompareOID
	case catalog.OIDFloat4:
		return btree.PGCompareFloat4
	case catalog.OIDFloat8:
		return btree.PGCompareFloat8
	case catalog.OIDBytea:
		return btree.PGCompareBytea
	case catalog.OIDText, catalog.OIDVarChar:
		return btree.PGCompareTextC
	case catalog.OIDBpChar:
		return btree.PGCompareBpcharC
	case catalog.OIDNumeric:
		return btree.PGCompareNumeric
	case catalog.OIDUUID:
		return btree.PGCompareUUID
	case catalog.OIDTimeTZ:
		return btree.PGCompareTimeTZ
	}
	return nil
}

// pgIndexKeyTypeOID resolves a key column's declared type to the built-in type
// OID whose opclass orders it.
//
// It deliberately does NOT reuse buildUserPGAttributeRow's resolution, which
// falls back to `text` for any name it does not know (enum, composite, range,
// user type). That fallback is right for pg_attribute — a wrong-typed row a PG
// standby can still parse beats an unparseable one — and catastrophic here,
// because it would hand an enum column the text comparator while goopg encodes
// enums by sort order. Only names this switch lists resolve; everything else
// reports false and the caller errors.
//
// A DOMAIN column needs no special case: catalog.ResolveColumnType already
// rewrote Type.Name to the base type (the domain name survives in
// DeclaredTypeName), and a domain stores and orders values exactly as its base.
func pgIndexKeyTypeOID(col *catalog.Column) (uint32, bool) {
	if col.Type.IsArray {
		// Type.Name is the ELEMENT name for an array column (see the
		// `int4[]` == Type{Name:"int4",IsArray:true} convention), so without
		// this guard an int4[] key would silently take the int4 comparator.
		return 0, false
	}
	switch strings.ToLower(col.Type.Name) {
	case "bool", "boolean":
		return catalog.OIDBool, true
	case "bytea":
		return catalog.OIDBytea, true
	case "name":
		return catalog.OIDName, true
	case "int2", "smallint", "smallserial", "serial2":
		return catalog.OIDInt2, true
	case "int4", "int", "integer", "serial", "serial4":
		return catalog.OIDInt4, true
	case "int8", "bigint", "bigserial", "serial8":
		return catalog.OIDInt8, true
	case "oid":
		return catalog.OIDOID, true
	case "float4", "real":
		return catalog.OIDFloat4, true
	case "float8", "double precision":
		return catalog.OIDFloat8, true
	case "text":
		return catalog.OIDText, true
	case "varchar", "character varying":
		return catalog.OIDVarChar, true
	case "char", "character", "bpchar":
		// The single-byte internal "char" (OID 18) and bpchar (1042) share the
		// spelling: the parser forces a length arg on the bpchar-equivalent
		// unquoted `char`, so an argument-less `char` is the quoted, 1-byte
		// catalog type. Same discriminator buildUserPGAttributeRow uses.
		if strings.EqualFold(col.Type.Name, "char") && len(col.Type.Args) == 0 {
			return catalog.OIDChar, true
		}
		return catalog.OIDBpChar, true
	case "numeric", "decimal":
		return catalog.OIDNumeric, true
	case "uuid":
		return catalog.OIDUUID, true
	case "date":
		return catalog.OIDDate, true
	case "time", "time without time zone":
		return catalog.OIDTime, true
	case "timetz", "time with time zone":
		return catalog.OIDTimeTZ, true
	case "timestamp", "timestamp without time zone":
		return catalog.OIDTimestamp, true
	case "timestamptz", "timestamp with time zone":
		return catalog.OIDTimestampTZ, true
	}
	return 0, false
}

// pgIndexCollationOrderable reports whether a per-column collation name orders
// bytewise, which is the only ordering the 3b-2a string comparators implement.
//
// An empty name is the column's default collation. goopg compares strings
// bytewise everywhere today (CompareKeys is bytes.Compare, and so is every
// order-preserving text key encoding), so the default IS C-equivalent in this
// engine; accepting it keeps the descriptor usable for the indexes that
// actually exist (TPC-H's char/varchar keys) instead of rejecting all of them.
// Real non-C collation support is a standing deferral — see the S11.4 ledger
// rows.
func pgIndexCollationOrderable(name string) bool {
	switch strings.ToLower(strings.Trim(name, `"`)) {
	case "", "c", "posix", "ucs_basic":
		return true
	}
	return false
}

// pgIndexKeyImageIsPGFaithful reports whether goopg's stored image for a type
// IS PostgreSQL's — the precondition the 3b-2a comparators were written under
// and the one the descriptor silently assumes.
//
// Slice 3b-2c-ii-B2-a's per-type ordering table (pgindex_tuplekey_test.go)
// found TWO types failing it — numeric and uuid. uuid was fixed by the
// M0119-0006 uuid-column storage slice (encodeValuePG now writes PG's 16-byte
// pg_uuid_t, so `PGCompareUUID` gets exactly the image it was written for and
// `DeformPGIndexTuple`'s attlen-16 window lands on the whole value); the
// pre-flip comment recorded that the descriptor would otherwise have handed
// the comparator a 16-byte window onto a 37-byte text varlena, i.e. the first
// 15 characters of the UUID's text.
//
// One type still fails it:
//
//   - numeric: `encodeValuePG` stores the DECIMAL STRING as a text varlena, not
//     PG's base-10000 NumericData (weight/sign/dscale + NBASE digits). Fed that,
//     `PGCompareNumeric` cannot decode a numeric header and falls back to
//     bytes.Compare over ASCII, which orders "-1000" above "0" and "0.5" above
//     "1" — a mis-ORDERED index, exactly the failure mode this mapper exists to
//     make unreachable.
//
// It is a heap-side divergence (a real PG 18.3 reading a goopg user table with
// a numeric column already misreads it), not an index-side one, so the fix
// belongs to `encodeValuePG` and is a codec change with its own gate list —
// the same shape the uuid flip took. Until then a numeric index keeps the
// pre-S11.4 blob key path, which orders it correctly because goopg's blob
// encoding is order-preserving over its own image. See the deferral ledger row
// for M0130-S11.4 B2-a.
func pgIndexKeyImageIsPGFaithful(typOID uint32) bool {
	switch typOID {
	case catalog.OIDNumeric:
		return false
	}
	return true
}

// findColumnByName resolves an index key column name against its table,
// case-insensitively (the catalog stores the declared spelling; pg_index's
// indkey is resolved by name here). It returns a pointer INTO idx.Table.Columns
// so the descriptor and the key encoder both read one catalog.Column — the
// sibling-pair hazard this file's header describes applies to the column
// resolution itself, not just to the bytes.
func findColumnByName(tbl *catalog.Table, name string) *catalog.Column {
	if tbl == nil {
		return nil
	}
	for j := range tbl.Columns {
		if strings.EqualFold(tbl.Columns[j].Name, name) {
			return &tbl.Columns[j]
		}
	}
	return nil
}

// buildPGIndexKeyDesc builds the `btree.PGIndexKeyDesc` for a catalog index:
// one PGKeyAttr per KEY column (INCLUDE columns are non-key and excluded, as
// IndexRelationGetNumberOfKeyAttributes does), carrying the physical layout
// FormPGIndexTuple/DeformPGIndexTuple consult and the ordering properties
// ComparePGIndexTuples consults.
//
// It errors rather than degrading whenever anything about the index falls
// outside what this layer can order exactly like PostgreSQL: a non-btree
// access method, an expression key, a key column missing from the table, an
// explicit operator class (goopg has no pg_opclass registry to resolve one
// against), a non-bytewise collation, or a type with no 3b-2a comparator.
func buildPGIndexKeyDesc(idx *catalog.Index) (*btree.PGIndexKeyDesc, error) {
	if idx == nil {
		return nil, fmt.Errorf("btree key descriptor: nil index")
	}
	if idx.Method != "" && !strings.EqualFold(idx.Method, "btree") {
		return nil, fmt.Errorf("btree key descriptor: index %q uses access method %q, not btree", idx.Name, idx.Method)
	}
	if idx.Table == nil {
		return nil, fmt.Errorf("btree key descriptor: index %q has no table", idx.Name)
	}
	if len(idx.Columns) == 0 {
		return nil, fmt.Errorf("btree key descriptor: index %q has no key columns", idx.Name)
	}
	attrs := make([]btree.PGKeyAttr, 0, len(idx.Columns))
	for i, name := range idx.Columns {
		if name == "" {
			// Columns[i]=="" marks an expression key (ColExprs[i]); its output
			// type is not recorded in the catalog, so nothing here can name a
			// comparator for it.
			return nil, fmt.Errorf("btree key descriptor: index %q key %d is an expression", idx.Name, i+1)
		}
		col := findColumnByName(idx.Table, name)
		if col == nil {
			return nil, fmt.Errorf("btree key descriptor: index %q key column %q not found on table %q", idx.Name, name, idx.Table.Name)
		}
		// pg_index.indclass: goopg records only the SPELLING of an explicit
		// operator class, with no registry mapping it to a comparator, so an
		// explicit one cannot be honoured — and honouring the type default
		// instead would silently mis-order e.g. text_pattern_ops.
		if i < len(idx.ColOpClasses) && idx.ColOpClasses[i] != "" {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q uses operator class %q", idx.Name, name, idx.ColOpClasses[i])
		}
		if i < len(idx.ColCollations) && !pgIndexCollationOrderable(idx.ColCollations[i]) {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q uses collation %q, which does not order bytewise", idx.Name, name, idx.ColCollations[i])
		}
		typOID, ok := pgIndexKeyTypeOID(col)
		if !ok {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q has type %q, which has no btree comparator", idx.Name, name, col.Type.Name)
		}
		if !pgIndexKeyImageIsPGFaithful(typOID) {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q has type %q, whose goopg stored image is not PostgreSQL's", idx.Name, name, col.Type.Name)
		}
		cmp := pgIndexComparatorForOID(typOID)
		if cmp == nil {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q (type oid %d) has no btree comparator", idx.Name, name, typOID)
		}
		ta := userTypeAttrsForOID(typOID)
		alignBy, err := btree.PGAlignBy(ta.TypAlign)
		if err != nil {
			return nil, fmt.Errorf("btree key descriptor: index %q key %q: %w", idx.Name, name, err)
		}
		// ColDescending/ColNullsFirst mirror pg_index.indoption and are EMPTY
		// when every key column is the default ASC NULLS LAST, so every read
		// must be bounds-checked. The two bits are carried across
		// independently (the parser already resolved a bare ASC/DESC to its
		// default NULLS ordering) rather than deriving one from the other —
		// `... DESC NULLS LAST` is legal and sets only Desc.
		desc := i < len(idx.ColDescending) && idx.ColDescending[i]
		nullsFirst := i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i]
		attrs = append(attrs, btree.PGKeyAttr{
			PGIndexAttr: btree.PGIndexAttr{
				Len:     ta.TypLen,
				ByVal:   ta.TypByVal,
				AlignBy: alignBy,
				Storage: ta.TypStorage,
			},
			Compare:    cmp,
			Desc:       desc,
			NullsFirst: nullsFirst,
		})
	}
	return &btree.PGIndexKeyDesc{Attrs: attrs}, nil
}
