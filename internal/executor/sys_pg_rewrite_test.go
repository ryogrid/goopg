package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgnodes"
)

// TestViewAttrIndexConstants pins the pg_attribute row-index constants
// viewColumnCanonicalType reads (attTypidRowIdx / attTypmodRowIdx /
// attCollationRowIdx) against the actual pgAttributeColumnsPG18 column names.
// A serialized view Var's vartype/varcollid MUST equal the atttypid/attcollation
// the standby stores; if a pg_attribute layout change moved a column, reading a
// stale index would silently emit a wrong (but plausible) OID that FATALs the
// standby's rule expansion. This guard fails loudly instead. (M0123-S3 2c.)
func TestViewAttrIndexConstants(t *testing.T) {
	cols := pgAttributeColumnsPG18()
	for idx, want := range map[int]string{
		attTypidRowIdx:     "atttypid",
		attTypmodRowIdx:    "atttypmod",
		attCollationRowIdx: "attcollation",
	} {
		if idx >= len(cols) || cols[idx].Name != want {
			t.Fatalf("row index %d = %q, want %q", idx, cols[idx].Name, want)
		}
	}
}

// TestViewColumnCanonicalType verifies the type resolution behind a canonical
// view Var. Plain scalar built-ins resolve to their pg_type OID / typmod /
// collation — the exact values the resolver_query_test.go golden expects
// (int4 => 23/-1/0, text => 25/-1/100) and the same buildUserPGAttributeRow
// writes into pg_attribute — while arrays and domain columns fall out of the
// supported subset so the writer degrades to SQL text (RuleIsCanonical=false).
func TestViewColumnCanonicalType(t *testing.T) {
	tbl := &catalog.Table{Schema: "public", Name: "t", OID: 16500}

	t.Run("int4", func(t *testing.T) {
		oid, typmod, coll, ok := viewColumnCanonicalType(nil, tbl,
			catalog.Column{Name: "client", Type: catalog.Type{Name: "int4"}, Ordinal: 0})
		if !ok || oid != pgnodes.OidInt4 || typmod != -1 || coll != 0 {
			t.Fatalf("int4 => (%d,%d,%d,%v) want (23,-1,0,true)", oid, typmod, coll, ok)
		}
	})
	t.Run("text", func(t *testing.T) {
		oid, typmod, coll, ok := viewColumnCanonicalType(nil, tbl,
			catalog.Column{Name: "src", Type: catalog.Type{Name: "text"}, Ordinal: 1})
		if !ok || oid != pgnodes.OidText || typmod != -1 || coll != pgnodes.DefaultCollationOid {
			t.Fatalf("text => (%d,%d,%d,%v) want (25,-1,100,true)", oid, typmod, coll, ok)
		}
	})
	t.Run("array_unsupported", func(t *testing.T) {
		if _, _, _, ok := viewColumnCanonicalType(nil, tbl,
			catalog.Column{Name: "tags", Type: catalog.Type{Name: "text", IsArray: true}, Ordinal: 2}); ok {
			t.Fatal("text[] column must be unsupported (falls back to SQL text)")
		}
	})
	t.Run("domain_unsupported", func(t *testing.T) {
		if _, _, _, ok := viewColumnCanonicalType(nil, tbl,
			catalog.Column{Name: "d", Type: catalog.Type{Name: "int4"}, DeclaredTypeName: "mydom", Ordinal: 3}); ok {
			t.Fatal("domain column must be unsupported (falls back to SQL text)")
		}
	})
}
