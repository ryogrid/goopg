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
	// prolang is now the oid string (matches PG's pg_proc.prolang). plpgsql is
	// not installed in goopg's pg_language, so it maps to "0" (InvalidOid); the
	// 3 built-in langs (internal/c/sql) map to 12/13/14. DU-002 slice 42.
	if row[3] != "0" {
		t.Errorf("prolang = %q, want 0 (plpgsql not in pg_language)", row[3])
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

// TestPgProcViewProbin pins the probin column (DU-002 slice 35):
// always NULL ("") for both built-in stubs and user routines — goopg has
// no C-language functions with an on-disk binary path. dumpFunc projects
// probin to emit `AS '<probin>', '<prosrc>'` for C functions.
func TestPgProcViewProbin(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "f",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// probin is the last column (index 16), appended after proretset (15).
	const probin = 16
	for i := range rows {
		if rows[i][probin] != "" {
			t.Errorf("row %q probin = %q, want \"\" (NULL)", rows[i][1], rows[i][probin])
		}
	}
}

// TestPgProcViewProconfig pins the proconfig column (DU-002 slice 36):
// always NULL ("") for both built-in stubs and user routines — goopg tracks
// no per-function GUC SET clauses. dumpFunc reads proconfig to emit `SET ...`
// lines in a function's definition; NULL means it emits none.
func TestPgProcViewProconfig(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "f",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// proconfig is the last column (index 17), appended after probin (16).
	const proconfig = 17
	for i := range rows {
		if rows[i][proconfig] != "" {
			t.Errorf("row %q proconfig = %q, want \"\" (NULL)", rows[i][1], rows[i][proconfig])
		}
	}
}

// TestPgProcViewProcost pins the procost column (DU-002 slice 37):
// built-in stubs (internal language) cost 1; a user routine's cost is
// derived from its language — 1 for internal/C, 100 for all others.
// dumpFunc reads procost to emit `COST <n>` when it differs from the default.
func TestPgProcViewProcost(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "plf",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "plpgsql",
		Body:       "BEGIN RETURN 1; END",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "cf",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "c",
		Body:       "cfunc",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// procost is the last column (index 18), appended after proconfig (17).
	const procost = 18
	// Built-in stubs are internal-language: cost 1.
	for i := range builtinProcs {
		if rows[i][procost] != "1" {
			t.Errorf("built-in stub %q procost = %q, want 1", rows[i][1], rows[i][procost])
		}
	}
	userRows := rows[len(builtinProcs):]
	// Insertion order: plf (plpgsql → 100), cf (c → 1).
	if userRows[0][1] != "plf" || userRows[0][procost] != "100" {
		t.Errorf("plf procost = %q, want 100", userRows[0][procost])
	}
	if userRows[1][1] != "cf" || userRows[1][procost] != "1" {
		t.Errorf("cf procost = %q, want 1", userRows[1][procost])
	}
}

// TestPgProcViewProrows pins the prorows column (DU-002 slice 38):
// 1000 estimated result rows for set-returning functions, 0 otherwise —
// mirrors PG's CREATE FUNCTION default. dumpFunc reads prorows to emit
// `ROWS <n>` for SRFs.
func TestPgProcViewProrows(t *testing.T) {
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
	// prorows is the last column (index 19), appended after procost (18).
	const prorows = 19
	// Built-in stubs are never SRFs: 0.
	for i := range builtinProcs {
		if rows[i][prorows] != "0" {
			t.Errorf("built-in stub %q prorows = %q, want 0", rows[i][1], rows[i][prorows])
		}
	}
	userRows := rows[len(builtinProcs):]
	// Insertion order: gen (SETOF → 1000), scalar (→ 0).
	if userRows[0][1] != "gen" || userRows[0][prorows] != "1000" {
		t.Errorf("gen prorows = %q, want 1000", userRows[0][prorows])
	}
	if userRows[1][1] != "scalar" || userRows[1][prorows] != "0" {
		t.Errorf("scalar prorows = %q, want 0", userRows[1][prorows])
	}
}

// TestPgProcViewProtrftypes pins the protrftypes column (DU-002 slice 39):
// always NULL — goopg supports no transforms, so dumpFunc emits no
// `TRANSFORM FOR TYPE ...` clause for any routine, built-in or user-defined.
func TestPgProcViewProtrftypes(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "myfunc",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// protrftypes is the last column (index 20), appended after prorows (19).
	const protrftypes = 20
	for i := range rows {
		if rows[i][protrftypes] != "" {
			t.Errorf("row %q protrftypes = %q, want NULL (empty)", rows[i][1], rows[i][protrftypes])
		}
	}
}

// TestPgProcViewProparallel pins the proparallel column (DU-002 slice 40):
// always 'u' (unsafe) — goopg tracks no parallel-safety, mirroring PG's
// CREATE FUNCTION default, so dumpFunc emits PARALLEL UNSAFE for every routine.
func TestPgProcViewProparallel(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "myfunc",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	rows := tbl.VirtualRows()
	// proparallel is the last column (index 21), appended after protrftypes (20).
	const proparallel = 21
	for i := range rows {
		if rows[i][proparallel] != "u" {
			t.Errorf("row %q proparallel = %q, want u (unsafe)", rows[i][1], rows[i][proparallel])
		}
	}
}

// TestPgProcViewProsupport pins the prosupport column (DU-002 slice 41, fixed in
// slice 148): always "-" (regproc text for InvalidOid) — goopg has no planner
// support functions. The column is typed regproc (as in PG's pg_proc), so
// InvalidOid renders as the text "-", NOT "0". pg_dump's dumpFunc emits
// `SUPPORT <val>` whenever `strcmp(prosupport, "-") != 0`; an `oid`-typed "0"
// cell made pg_dump emit the invalid `SUPPORT 0` clause (a restore error —
// SUPPORT wants a function name). "-" suppresses the clause, matching real PG.
func TestPgProcViewProsupport(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "myfunc",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	// The prosupport column must be regproc-typed so InvalidOid renders as "-"
	// (an oid-typed "0" makes pg_dump emit the invalid `SUPPORT 0` clause).
	var prosupportCol catalog.Column
	for _, c := range tbl.Columns {
		if c.Name == "prosupport" {
			prosupportCol = c
		}
	}
	if prosupportCol.Type.Name != "regproc" {
		t.Fatalf("prosupport column type = %q, want regproc (oid makes pg_dump emit `SUPPORT 0`)", prosupportCol.Type.Name)
	}
	rows := tbl.VirtualRows()
	// prosupport is the last column (index 22), appended after proparallel (21).
	const prosupport = 22
	for i := range rows {
		if rows[i][prosupport] != "-" {
			t.Errorf("row %q prosupport = %q, want - (regproc InvalidOid)", rows[i][1], rows[i][prosupport])
		}
	}
}

// TestPgProcViewProlangOID pins the prolang column as oid-typed with OID-string
// values (DU-002 slice 42). PG's pg_proc.prolang is oid; pg_dump's dumpFunc joins
// `pg_language l ON l.oid = p.prolang`, so a text-typed prolang silently returns 0
// rows ("0 rows instead of one") and aborts the dump. The built-in stubs use OID
// "12" (internal); a user sql routine maps to "14".
func TestPgProcViewProlangOID(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgProcView(cat); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Schema:     "public",
		Name:       "sqlfn",
		ReturnType: catalog.Type{Name: "int"},
		Language:   "sql",
		Body:       "SELECT 1",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_proc"})
	// The prolang column must be oid-typed so the join compares oid=oid.
	var prolangCol catalog.Column
	for _, c := range tbl.Columns {
		if c.Name == "prolang" {
			prolangCol = c
		}
	}
	if prolangCol.Type.Name != "oid" {
		t.Fatalf("prolang column type = %q, want oid (text breaks dumpFunc join)", prolangCol.Type.Name)
	}
	rows := tbl.VirtualRows()
	const prolang = 3
	// Built-in RI_FKey_cascade_del (oid 1654) is an internal-language stub → "12".
	var sawInternal, sawSQL bool
	for _, r := range rows {
		switch r[1] {
		case "RI_FKey_cascade_del":
			sawInternal = true
			if r[prolang] != "12" {
				t.Errorf("RI_FKey_cascade_del prolang = %q, want 12 (internal)", r[prolang])
			}
		case "sqlfn":
			sawSQL = true
			if r[prolang] != "14" {
				t.Errorf("sqlfn prolang = %q, want 14 (sql)", r[prolang])
			}
		}
	}
	if !sawInternal || !sawSQL {
		t.Fatalf("missing rows: sawInternal=%v sawSQL=%v", sawInternal, sawSQL)
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
