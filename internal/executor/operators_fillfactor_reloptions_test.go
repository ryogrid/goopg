package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFillfactorSurfacesInPgClassReloptions verifies that a `WITH
// (fillfactor=N)` storage parameter declared on CREATE TABLE is persisted on
// the catalog table and surfaced through the pg_class virtual view's reloptions
// cell as the text[] literal `{fillfactor=N}`. pg_dump renders that array back
// as `WITH (fillfactor='N')`, so this is the engine-side half of the round-trip
// the pg_dump TAP port (TestPort_PgDumpConnectionSetup, slice 54) asserts
// end-to-end. DU-002 slice 54.
func TestFillfactorSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE opt (id integer PRIMARY KEY) WITH (fillfactor=70)`); err != nil {
		t.Fatalf("CREATE TABLE opt: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE plain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE plain: %v", err)
	}

	optTbl, ok := cat.LookupTable(parser.ObjectName{Name: "opt"})
	if !ok {
		t.Fatal("opt table not found")
	}
	if optTbl.Fillfactor != 70 {
		t.Errorf("opt.Fillfactor = %d, want 70", optTbl.Fillfactor)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "plain"})
	if !ok {
		t.Fatal("plain table not found")
	}
	if plainTbl.Fillfactor != 0 {
		t.Errorf("plain.Fillfactor = %d, want 0 (unset)", plainTbl.Fillfactor)
	}

	// pg_class.reloptions (column index 32) must read `{fillfactor=70}` for the
	// option-bearing table and "" (→ SQL NULL) for the plain one.
	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "opt" || r[1] == "plain") {
			got[r[1]] = r[32]
		}
	}
	if got["opt"] != "{fillfactor=70}" {
		t.Errorf("pg_class.reloptions for opt = %q, want %q", got["opt"], "{fillfactor=70}")
	}
	if got["plain"] != "" {
		t.Errorf("pg_class.reloptions for plain = %q, want \"\" (NULL)", got["plain"])
	}
}

// TestFillfactorOutOfBoundsRejected verifies CREATE TABLE rejects a fillfactor
// outside the valid 10–100 range with PG's 22023 error, mirroring the existing
// CREATE INDEX bounds check. DU-002 slice 54.
func TestFillfactorOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ff := range []string{"5", "0", "101"} {
		err := runDDL(t, ctx, `CREATE TABLE bad`+ff+` (id integer) WITH (fillfactor=`+ff+`)`)
		if err == nil {
			t.Errorf("fillfactor=%s: expected an out-of-bounds error, got nil", ff)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("fillfactor=%s: error type = %T, want *ExecError", ff, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("fillfactor=%s: error code = %q, want 22023", ff, ee.Code)
		}
	}
}
