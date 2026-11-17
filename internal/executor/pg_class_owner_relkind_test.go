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
		"CREATE TABLE sub PARTITION OF pt FOR VALUES FROM (0) TO (10) PARTITION BY RANGE (id)",
		"CREATE TABLE leaf PARTITION OF sub FOR VALUES FROM (0) TO (5)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	rows := runQueryRows(t, ctx, "SELECT relname, relkind, relfilenode FROM pg_catalog.pg_class WHERE relname IN ('pt', 'pti') ORDER BY relname")
	if len(rows) != 2 || rows[1][1].StringValue() != "I" || rows[1][2].Int != 0 {
		t.Fatalf("partitioned index row = %+v, want relkind I, relfilenode 0", rows)
	}
	// A sub-partitioned partition is itself RELKIND_PARTITIONED_TABLE; its
	// own partition is an ordinary table (M0149-0001h1a28).
	kinds := runQueryRows(t, ctx, "SELECT relname, relkind FROM pg_catalog.pg_class WHERE relname IN ('sub', 'leaf') ORDER BY relname")
	if len(kinds) != 2 || kinds[0][1].StringValue() != "r" || kinds[1][1].StringValue() != "p" {
		t.Errorf("leaf / sub relkind = %+v, want r / p", kinds)
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

// TestIndexPartitionsAreLinked pins M0149-0001h1a29: an index cloned onto a
// new partition and a matching index on an ATTACHed table are partitions of
// the parent index (pg_inherits row, relispartition), as DefineIndex /
// AttachPartitionEnsureIndexes record them; an ATTACHed table is
// relispartition too.
func TestIndexPartitionsAreLinked(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE lp (id int) PARTITION BY RANGE (id)",
		"CREATE INDEX lpi ON lp (id)",
		"CREATE TABLE lp1 PARTITION OF lp FOR VALUES FROM (0) TO (10)",
		"CREATE TABLE lp2 (id int)",
		"CREATE INDEX lp2i ON lp2 (id)",
		"ALTER TABLE lp ATTACH PARTITION lp2 FOR VALUES FROM (10) TO (20)",
		"CREATE TABLE lp3 (id int)",
		"ALTER TABLE lp ATTACH PARTITION lp3 FOR VALUES FROM (20) TO (30)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	rows := runQueryRows(t, ctx, "SELECT count(*) FROM pg_catalog.pg_inherits")
	if len(rows) != 1 || rows[0][0].Int != 6 {
		t.Fatalf("pg_inherits rows = %+v, want 6 (3 tables + 3 indexes)", rows)
	}
	parts := runQueryRows(t, ctx, "SELECT count(*) FROM pg_catalog.pg_class WHERE relispartition")
	if len(parts) != 1 || parts[0][0].Int != 6 {
		t.Errorf("relispartition rows = %+v, want 6", parts)
	}
}

// TestAttachAndRegclassFollowSearchPath pins M0149-0001h1a30: with
// search_path set to a schema, an unqualified ATTACH PARTITION child and an
// unqualified '<name>'::regclass resolve there (RangeVarGetRelid), and a
// missing ATTACH child is 42P01 instead of a silent no-op.
func TestAttachAndRegclassFollowSearchPath(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "sp1", true
		}
		return "", false
	}
	for _, ddl := range []string{
		"CREATE SCHEMA sp1",
		"CREATE TABLE sp1.spp (id int) PARTITION BY RANGE (id)",
		"CREATE TABLE sp1.spc (id int)",
		"ALTER TABLE sp1.spp ATTACH PARTITION spc FOR VALUES FROM (0) TO (10)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	rows := runQueryRows(t, ctx, "SELECT count(*) FROM pg_catalog.pg_inherits")
	if len(rows) != 1 || rows[0][0].Int != 1 {
		t.Errorf("pg_inherits rows = %+v, want 1 (spc attached under search_path)", rows)
	}
	d, err := regIdentifierInput(NewStringDatum("spc"), "regclass", ctx, 0)
	if err != nil || d.Kind != KindInt || d.Int == 0 {
		t.Errorf("'spc'::regclass = %+v, %v", d, err)
	}
	err = runDDL(t, ctx, "ALTER TABLE sp1.spp ATTACH PARTITION nosuch FOR VALUES FROM (10) TO (20)")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
		t.Errorf("ATTACH of a missing table: %v, want 42P01", err)
	}
}
