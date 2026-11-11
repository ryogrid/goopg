package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
)

// TestConnTxStateRollbackRevertsSessionRole pins M0148-0001j3's
// AtEOXact_GUC(isCommit=false) role revert: a session-level SET ROLE / SET
// SESSION AUTHORIZATION made inside an explicit transaction is undone by
// ROLLBACK (and kept by COMMIT), and the transaction-level snapshot wins
// over a SET LOCAL snapshot taken later in the same transaction.
func TestConnTxStateRollbackRevertsSessionRole(t *testing.T) {
	mgr := transam.NewManager()
	begin := func(c *connTxState) {
		tx, err := mgr.Begin(transam.IsolationReadCommitted)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		c.Begin(tx)
	}
	c := &connTxState{LoginUser: "postgres", SessionUser: "postgres"}

	// BEGIN; SET ROLE r3; ROLLBACK → no role.
	begin(c)
	applySetRole(c, "r3")
	c.RestoreTxRoleForRollback()
	c.End()
	if c.NonSuperuserRole != "" || c.SetRoleIsActive {
		t.Fatalf("after ROLLBACK: role=%q active=%v, want none", c.NonSuperuserRole, c.SetRoleIsActive)
	}

	// BEGIN; SET ROLE r3; COMMIT → r3 stays.
	begin(c)
	applySetRole(c, "r3")
	c.End()
	if c.NonSuperuserRole != "r3" || !c.SetRoleIsActive {
		t.Fatalf("after COMMIT: role=%q active=%v, want r3 active", c.NonSuperuserRole, c.SetRoleIsActive)
	}

	// SET ROLE r4 (outside); BEGIN; SET ROLE r3; SET LOCAL ROLE x; ROLLBACK → r4.
	applySetRole(c, "r4")
	begin(c)
	applySetRole(c, "r3")
	c.SnapshotLocalRoleIfNeeded(true)
	applySetRole(c, "x")
	c.RestoreTxRoleForRollback()
	c.End()
	if c.NonSuperuserRole != "r4" {
		t.Fatalf("after ROLLBACK with SET LOCAL: role=%q, want r4", c.NonSuperuserRole)
	}

	// BEGIN; SET SESSION AUTHORIZATION r3; ROLLBACK → session user restored.
	applySetRole(c, "")
	begin(c)
	applySetSessionAuthorization(c, "r3")
	c.RestoreTxRoleForRollback()
	c.End()
	if c.SessionUser != "postgres" || c.NonSuperuserRole != "" {
		t.Fatalf("after ROLLBACK of SET SESSION AUTHORIZATION: session=%q role=%q", c.SessionUser, c.NonSuperuserRole)
	}
}
