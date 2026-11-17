package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgClassOwnerAndPartitionedIndexRelkind pins two pg_class columns
// (M0149-0001h1a26): relowner is the creating role — SET ROLE counts, as
// heap_create_with_catalog uses GetUserId() — not always the bootstrap
// superuser; and an index on a partitioned table is relkind 'I' with no
// storage (relfilenode 0) from the moment it is created.
func TestPgClassOwnerAndPartitionedIndexRelkind(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE pt (id int) PARTITION BY RANGE (id)",
		"CREATE INDEX pti ON pt (id)",
		"CREATE TABLE plain (a int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	rows := runQueryRows(t, ctx, "SELECT relname, relkind, relfilenode FROM pg_catalog.pg_class WHERE relname IN ('pt', 'pti') ORDER BY relname")
	if len(rows) != 2 || rows[1][1].StringValue() != "I" || rows[1][2].Int != 0 {
		t.Fatalf("partitioned index row = %+v, want relkind I, relfilenode 0", rows)
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("fixture catalog is not *catalog.InMemory")
	}
	im.RegisterRole("owner_probe")
	oid, ok := ctx.Catalog.RoleOID("owner_probe")
	if !ok {
		t.Fatal("owner_probe role missing")
	}
	if got := tableOwnerOID(ctx.Catalog, &catalog.Table{Owner: "owner_probe"}); got != int64(oid) {
		t.Errorf("relowner for Owner=owner_probe = %d, want %d", got, oid)
	}
	if got := tableOwnerOID(ctx.Catalog, &catalog.Table{}); got != 10 {
		t.Errorf("relowner for an unset owner = %d, want 10", got)
	}
	if got := catalogIndexRelkindProbe(); got != "i" {
		t.Errorf("IndexRelkind(nil) = %q, want i", got)
	}
}

func catalogIndexRelkindProbe() string { return catalog.IndexRelkind(nil) }
