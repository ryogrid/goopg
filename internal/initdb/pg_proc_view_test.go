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
	rows := tbl.VirtualRows()
	// Built-in stubs are always present; without user-defined routines we
	// should see exactly len(builtinProcs) rows.
	if len(rows) != len(builtinProcs) {
		t.Errorf("rows = %d, want %d (only built-in stubs)", len(rows), len(builtinProcs))
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
	// Built-in stubs + 1 user routine.
	wantLen := len(builtinProcs) + 1
	if len(rows) != wantLen {
		t.Fatalf("rows = %d, want %d", len(rows), wantLen)
	}
	// User routine is appended after built-ins.
	row := rows[len(rows)-1]
	// Columns: oid, proname, pronamespace, prolang, prorettype,
	//          proargtypes, pronargs, proacl, proowner, prosrc.
	if row[1] != "add" {
		t.Errorf("proname = %q, want add", row[1])
	}
	// pronamespace is now the OID string "2200" for public.
	if row[2] != "2200" {
		t.Errorf("pronamespace = %q, want 2200 (public OID)", row[2])
	}
	if row[3] != "plpgsql" {
		t.Errorf("prolang = %q, want plpgsql", row[3])
	}
	// prorettype is now the OID string for "int" (int4 = 23).
	if row[4] != "23" {
		t.Errorf("prorettype = %q, want 23 (int4 OID)", row[4])
	}
	// proargtypes is now space-separated OID strings (oidvector format).
	if row[5] != "23 23" {
		t.Errorf("proargtypes = %q, want \"23 23\" (int4 OID twice)", row[5])
	}
	// pronargs = number of input args (DU-002 slice 7).
	if row[6] != "2" {
		t.Errorf("pronargs = %q, want 2", row[6])
	}
	// proacl is NULL (default privileges).
	if row[7] != "" {
		t.Errorf("proacl = %q, want \"\" (NULL)", row[7])
	}
	// proowner = bootstrap superuser OID 10.
	if row[8] != "10" {
		t.Errorf("proowner = %q, want 10", row[8])
	}
	if row[9] != "BEGIN RETURN $1 + $2; END" {
		t.Errorf("prosrc = %q", row[9])
	}
	if row[0] == "" || row[0] == "0" {
		t.Errorf("oid = %q, want non-zero text", row[0])
	}
}

// TestPgProcViewProretset pins the proretset column (DU-002 slice 34):
// built-in stubs are never SRFs ('f'); a user routine reflects
// catalog.Routine.ReturnsSet. dumpFunc reads this to decide RETURNS SETOF.
func TestPgProcViewProretset(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "gen",
		ReturnType: catalog.Type{Name: "int"},
		ReturnsSet: true,
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "scalar",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// proretset is the last column (index 15).
	const proretset = 15
	// Built-in stubs: never SRFs.
	for i := range builtinProcs {
		if rows[i][proretset] != "f" {
			t.Errorf("built-in stub %q proretset = %q, want f", rows[i][1], rows[i][proretset])
		}
	}
	userRows := rows[len(builtinProcs):]
	// Insertion order: gen (SETOF), scalar.
	if userRows[0][1] != "gen" || userRows[0][proretset] != "t" {
		t.Errorf("gen proretset = %q, want t", userRows[0][proretset])
	}
	if userRows[1][1] != "scalar" || userRows[1][proretset] != "f" {
		t.Errorf("scalar proretset = %q, want f", userRows[1][proretset])
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
	// Built-in stubs + 3 user routines.
	wantLen := len(builtinProcs) + 3
	if len(rows) != wantLen {
		t.Fatalf("rows = %d, want %d", len(rows), wantLen)
	}
	// Insertion order: zeta, alpha, mu — OIDs assigned in that
	// order — user view rows are appended after built-ins in insertion order.
	wantNames := []string{"zeta", "alpha", "mu"}
	userRows := rows[len(builtinProcs):]
	for i, want := range wantNames {
		if userRows[i][1] != want {
			t.Errorf("row %d proname = %q, want %q", i, userRows[i][1], want)
		}
	}
}
