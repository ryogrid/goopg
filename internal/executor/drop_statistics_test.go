package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDropStatistics guards the DU-002 restart-persistence follow-up to
// slice 441's discovery: `DROP STATISTICS` was entirely unparsed before this
// change (no grammar rule matched it at all), so a user's DROP STATISTICS
// statement failed with a syntax error instead of removing the extended
// statistics object. Mirrors TestAlterStatisticsRenameOwnerSetSchema's shape.
func TestDropStatistics(t *testing.T) {
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
	if _, found := im.LookupStatistics("s"); !found {
		t.Fatal("LookupStatistics(\"s\") not found after CREATE STATISTICS")
	}

	if err := runDDL(t, ctx, `DROP STATISTICS s`); err != nil {
		t.Fatalf("DROP STATISTICS: %v", err)
	}
	if _, found := im.LookupStatistics("s"); found {
		t.Error("statistics object \"s\" still resolves after DROP STATISTICS")
	}

	// Schema-qualified DROP.
	im.RegisterSchema("myschema")
	if err := runDDL(t, ctx, `CREATE STATISTICS myschema.s2 ON a, b FROM t`); err != nil {
		t.Fatalf("CREATE STATISTICS (schema-qualified): %v", err)
	}
	if err := runDDL(t, ctx, `DROP STATISTICS myschema.s2`); err != nil {
		t.Fatalf("DROP STATISTICS (schema-qualified): %v", err)
	}
	if _, found := im.LookupStatistics("myschema.s2"); found {
		t.Error("statistics object \"myschema.s2\" still resolves after DROP STATISTICS")
	}

	// IF EXISTS on an unknown statistics object is a no-op.
	if err := runDDL(t, ctx, `DROP STATISTICS IF EXISTS nosuchstat`); err != nil {
		t.Fatalf("DROP STATISTICS IF EXISTS on unknown object should be a no-op, got: %v", err)
	}

	// Without IF EXISTS, an unknown statistics object raises 42704.
	err := runDDL(t, ctx, `DROP STATISTICS nosuchstat`)
	if err == nil {
		t.Fatal("DROP STATISTICS on unknown object without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	if !strings.Contains(err.Error(), "nosuchstat") {
		t.Errorf("err = %v, want it to name the missing statistics object", err)
	}
}
