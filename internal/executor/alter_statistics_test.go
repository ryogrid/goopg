package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterStatisticsRenameOwnerSetSchema guards DU-002 slice 441: ALTER
// STATISTICS RENAME TO / OWNER TO / SET SCHEMA previously parsed to a fully
// unmodelled no-op (the parser silently discarded the trailing tokens), so a
// user's rename/re-own/move statement executed successfully but left the
// statistics object completely unchanged. Mirrors
// TestAlterCollationRenameOwnerRefresh's shape.
func TestAlterStatisticsRenameOwnerSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TABLE t (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE STATISTICS s ON a, b FROM t`); err != nil {
		t.Fatalf("CREATE STATISTICS: %v", err)
	}

	obj, found := im.LookupStatistics("s")
	if !found {
		t.Fatal("LookupStatistics(\"s\") not found after CREATE STATISTICS")
	}
	if obj.OwnerOrDefault() != 10 {
		t.Errorf("Owner before OWNER TO = %d, want 10 (bootstrap superuser default)", obj.OwnerOrDefault())
	}

	// RENAME TO.
	if err := runDDL(t, ctx, `ALTER STATISTICS s RENAME TO s2`); err != nil {
		t.Fatalf("ALTER STATISTICS RENAME TO: %v", err)
	}
	if _, found := im.LookupStatistics("s"); found {
		t.Error("old statistics name still resolves after RENAME TO")
	}
	obj, found = im.LookupStatistics("s2")
	if !found {
		t.Fatal("renamed statistics object not found via LookupStatistics")
	}

	// OWNER TO a registered role.
	im.RegisterRole("newowner")
	wantOID, found := im.RoleOID("newowner")
	if !found {
		t.Fatal("RoleOID(\"newowner\") not found after RegisterRole")
	}
	if err := runDDL(t, ctx, `ALTER STATISTICS s2 OWNER TO newowner`); err != nil {
		t.Fatalf("ALTER STATISTICS OWNER TO: %v", err)
	}
	obj, found = im.LookupStatistics("s2")
	if !found || obj.OwnerOrDefault() != wantOID {
		t.Errorf("Owner after OWNER TO = %+v, want owner OID %d", obj, wantOID)
	}

	// SET SCHEMA moves the object and re-keys the lookup.
	im.RegisterSchema("myschema")
	if err := runDDL(t, ctx, `ALTER STATISTICS s2 SET SCHEMA myschema`); err != nil {
		t.Fatalf("ALTER STATISTICS SET SCHEMA: %v", err)
	}
	if _, found := im.LookupStatistics("public.s2"); found {
		t.Error("statistics object still resolves under the old schema after SET SCHEMA")
	}
	obj, found = im.LookupStatistics("myschema.s2")
	if !found || obj.Schema != "myschema" {
		t.Errorf("statistics object after SET SCHEMA = %+v, want Schema=myschema", obj)
	}

	// IF EXISTS on an unknown statistics object is a no-op.
	if err := runDDL(t, ctx, `ALTER STATISTICS IF EXISTS nosuchstat RENAME TO x`); err != nil {
		t.Fatalf("ALTER STATISTICS IF EXISTS on unknown object should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown statistics object raises 42704.
	err := runDDL(t, ctx, `ALTER STATISTICS nosuchstat RENAME TO x`)
	if err == nil {
		t.Fatal("ALTER STATISTICS on unknown object without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	if !strings.Contains(err.Error(), "nosuchstat") {
		t.Errorf("err = %v, want it to name the missing statistics object", err)
	}
}
