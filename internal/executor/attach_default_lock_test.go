package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
)

// TestAttachPartitionLocksDefaultPartition verifies that ALTER TABLE … ATTACH
// PARTITION (of a non-default partition), issued inside an explicit transaction,
// takes a transaction-scoped AccessExclusiveLock on the parent's existing
// DEFAULT partition — mirroring PostgreSQL ATExecAttachPartition, which locks
// the default partition first because the new partition narrows the default's
// implicit constraint:
//
//	defaultPartOid = get_default_oid_from_partdesc(...);
//	if (OidIsValid(defaultPartOid))
//	    LockRelationOid(defaultPartOid, AccessExclusiveLock);
//
// This is piece (b) of the partition-concurrent-attach isolation spec
// (M0118-0008): a concurrent INSERT that routes to the default (RowExclusiveLock)
// must block behind the still-open attach until it commits. Prior to this change
// the attach took no lock on the default, so the concurrent insert never waited.
func TestAttachPartitionLocksDefaultPartition(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// Explicit-transaction backend so acquireDDLLockTxn takes a real lock held
	// to end-of-transaction (it is a no-op when TxnLockBackendID==0, autocommit).
	const backend lockmgr.BackendID = 91003
	ctx.TxnLockBackendID = backend
	defer tableLockMgr.ReleaseAll(backend)

	for _, s := range []string{
		"CREATE TABLE tpa (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tpa_def PARTITION OF tpa DEFAULT",
		"CREATE TABLE tpa_1 PARTITION OF tpa FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE tpa_2 (LIKE tpa)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	oidOf := func(name string) uint32 {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %q not found", name)
		}
		return tbl.OID
	}
	defOID := oidOf("tpa_def")
	nondefOID := oidOf("tpa_1")

	// Attach a new non-default partition (no default-conflict rows, so the
	// attach itself succeeds).
	if err := runDDL(t, ctx, "ALTER TABLE tpa ATTACH PARTITION tpa_2 FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("ATTACH PARTITION: %v", err)
	}

	held := make(map[uint32]bool)
	for _, h := range tableLockMgr.AllLocks() {
		if h.Backend == backend && h.Granted && h.Mode == lockmgr.AccessExclusiveLock &&
			h.Tag.Block == 0 && h.Tag.Offset == 0 {
			held[h.Tag.Rel] = true
		}
	}

	if !held[defOID] {
		t.Errorf("expected AccessExclusiveLock on the DEFAULT partition tpa_def (oid %d); held=%v", defOID, held)
	}
	// The attach must not AccessExclusive-lock an unrelated, already-attached
	// non-default sibling partition.
	if held[nondefOID] {
		t.Errorf("ATTACH wrongly AccessExclusive-locked non-default sibling tpa_1 (oid %d)", nondefOID)
	}
}

// TestAttachPartitionNoDefaultNoLock is the control: attaching to a parent that
// has no DEFAULT partition takes no AccessExclusiveLock on any sibling (there is
// nothing whose constraint narrows).
func TestAttachPartitionNoDefaultNoLock(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	const backend lockmgr.BackendID = 91004
	ctx.TxnLockBackendID = backend
	defer tableLockMgr.ReleaseAll(backend)

	for _, s := range []string{
		"CREATE TABLE tpb (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tpb_1 PARTITION OF tpb FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE tpb_2 (LIKE tpb)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	sibOID := func() uint32 {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "tpb_1"})
		if !ok {
			t.Fatal("tpb_1 not found")
		}
		return tbl.OID
	}()

	if err := runDDL(t, ctx, "ALTER TABLE tpb ATTACH PARTITION tpb_2 FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("ATTACH PARTITION: %v", err)
	}

	for _, h := range tableLockMgr.AllLocks() {
		if h.Backend == backend && h.Granted && h.Mode == lockmgr.AccessExclusiveLock &&
			h.Tag.Rel == sibOID && h.Tag.Block == 0 && h.Tag.Offset == 0 {
			t.Errorf("ATTACH with no default partition wrongly AccessExclusive-locked sibling tpb_1 (oid %d)", sibOID)
		}
	}
}

