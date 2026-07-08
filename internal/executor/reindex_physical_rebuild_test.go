package executor

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestReindexIndexPhysicallyRebuilds is the non-vacuous DoD test for
// M0122-0007's REINDEX physical rebuild slice: plain REINDEX INDEX must
// actually repopulate the index's on-disk btree pages from the heap, not
// merely validate the name and return (the pre-existing behavior). The
// index's storage is deliberately corrupted (truncated to zero blocks,
// simulating on-disk damage) before REINDEX runs — if REINDEX were still the
// old no-op, the corruption would persist and the post-REINDEX btree scan
// below would find nothing.
func TestReindexIndexPhysicallyRebuilds(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for _, v := range []string{"1", "2", "3", "4", "5"} {
		if err := runDDL(t, ctx, "INSERT INTO t VALUES ("+v+")"); err != nil {
			t.Fatalf("INSERT %s: %v", v, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_idx ON t (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "t_idx"})
	if !ok {
		t.Fatalf("t_idx not found after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)

	// Corrupt the index in place: truncate its storage to zero blocks (as if
	// the on-disk file were damaged) and drop it from the buffer pool cache so
	// a later Pin observes the truncated state rather than a stale page.
	if err := ctx.Pool.Manager().TruncateRelation(idxRel); err != nil {
		t.Fatalf("TruncateRelation: %v", err)
	}
	ctx.Pool.InvalidateRel(idxRel)
	if n, err := ctx.Pool.NBlocks(idxRel); err != nil || n != 0 {
		t.Fatalf("pre-REINDEX sanity: NBlocks = %d, %v, want 0, nil", n, err)
	}

	if err := runDDL(t, ctx, "REINDEX INDEX t_idx"); err != nil {
		t.Fatalf("REINDEX INDEX: %v", err)
	}

	bt, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatalf("btree.Open after REINDEX: %v", err)
	}
	var got []storage.ItemPointer
	if err := bt.RangeScan(nil, nil, func(key []byte, ptr storage.ItemPointer) (bool, error) {
		got = append(got, ptr)
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("post-REINDEX entry count = %d, want 5 (REINDEX did not physically rebuild the index)", len(got))
	}
}

// TestReindexTablePhysicallyRebuildsAllIndexes covers the REINDEX TABLE form
// (rebuild every btree index on the table) and confirms a catalog-only
// access method (gist — no physical storage in goopg, same as CREATE INDEX)
// is silently skipped rather than erroring.
func TestReindexTablePhysicallyRebuildsAllIndexes(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (a int4, b int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for _, v := range []string{"(1,10)", "(2,20)", "(3,30)"} {
		if err := runDDL(t, ctx, "INSERT INTO t VALUES "+v); err != nil {
			t.Fatalf("INSERT %s: %v", v, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_a_idx ON t (a)"); err != nil {
		t.Fatalf("CREATE INDEX t_a_idx: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_b_idx ON t USING gist (b)"); err != nil {
		t.Fatalf("CREATE INDEX t_b_idx (gist): %v", err)
	}

	aIdx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "t_a_idx"})
	aRel := ctx.Catalog.IndexRelFileNode(aIdx)
	if err := ctx.Pool.Manager().TruncateRelation(aRel); err != nil {
		t.Fatalf("TruncateRelation(t_a_idx): %v", err)
	}
	ctx.Pool.InvalidateRel(aRel)

	if err := runDDL(t, ctx, "REINDEX TABLE t"); err != nil {
		t.Fatalf("REINDEX TABLE: %v", err)
	}

	bt, err := btree.Open(ctx.Pool, aRel)
	if err != nil {
		t.Fatalf("btree.Open(t_a_idx) after REINDEX TABLE: %v", err)
	}
	var count int
	if err := bt.RangeScan(nil, nil, func(key []byte, ptr storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if count != 3 {
		t.Fatalf("t_a_idx post-REINDEX TABLE entry count = %d, want 3", count)
	}
}

// TestReindexSchemaPhysicallyRebuildsAllTables is the non-vacuous DoD test for
// M0122-0007's REINDEX SCHEMA physical-rebuild follow-up: plain REINDEX
// SCHEMA must rebuild every btree index on every table in the schema (reusing
// rebuildTableIndexes per relation), not merely reproduce the schema's
// locking behavior while leaving corrupted storage untouched (the pre-fix
// behavior). Two tables are each given a truncated (simulated-corrupt) btree
// index; REINDEX SCHEMA must repair both.
func TestReindexSchemaPhysicallyRebuildsAllTables(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SCHEMA rs"); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE rs.t1 (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE rs.t1: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE rs.t2 (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE rs.t2: %v", err)
	}
	for _, v := range []string{"1", "2", "3"} {
		if err := runDDL(t, ctx, "INSERT INTO rs.t1 VALUES ("+v+")"); err != nil {
			t.Fatalf("INSERT rs.t1 %s: %v", v, err)
		}
		if err := runDDL(t, ctx, "INSERT INTO rs.t2 VALUES ("+v+")"); err != nil {
			t.Fatalf("INSERT rs.t2 %s: %v", v, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX t1_idx ON rs.t1 (id)"); err != nil {
		t.Fatalf("CREATE INDEX t1_idx: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t2_idx ON rs.t2 (id)"); err != nil {
		t.Fatalf("CREATE INDEX t2_idx: %v", err)
	}

	idx1, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "rs", Name: "t1_idx"})
	if !ok {
		t.Fatalf("t1_idx not found after CREATE INDEX")
	}
	idx2, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "rs", Name: "t2_idx"})
	if !ok {
		t.Fatalf("t2_idx not found after CREATE INDEX")
	}
	rel1 := ctx.Catalog.IndexRelFileNode(idx1)
	rel2 := ctx.Catalog.IndexRelFileNode(idx2)
	for _, rel := range []storage.RelFileNode{rel1, rel2} {
		if err := ctx.Pool.Manager().TruncateRelation(rel); err != nil {
			t.Fatalf("TruncateRelation(%v): %v", rel, err)
		}
		ctx.Pool.InvalidateRel(rel)
	}

	if err := runDDL(t, ctx, "REINDEX SCHEMA rs"); err != nil {
		t.Fatalf("REINDEX SCHEMA: %v", err)
	}

	for name, rel := range map[string]storage.RelFileNode{"t1_idx": rel1, "t2_idx": rel2} {
		bt, err := btree.Open(ctx.Pool, rel)
		if err != nil {
			t.Fatalf("btree.Open(%s) after REINDEX SCHEMA: %v", name, err)
		}
		var count int
		if err := bt.RangeScan(nil, nil, func(key []byte, ptr storage.ItemPointer) (bool, error) {
			count++
			return true, nil
		}); err != nil {
			t.Fatalf("RangeScan(%s): %v", name, err)
		}
		if count != 3 {
			t.Fatalf("%s post-REINDEX SCHEMA entry count = %d, want 3 (REINDEX SCHEMA did not physically rebuild this table's index)", name, count)
		}
	}
}

// TestReindexIndexBlocksBehindConcurrentIndexReader confirms the new REINDEX
// lock is genuinely held (not the wait-only-then-release pattern
// acquireRelLockMaybeTransient uses elsewhere): a session holding an ACCESS
// SHARE lock on the index (e.g. a live index scan) must block REINDEX INDEX
// from another backend until it releases, mirroring real PostgreSQL's plain-
// REINDEX locking (reindex.sgml: ACCESS EXCLUSIVE on the index itself).
func TestReindexIndexBlocksBehindConcurrentIndexReader(t *testing.T) {
	ctx, cleanup := newFSMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_idx ON t (id)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Schema: "public", Name: "t_idx"})
	if !ok {
		t.Fatalf("t_idx not found after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	idxTag := lockmgr.LockTag{DB: idxRel.DBOid, Rel: idxRel.RelOid}

	const readerBackend lockmgr.BackendID = 424242
	if err := tableLockMgr.Acquire(context.Background(), readerBackend, idxTag, lockmgr.AccessShareLock); err != nil {
		t.Fatalf("reader Acquire: %v", err)
	}

	ctx.BackendID = 909090 // nonzero so acquireReindexLocks actually engages
	done := make(chan error, 1)
	go func() { done <- runDDL(t, ctx, "REINDEX INDEX t_idx") }()

	select {
	case err := <-done:
		t.Fatalf("REINDEX INDEX completed while a concurrent AccessShareLock reader still held the index (err=%v) — the new lock is not actually exclusive", err)
	case <-time.After(150 * time.Millisecond):
	}

	tableLockMgr.Release(readerBackend, idxTag, lockmgr.AccessShareLock)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("REINDEX INDEX after reader released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REINDEX INDEX never completed after the conflicting reader released its lock")
	}
}
