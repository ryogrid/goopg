package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0146-0196 pins: REINDEX INDEX / TABLE / SCHEMA must resolve relation
// names in the CONNECTION's database, the way every SELECT does via
// ctxPlanCatalog / SearchPathCatalog. Before this fix the reindex op called
// raw ctx.Catalog.LookupTable/LookupIndex/TablesInSchema — always
// DefaultDBOid's namespace — so in a CREATE DATABASE'd database
// `REINDEX TABLE t` / `REINDEX INDEX i` raised 42P01 for a relation SELECT
// could read, and `REINDEX SCHEMA s` silently rebuilt nothing while
// returning success (the worst variant, same class as bare-VACUUM's
// wrong-db no-op). DROP INDEX already routed through
// catalog.NamespaceDBOid(ctx.CurrentDatabaseOid) and needed no change.
//
// Each test builds the fixture under a genuinely distinct dbOid, corrupts
// the index's storage in place (truncate to zero blocks — the same device
// TestReindexIndexPhysicallyRebuilds uses), runs the REINDEX form, then
// proves the physical rebuild happened by scanning the reopened btree: if
// name resolution still hit the wrong namespace the statement would either
// error (INDEX/TABLE) or return while leaving the corruption in place
// (SCHEMA).

// corruptIndexForReindex truncates idx's storage to zero blocks and drops
// it from the buffer pool so a later read observes the damage.
func corruptIndexForReindex(t *testing.T, ctx *Context, idxName string, dbOid uint32) storage.RelFileNode {
	t.Helper()
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: idxName}, dbOid)
	if !ok {
		t.Fatalf("fixture: %s not found under dbOid %d", idxName, dbOid)
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	if err := ctx.Pool.Manager().TruncateRelation(idxRel); err != nil {
		t.Fatalf("TruncateRelation: %v", err)
	}
	ctx.Pool.InvalidateRel(idxRel)
	if n, err := ctx.Pool.NBlocks(idxRel); err != nil || n != 0 {
		t.Fatalf("pre-REINDEX sanity: NBlocks = %d, %v, want 0, nil", n, err)
	}
	return idxRel
}

// assertIndexRebuilt scans the reopened btree and wants exactly want
// entries — proof REINDEX physically repopulated it from the heap.
func assertIndexRebuilt(t *testing.T, ctx *Context, idxRel storage.RelFileNode, want int) {
	t.Helper()
	bt, err := nbtree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatalf("nbtree.Open after REINDEX: %v", err)
	}
	var got []storage.ItemPointer
	if err := bt.RangeScan(nil, nil, func(key []byte, ptr storage.ItemPointer) (bool, error) {
		got = append(got, ptr)
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != want {
		t.Fatalf("post-REINDEX entry count = %d, want %d (REINDEX did not physically rebuild the index)", len(got), want)
	}
}

func TestReindexIndexResolvesConnectionDBOid(t *testing.T) {
	const otherDBOid = 7373
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE ridx_t (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO ridx_t VALUES (1),(2),(3),(4),(5)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX ridx_i ON ridx_t (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idxRel := corruptIndexForReindex(t, ctx, "ridx_i", otherDBOid)

	if err := runDDL(t, ctx, "REINDEX INDEX ridx_i"); err != nil {
		t.Fatalf("REINDEX INDEX in the connection's own database: %v", err)
	}
	assertIndexRebuilt(t, ctx, idxRel, 5)
}

func TestReindexTableResolvesConnectionDBOid(t *testing.T) {
	const otherDBOid = 7474
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE ridx_t (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO ridx_t VALUES (1),(2),(3)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX ridx_i ON ridx_t (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idxRel := corruptIndexForReindex(t, ctx, "ridx_i", otherDBOid)

	if err := runDDL(t, ctx, "REINDEX TABLE ridx_t"); err != nil {
		t.Fatalf("REINDEX TABLE in the connection's own database: %v", err)
	}
	assertIndexRebuilt(t, ctx, idxRel, 3)
}

// TestReindexSchemaResolvesConnectionDBOid pins the silent-no-op variant:
// REINDEX SCHEMA enumerated TablesInSchema under DefaultDBOid, found none
// of the connection's tables, and returned success having rebuilt nothing.
// The corrupt-then-rescan shape catches exactly that regression.
func TestReindexSchemaResolvesConnectionDBOid(t *testing.T) {
	const otherDBOid = 7575
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE ridx_t (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO ridx_t VALUES (1),(2),(3),(4)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX ridx_i ON ridx_t (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idxRel := corruptIndexForReindex(t, ctx, "ridx_i", otherDBOid)

	if err := runDDL(t, ctx, "REINDEX SCHEMA public"); err != nil {
		t.Fatalf("REINDEX SCHEMA in the connection's own database: %v", err)
	}
	assertIndexRebuilt(t, ctx, idxRel, 4)
}
