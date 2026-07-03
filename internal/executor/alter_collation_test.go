package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterCollationRenameOwnerRefresh guards the loop #50 deferral: ALTER
// COLLATION RENAME TO / OWNER TO / REFRESH VERSION were entirely unhandled
// (no parser branch existed for the "collation" keyword inside parseAlter at
// all). M0119-0004 (DU-002).
func TestAlterCollationRenameOwnerRefresh(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE COLLATION mycoll (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION: %v", err)
	}

	// RENAME TO.
	if err := runDDL(t, ctx, `ALTER COLLATION mycoll RENAME TO renamedcoll`); err != nil {
		t.Fatalf("ALTER COLLATION RENAME TO: %v", err)
	}
	if _, found := im.CollationAttrsByName("mycoll"); found {
		t.Error("old collation name still resolves after RENAME TO")
	}
	uc, found := im.CollationAttrsByName("renamedcoll")
	if !found {
		t.Fatal("renamed collation not found via CollationAttrsByName")
	}
	if uc.Owner != 10 {
		t.Errorf("Owner before OWNER TO = %d, want 10 (bootstrap superuser default)", uc.Owner)
	}

	// OWNER TO a registered role.
	im.RegisterRole("newowner")
	wantOID, found := im.RoleOID("newowner")
	if !found {
		t.Fatal("RoleOID(\"newowner\") not found after RegisterRole")
	}
	if err := runDDL(t, ctx, `ALTER COLLATION renamedcoll OWNER TO newowner`); err != nil {
		t.Fatalf("ALTER COLLATION OWNER TO: %v", err)
	}
	uc, found = im.CollationAttrsByName("renamedcoll")
	if !found || uc.Owner != wantOID {
		t.Errorf("Owner after OWNER TO = %+v, want owner OID %d", uc, wantOID)
	}

	// REFRESH VERSION is a no-op notice, not an error.
	if err := runDDL(t, ctx, `ALTER COLLATION renamedcoll REFRESH VERSION`); err != nil {
		t.Fatalf("ALTER COLLATION REFRESH VERSION: %v", err)
	}
	if _, found := im.CollationAttrsByName("renamedcoll"); !found {
		t.Error("collation vanished after REFRESH VERSION")
	}

	// IF EXISTS on an unknown collation is a no-op.
	if err := runDDL(t, ctx, `ALTER COLLATION IF EXISTS nosuchcoll RENAME TO x`); err != nil {
		t.Fatalf("ALTER COLLATION IF EXISTS on unknown collation should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown collation raises 42704.
	err := runDDL(t, ctx, `ALTER COLLATION nosuchcoll RENAME TO x`)
	if err == nil {
		t.Fatal("ALTER COLLATION on unknown collation without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	if !strings.Contains(err.Error(), "nosuchcoll") {
		t.Errorf("err = %v, want it to name the missing collation", err)
	}
}

// TestAlterCollationSetSchema guards DU-002 slice 442: ALTER COLLATION name
// SET SCHEMA newschema was the last unmodelled ALTER COLLATION form
// (RENAME TO / OWNER TO already had dedicated parser+executor support),
// silently discarded as a no-op.
func TestAlterCollationSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA otherschema`); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE COLLATION setschemacoll (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER COLLATION setschemacoll SET SCHEMA otherschema`); err != nil {
		t.Fatalf("ALTER COLLATION SET SCHEMA: %v", err)
	}
	uc, found := im.CollationAttrsByName("setschemacoll")
	if !found {
		t.Fatal("collation not found via CollationAttrsByName after SET SCHEMA")
	}
	wantNsOID := im.SchemaOID("otherschema")
	if wantNsOID == 0 {
		t.Fatal("SchemaOID(\"otherschema\") = 0, want a real namespace OID")
	}
	if uc.NamespaceOID != wantNsOID {
		t.Errorf("NamespaceOID after SET SCHEMA = %d, want %d (otherschema)", uc.NamespaceOID, wantNsOID)
	}

	// IF EXISTS on an unknown collation is a no-op.
	if err := runDDL(t, ctx, `ALTER COLLATION IF EXISTS nosuchcoll SET SCHEMA otherschema`); err != nil {
		t.Fatalf("ALTER COLLATION IF EXISTS SET SCHEMA on unknown collation should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown collation raises 42704.
	err := runDDL(t, ctx, `ALTER COLLATION nosuchcoll SET SCHEMA otherschema`)
	if err == nil {
		t.Fatal("ALTER COLLATION SET SCHEMA on unknown collation without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
