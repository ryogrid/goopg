package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAlterSchemaRenameOwner guards DU-002 slice 440 resume point (3)
// (M0110-0001): ALTER SCHEMA RENAME TO / OWNER TO previously fell into the
// generic compat-stub loop in parseAlter, which silently consumed and
// discarded both forms — a user's rename/re-own statement executed
// successfully but the schema registry, and every table/sequence inside it,
// never changed. Mirrors TestAlterStatisticsRenameOwnerSetSchema's shape.
func TestAlterSchemaRenameOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA s1`); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE s1.t1 (a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE SEQUENCE s1.seq1`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if !im.SchemaExists("s1") {
		t.Fatal("schema s1 not registered after CREATE SCHEMA")
	}

	// RENAME TO re-keys the schema registry and cascades into contained
	// tables/sequences.
	if err := runDDL(t, ctx, `ALTER SCHEMA s1 RENAME TO s2`); err != nil {
		t.Fatalf("ALTER SCHEMA RENAME TO: %v", err)
	}
	if im.SchemaExists("s1") {
		t.Error("old schema name s1 still resolves after RENAME TO")
	}
	if !im.SchemaExists("s2") {
		t.Fatal("renamed schema s2 not found via SchemaExists")
	}
	tbl, found := im.LookupTable(parser.ObjectName{Schema: "s2", Name: "t1"})
	if !found || tbl.Schema != "s2" {
		t.Errorf("table after schema RENAME = %+v, found=%v, want Schema=s2", tbl, found)
	}
	if _, found := im.LookupTable(parser.ObjectName{Schema: "s1", Name: "t1"}); found {
		t.Error("table still resolves under the old schema after RENAME TO")
	}

	// RENAME TO an already-existing schema is rejected (42P06).
	if err := runDDL(t, ctx, `CREATE SCHEMA taken`); err != nil {
		t.Fatalf("CREATE SCHEMA taken: %v", err)
	}
	err := runDDL(t, ctx, `ALTER SCHEMA s2 RENAME TO taken`)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42P06" {
		t.Errorf("err = %v, want *ExecError{Code: 42P06}", err)
	}

	// RENAME TO on an unknown schema raises 3F000.
	err = runDDL(t, ctx, `ALTER SCHEMA nosuchschema RENAME TO whatever`)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "3F000" {
		t.Errorf("err = %v, want *ExecError{Code: 3F000}", err)
	}

	// OWNER TO an unregistered role raises 42704.
	err = runDDL(t, ctx, `ALTER SCHEMA s2 OWNER TO nosuchrole`)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// OWNER TO a registered role updates pg_namespace.nspowner.
	im.RegisterRole("newowner")
	wantOID, found := im.RoleOID("newowner")
	if !found {
		t.Fatal("RoleOID(\"newowner\") not found after RegisterRole")
	}
	if err := runDDL(t, ctx, `ALTER SCHEMA s2 OWNER TO newowner`); err != nil {
		t.Fatalf("ALTER SCHEMA OWNER TO: %v", err)
	}
	if got := im.SchemaOwnerOID("s2"); got != wantOID {
		t.Errorf("SchemaOwnerOID(s2) = %d, want %d", got, wantOID)
	}

	// A schema with no explicit owner change defaults to the bootstrap
	// superuser (OID 10), matching pg_namespace.nspowner's prior hardcoded
	// literal.
	if got := im.SchemaOwnerOID("taken"); got != 10 {
		t.Errorf("SchemaOwnerOID(taken) = %d, want 10 (default)", got)
	}
}