// hasPartChild reports whether parentOID's registered partition-child set
// contains childOID.
func hasPartChild(im *catalog.InMemory, parentOID, childOID uint32) bool {
	for _, c := range im.PartitionChildren(parentOID) {
		if c.OID == childOID {
			return true
		}
	}
	return false
}

// TestAttachPartitionDeferredUntilCommit verifies piece (a) of the
// partition-concurrent-attach isolation spec (M0118-0008): ALTER TABLE … ATTACH
// PARTITION issued inside an explicit transaction defers its catalog
// registration (partition bounds + parent-child link) to COMMIT. Until the
// attaching transaction commits, the new partition is NOT registered, so a
// concurrent session does not see it and an INSERT routing through the parent
// falls to the DEFAULT partition — mirroring PG transactional-DDL visibility (an
// uncommitted ATTACH is invisible to other snapshots). ApplyPendingPartitionAttaches
// performs the real registration at commit.
func TestAttachPartitionDeferredUntilCommit(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	// Build the partition tree in autocommit (Session nil) so only the ATTACH
	// under test exercises the deferral.
	for _, s := range []string{
		"CREATE TABLE tpc (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tpc_def PARTITION OF tpc DEFAULT",
		"CREATE TABLE tpc_1 PARTITION OF tpc FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE tpc_2 (LIKE tpc)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	oidOf := func(name string) uint32 {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %q not found", name)
		}
		return tbl.OID
	}
	parentOID := oidOf("tpc")
	childOID := oidOf("tpc_2")

	// Enter an explicit transaction: the attach must now defer.
	sess := NewBasicSession()
	sess.BeginExplicitTransaction(ctx.Tx, ctx.Snap)
	ctx.Session = sess
	const backend lockmgr.BackendID = 91005
	ctx.TxnLockBackendID = backend
	defer tableLockMgr.ReleaseAll(backend)

	if err := runDDL(t, ctx, "ALTER TABLE tpc ATTACH PARTITION tpc_2 FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("ATTACH PARTITION: %v", err)
	}

	// Deferred: the child is not yet registered (invisible to other sessions).
	if hasPartChild(im, parentOID, childOID) {
		t.Fatalf("ATTACH inside a transaction registered tpc_2 immediately; want deferred to COMMIT")
	}
	if got := len(sess.pendingPartAttaches); got != 1 {
		t.Fatalf("pendingPartAttaches len=%d, want 1", got)
	}

	// Commit applies the registration.
	ApplyPendingPartitionAttaches(ctx, sess)
	if !hasPartChild(im, parentOID, childOID) {
		t.Fatalf("ApplyPendingPartitionAttaches did not register tpc_2 at commit")
	}
	if got := len(sess.pendingPartAttaches); got != 0 {
		t.Fatalf("pendingPartAttaches len=%d after apply, want 0 (drained)", got)
	}
	childTbl, _ := im.LookupTableByOID(childOID)
	if len(childTbl.PartitionBounds) != 1 || childTbl.PartitionBounds[0].From == "" {
		t.Fatalf("child partition bounds not set at commit: %+v", childTbl.PartitionBounds)
	}
}

// TestAttachPartitionImmediateInAutocommit is the control: ATTACH PARTITION in
// autocommit (no explicit transaction) registers the partition immediately, as
// before — the deferral applies only inside an explicit transaction.
func TestAttachPartitionImmediateInAutocommit(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)

	for _, s := range []string{
		"CREATE TABLE tpd (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tpd_def PARTITION OF tpd DEFAULT",
		"CREATE TABLE tpd_2 (LIKE tpd)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	parentOID := func() uint32 {
		tbl, _ := cat.LookupTable(parser.ObjectName{Name: "tpd"})
		return tbl.OID
	}()
	childOID := func() uint32 {
		tbl, _ := cat.LookupTable(parser.ObjectName{Name: "tpd_2"})
		return tbl.OID
	}()

	if err := runDDL(t, ctx, "ALTER TABLE tpd ATTACH PARTITION tpd_2 FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("ATTACH PARTITION: %v", err)
	}
	if !hasPartChild(im, parentOID, childOID) {
		t.Fatalf("autocommit ATTACH did not register tpd_2 immediately")
	}
}
