package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestBuiltinCatalogViewsServeSeed pins pg_cast / pg_opfamily / pg_opclass /
// pg_amop / pg_amproc to the PG 18.3 initdb rows (M0149-0001h1a20): each
// view used to hold only user-created rows and was empty on a fresh
// cluster, so opr_sanity's cross-checks and psql's \dA family saw nothing.
func TestBuiltinCatalogViewsServeSeed(t *testing.T) {
	c := NewInMemory()
	for name, want := range map[string]int{
		"pg_cast": len(seedPgCastRows), "pg_opfamily": len(seedPgOpfamilyRows),
		"pg_opclass": len(seedPgOpclassRows), "pg_amop": len(seedPgAmopRows),
		"pg_amproc": len(seedPgAmprocRows),
	} {
		tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: name})
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		rows := tbl.VirtualRows()
		if len(rows) != want || want == 0 {
			t.Errorf("%s rows = %d, want %d (non-zero)", name, len(rows), want)
		}
	}
	// btree int4_ops (1978) is the default opclass for int4 in family 1976.
	tbl, _ := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_opclass"})
	var found bool
	for _, r := range tbl.VirtualRows() {
		if r[0] == "1978" {
			found = true
			if r[1] != "403" || r[2] != "int4_ops" || r[5] != "1976" || r[6] != "23" || r[7] != "t" {
				t.Errorf("int4_ops row = %v", r)
			}
		}
	}
	if !found {
		t.Error("opclass 1978 (int4_ops) missing")
	}
}
