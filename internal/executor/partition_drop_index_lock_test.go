package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/parser"
)

// TestSelectLocksLeafPartitionIndexes verifies that a bare SELECT on a leaf
// partition takes a transaction-scoped AccessShare lock not only on the leaf
// table but on EVERY index of that leaf, mirroring PostgreSQL (get_relation_info
// opens — and AccessShare-locks for a read — all of a scanned relation's
// indexes). This is blocker 2 of the partition-drop-index-locking isolation spec
// (M0118-0008): the spec's s3getlocks lists the open SELECT holding AccessShare
// on the leaf's `..._subpart_child_id_idx` and `..._subpart_child_id_idx1`, which
// is what makes a concurrent DROP INDEX block behind the reader.
func TestSelectLocksLeafPartitionIndexes(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// Explicit-transaction backend so the AccessShare scan locks are held to
	// end-of-transaction (acquireScanReadLockTxn is transient in autocommit).
	const backend lmgr.BackendID = 91002
	ctx.TxnLockBackendID = backend
	defer tableLockMgr.ReleaseAll(backend)

	for _, s := range []string{
		"CREATE TABLE part_dil (id int) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart PARTITION OF part_dil FOR VALUES FROM (1) TO (100) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart_child PARTITION OF part_dil_subpart FOR VALUES FROM (1) TO (100)",
		"CREATE INDEX part_dil_idx ON part_dil(id)",
		"CREATE INDEX part_dil_subpart_idx ON part_dil_subpart(id)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	leafOID := func(name string) uint32 {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %q not found", name)
		}
		return tbl.OID
	}
	idxOID := func(name string) uint32 {
		idx, ok := cat.LookupIndex(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("index %q not found", name)
		}
		return idx.OID
	}
	leafTbl := leafOID("part_dil_subpart_child")
	leafIdx1 := idxOID("part_dil_subpart_child_id_idx")  // from part_dil_idx
	leafIdx2 := idxOID("part_dil_subpart_child_id_idx1") // from part_dil_subpart_idx

	if _, err := runQueryWithErr(ctx, "SELECT * FROM part_dil_subpart_child"); err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	held := make(map[uint32]bool)
	for _, h := range tableLockMgr.AllLocks() {
		if h.Backend == backend && h.Granted && h.Mode == lmgr.AccessShareLock &&
			h.Tag.Block == 0 && h.Tag.Offset == 0 {
			held[h.Tag.Rel] = true
		}
	}

	for _, w := range []struct {
		name string
		oid  uint32
	}{
		{"part_dil_subpart_child (leaf table)", leafTbl},
		{"part_dil_subpart_child_id_idx (leaf index)", leafIdx1},
		{"part_dil_subpart_child_id_idx1 (leaf index)", leafIdx2},
	} {
		if !held[w.oid] {
			t.Errorf("expected AccessShareLock on %s (oid %d); held=%v", w.name, w.oid, held)
		}
	}
}

// TestDropIndexLocksIndexRelationTree verifies that DROP INDEX (non-CONCURRENTLY)
// inside an explicit transaction AccessExclusive-locks the index relation itself
// AND every partition-descendant child index, in addition to the table tree.
// This is the lock half of the partition-drop-index-locking isolation spec
// (M0118-0008): the spec's s3getlocks output lists the dropped index
// `part_drop_index_locking_idx` plus its child indexes
// (`..._subpart_id_idx`, `..._subpart_child_id_idx`) as AccessExclusiveLock|t.
// Prior to this change lockDropIndexTableTree only locked the heap partition
// tree, so the index relations never appeared in pg_locks.
func TestDropIndexLocksIndexRelationTree(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// An explicit-transaction backend so acquireDDLLockTxn takes real locks
	// (it is a no-op when TxnLockBackendID==0, i.e. autocommit).
	const backend lmgr.BackendID = 91001
	ctx.TxnLockBackendID = backend
	defer tableLockMgr.ReleaseAll(backend)

	for _, s := range []string{
		"CREATE TABLE part_dil (id int) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart PARTITION OF part_dil FOR VALUES FROM (1) TO (100) PARTITION BY RANGE(id)",
		"CREATE TABLE part_dil_subpart_child PARTITION OF part_dil_subpart FOR VALUES FROM (1) TO (100)",
		"CREATE INDEX part_dil_idx ON part_dil(id)",
		"CREATE INDEX part_dil_subpart_idx ON part_dil_subpart(id)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	// Capture index OIDs before the drop removes them from the catalog.
	oidOf := func(name string) uint32 {
		idx, ok := cat.LookupIndex(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("index %q not found", name)
		}
		return idx.OID
	}
	topIdx := oidOf("part_dil_idx")
	childOnSubpart := oidOf("part_dil_subpart_id_idx")        // top index -> subpart
	childOnLeaf := oidOf("part_dil_subpart_child_id_idx")     // top index chain -> leaf
	otherLeafIdx := oidOf("part_dil_subpart_child_id_idx1")   // part_dil_subpart_idx -> leaf

	// DROP INDEX on the top partitioned index.
	if err := runDDL(t, ctx, "DROP INDEX part_dil_idx"); err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}

	// Collect the AccessExclusive locks now held by our backend (locks outlive the
	// catalog mutation — they are keyed by OID and released at COMMIT/ROLLBACK).
	held := make(map[uint32]bool)
	for _, h := range tableLockMgr.AllLocks() {
		if h.Backend == backend && h.Granted && h.Mode == lmgr.AccessExclusiveLock &&
			h.Tag.Block == 0 && h.Tag.Offset == 0 {
			held[h.Tag.Rel] = true
		}
	}

	// The dropped index relation and its two partition-descendant child indexes
	// must be locked.
	for _, w := range []struct {
		name string
		oid  uint32
	}{
		{"part_dil_idx (target index relation)", topIdx},
		{"part_dil_subpart_id_idx (child index)", childOnSubpart},
		{"part_dil_subpart_child_id_idx (descendant child index)", childOnLeaf},
	} {
		if !held[w.oid] {
			t.Errorf("expected AccessExclusiveLock on %s (oid %d); held=%v", w.name, w.oid, held)
		}
	}

	// A child index that belongs to a *different* index tree
	// (part_dil_subpart_idx, not dropped here) must NOT be locked by this drop.
	if held[otherLeafIdx] {
		t.Errorf("DROP INDEX part_dil_idx wrongly locked unrelated child index oid %d (part_dil_subpart_child_id_idx1)", otherLeafIdx)
	}

	// And the heap partition tree is still locked (regression guard for the
	// pre-existing lockDropIndexTableTree behaviour).
	tblOID := func(name string) uint32 {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %q not found", name)
		}
		return tbl.OID
	}
	for _, name := range []string{"part_dil", "part_dil_subpart", "part_dil_subpart_child"} {
		if !held[tblOID(name)] {
			t.Errorf("expected AccessExclusiveLock on heap partition %q", name)
		}
	}
}
