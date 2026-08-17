package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
)

// TestConnTxStateSnapshotLocalRoleIfNeeded pins SnapshotLocalRoleIfNeeded's
// no-op / capture-once contract in isolation from the wire protocol:
// non-LOCAL and no-active-transaction calls are no-ops, and only the FIRST
// LOCAL call within a transaction captures a snapshot (mirroring
// PostgreSQL's GUC_ACTION_LOCAL stack, guc.c, at a flat non-nested
// fidelity). M0119-0004.
func TestConnTxStateSnapshotLocalRoleIfNeeded(t *testing.T) {
	c := &connTxState{}

	c.SnapshotLocalRoleIfNeeded(false)
	if c.LocalRolePriorValue != nil {
		t.Fatal("non-LOCAL call must not snapshot")
	}

	c.SnapshotLocalRoleIfNeeded(true)
	if c.LocalRolePriorValue != nil {
		t.Fatal("LOCAL call with no active explicit transaction must not snapshot")
	}

	mgr := transam.NewManager()
	tx, err := mgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	c.Begin(tx)

	c.NonSuperuserRole = "role_a"
	c.SnapshotLocalRoleIfNeeded(true)
	if c.LocalRolePriorValue == nil || *c.LocalRolePriorValue != "role_a" {
		t.Fatalf("first LOCAL call: LocalRolePriorValue = %v, want *\"role_a\"", c.LocalRolePriorValue)
	}

	// A second LOCAL call in the same transaction must not move the
	// snapshot: it should still point at "role_a" (the value from before the
	// FIRST local change), not "role_b".
	c.NonSuperuserRole = "role_b"
	c.SnapshotLocalRoleIfNeeded(true)
	if c.LocalRolePriorValue == nil || *c.LocalRolePriorValue != "role_a" {
		t.Fatalf("second LOCAL call moved the snapshot: LocalRolePriorValue = %v, want *\"role_a\"", c.LocalRolePriorValue)
	}
}

// TestConnTxStateEndRestoresLocalRole pins End()'s restore-and-clear
// contract: a pending LOCAL role snapshot is applied to NonSuperuserRole and
// the pointer is cleared, so it can never leak into the connection's next
// transaction. End() is the shared COMMIT/ROLLBACK teardown path, so one
// exercise covers both (the commit-vs-rollback distinction lives in the
// caller, outside connTxState). M0119-0004.
func TestConnTxStateEndRestoresLocalRole(t *testing.T) {
	c := &connTxState{}
	mgr := transam.NewManager()
	tx, err := mgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	c.Begin(tx)

	c.SnapshotLocalRoleIfNeeded(true)
	c.NonSuperuserRole = "role_a"

	c.End()

	if c.NonSuperuserRole != "" {
		t.Errorf("after End(): NonSuperuserRole = %q, want restored to %q", c.NonSuperuserRole, "")
	}
	if c.LocalRolePriorValue != nil {
		t.Errorf("after End(): LocalRolePriorValue = %v, want nil (cleared)", c.LocalRolePriorValue)
	}

	// A transaction with no LOCAL role change leaves NonSuperuserRole
	// untouched by End() (the non-LOCAL persistence path is unaffected).
	tx2, err := mgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	c.Begin(tx2)
	c.NonSuperuserRole = "role_persisted"
	c.End()
	if c.NonSuperuserRole != "role_persisted" {
		t.Errorf("after End() with no LOCAL snapshot: NonSuperuserRole = %q, want %q (must persist)", c.NonSuperuserRole, "role_persisted")
	}
}
