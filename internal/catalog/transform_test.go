package catalog

import (
	"strconv"
	"testing"
)

// TestLanguageNameToOID covers the 4 pg_language rows the pg_language virtual
// view serves (see the pg_language registration in catalog.go). Used by
// CREATE TRANSFORM to resolve pg_transform.trflang. DU-002 (M0119-0004).
func TestLanguageNameToOID(t *testing.T) {
	cases := []struct {
		name string
		oid  uint32
	}{
		{"internal", 12},
		{"c", 13},
		{"sql", 14},
		{"SQL", 14}, // case-insensitive
		{"plpgsql", 13627},
		{"plpythonu", 0}, // unknown/user-defined language → 0
	}
	for _, tc := range cases {
		if got := LanguageNameToOID(tc.name); got != tc.oid {
			t.Errorf("LanguageNameToOID(%q) = %d, want %d", tc.name, got, tc.oid)
		}
	}
}

// TestTransformRegistry covers RegisterTransform/TransformExists/DropTransform/
// ListTransforms: idempotent re-registration keeps the OID but refreshes the
// function OIDs, and DropTransform removes the entry. DU-002 (M0119-0004).
func TestTransformRegistry(t *testing.T) {
	c := NewInMemory()
	if c.TransformExists("int", "sql") {
		t.Fatal("TransformExists on empty registry = true, want false")
	}
	tf := c.RegisterTransform("int", "sql", 3721, 2406)
	if tf.OID == 0 {
		t.Fatal("RegisterTransform returned OID 0")
	}
	if !c.TransformExists("int", "sql") {
		t.Error("TransformExists after register = false, want true")
	}
	// Case-insensitive key match.
	if !c.TransformExists("INT", "SQL") {
		t.Error("TransformExists is not case-insensitive")
	}
	// Re-registering the same pair keeps the OID but refreshes func OIDs.
	tf2 := c.RegisterTransform("int", "sql", 0, 2406)
	if tf2.OID != tf.OID {
		t.Errorf("re-register changed OID: got %d, want %d", tf2.OID, tf.OID)
	}
	if tf2.FromFuncOID != 0 {
		t.Errorf("re-register FromFuncOID = %d, want 0", tf2.FromFuncOID)
	}

	list := c.ListTransforms()
	if len(list) != 1 {
		t.Fatalf("ListTransforms = %d entries, want 1", len(list))
	}

	if !c.DropTransform("int", "sql") {
		t.Error("DropTransform = false, want true")
	}
	if c.TransformExists("int", "sql") {
		t.Error("TransformExists after drop = true, want false")
	}
	if c.DropTransform("int", "sql") {
		t.Error("second DropTransform = true, want false (already removed)")
	}
}

// TestPgTransformVirtualRows verifies that a registered transform surfaces as
// a dumpable pg_transform row with the resolved type/language OIDs and the
// from/to function OIDs pg_dump's getTransforms/dumpTransform expect. Mirrors
// the real fixture: `CREATE TRANSFORM FOR int LANGUAGE sql (FROM SQL WITH
// FUNCTION prsd_lextype(internal), TO SQL WITH FUNCTION int4recv(internal))`.
// DU-002 (M0119-0004).
func TestPgTransformVirtualRows(t *testing.T) {
	c := NewInMemory()
	tbl := c.ns(DefaultDBOid).tables["pg_catalog.pg_transform"]
	if tbl == nil || tbl.VirtualRows == nil {
		t.Fatal("pg_transform virtual table missing")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Fatalf("pg_transform rows on empty registry = %d, want 0", len(rows))
	}

	tf := c.RegisterTransform("int", "sql", 3721, 2406)

	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_transform rows = %d, want 1", len(rows))
	}
	r := rows[0]
	// cols: oid, trftype, trflang, trffromsql, trftosql
	if r[0] != strconv.FormatUint(uint64(tf.OID), 10) {
		t.Errorf("oid = %q, want %q", r[0], strconv.FormatUint(uint64(tf.OID), 10))
	}
	if r[1] != "23" { // int4
		t.Errorf("trftype = %q, want 23 (int4)", r[1])
	}
	if r[2] != "14" { // sql
		t.Errorf("trflang = %q, want 14 (sql)", r[2])
	}
	if r[3] != "3721" {
		t.Errorf("trffromsql = %q, want 3721", r[3])
	}
	if r[4] != "2406" {
		t.Errorf("trftosql = %q, want 2406", r[4])
	}

	// A from/to func OID of 0 (unresolved builtin) renders as literal "0", not
	// NULL — matching pg_cast.castfunc's convention for the same situation.
	c2 := NewInMemory()
	c2.RegisterTransform("hstore", "plpythonu", 0, 0)
	rows2 := c2.ns(DefaultDBOid).tables["pg_catalog.pg_transform"].VirtualRows()
	if rows2[0][3] != "0" || rows2[0][4] != "0" {
		t.Errorf("unresolved func OIDs = %q/%q, want 0/0", rows2[0][3], rows2[0][4])
	}
}
