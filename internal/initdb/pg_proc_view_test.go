package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgProcViewEmptyByDefault: registering the view against a
// fresh catalog gives a non-nil but empty rowset. Pins the
// view-exists-but-zero-rows contract every other pg_catalog.* view
// in goopg honours.
func TestPgProcViewEmptyByDefault(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	if !ok {
		t.Fatal("pg_proc not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("rows = %d, want 0 (no routines registered)", len(rows))
	}
}

// TestPgProcViewRendersRoutine pins the column shape and value
// mapping for a registered routine. Two-arg function with a
// non-empty body — exercises every column.
func TestPgProcViewRendersRoutine(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	_, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "add",
		ArgTypes:   []catalog.Type{{Name: "int"}, {Name: "int"}},
		ReturnType: catalog.Type{Name: "int"},
		Language:   "plpgsql",
		Body:       "BEGIN RETURN $1 + $2; END",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	// Columns: oid, proname, pronamespace, prolang, prorettype,
	//          proargtypes, prosrc.
	if row[1] != "add" {
		t.Errorf("proname = %q, want add", row[1])
	}
	if row[2] != "public" {
		t.Errorf("pronamespace = %q, want public", row[2])
	}
	if row[3] != "plpgsql" {
		t.Errorf("prolang = %q, want plpgsql", row[3])
	}
	if row[4] != "int" {
		t.Errorf("prorettype = %q, want int", row[4])
	}
	if row[5] != "int,int" {
		t.Errorf("proargtypes = %q, want int,int", row[5])
	}
	if row[6] != "BEGIN RETURN $1 + $2; END" {
		t.Errorf("prosrc = %q", row[6])
	}
	if row[0] == "" || row[0] == "0" {
		t.Errorf("oid = %q, want non-zero text", row[0])
	}
}

// TestPgProcViewOrdering pins that the row order matches OID
// ordering — an operator's `ORDER BY oid` is a no-op against this
// view's natural order, which makes diff-based regression tests
// against `\df` output stable.
func TestPgProcViewOrdering(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"zeta", "alpha", "mu"} {
		if _, err := cat.Routines().Create(&catalog.Routine{
			Schema:     "public",
			Name:       n,
			ReturnType: catalog.Type{Name: "int"},
			Language:   "plpgsql",
			Body:       "BEGIN END",
		}, false); err != nil {
			t.Fatal(err)
		}
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Insertion order: zeta, alpha, mu — OIDs assigned in that
	// order — view rows should be [zeta, alpha, mu].
	wantNames := []string{"zeta", "alpha", "mu"}
	for i, want := range wantNames {
		if rows[i][1] != want {
			t.Errorf("row %d proname = %q, want %q", i, rows[i][1], want)
		}
	}
}
