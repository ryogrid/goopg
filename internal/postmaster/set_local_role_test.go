package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// TestSimpleProtocolSetLocalRoleAloneDoesNotError pins one half of the
// M0119-0004 fix: `SET LOCAL ROLE <name>` sent as its own simple-query
// message (the common client shape — psql and most drivers send one
// statement per message) previously had no dedicated case in
// server/query.go's fast-path switch, so it fell through to the generic
// "SET LOCAL " handler, which mis-parsed "ROLE <name>" as GUC name "role"
// and failed with `unrecognized configuration parameter "role"` — "role" is
// not a config.Registry variable; SET ROLE is tracked entirely via
// connTx.NonSuperuserRole. `SET LOCAL SESSION AUTHORIZATION` already had a
// dedicated case; `SET LOCAL ROLE` did not.
func TestSimpleProtocolSetLocalRoleAloneDoesNotError(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET LOCAL ROLE some_nonsuper_role")
	for _, f := range readUntilReady(t, conn) {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("SET LOCAL ROLE (no explicit txn): unexpected ErrorResponse: %s", f.Payload)
		}
	}
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after SET LOCAL ROLE: is_superuser=%q, want %q", got, "off")
	}
}

// TestSimpleProtocolSetLocalRoleRevertsAtCommitAndRollback pins the other
// half of M0119-0004: `SET LOCAL ROLE` / `SET LOCAL SESSION AUTHORIZATION`
// inside an explicit transaction must revert to the pre-transaction role at
// both COMMIT and ROLLBACK — PostgreSQL's GUC_ACTION_LOCAL stack (guc.c) —
// whereas a plain (non-LOCAL) `SET ROLE` must keep persisting past COMMIT
// exactly as before (config.SessionRegistry's existing ordinary-GUC LOCAL
// layer is the fidelity target: flat, non-nested, first-LOCAL-wins).
func TestSimpleProtocolSetLocalRoleRevertsAtCommitAndRollback(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("initial is_superuser=%q, want %q", got, "on")
	}

	// SET LOCAL ROLE inside an explicit transaction, then COMMIT: must revert.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL ROLE some_nonsuper_role")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("in txn after SET LOCAL ROLE: is_superuser=%q, want %q", got, "off")
	}
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after COMMIT: is_superuser=%q, want %q (SET LOCAL ROLE must revert)", got, "on")
	}

	// SET LOCAL SESSION AUTHORIZATION inside an explicit transaction, then
	// ROLLBACK: must also revert.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL SESSION AUTHORIZATION some_nonsuper_role")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("in txn after SET LOCAL SESSION AUTHORIZATION: is_superuser=%q, want %q", got, "off")
	}
	writeQuery(t, conn, "ROLLBACK")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after ROLLBACK: is_superuser=%q, want %q (SET LOCAL SESSION AUTHORIZATION must revert)", got, "on")
	}

	// A second SET LOCAL ROLE in the same transaction must not move the
	// restore target: the transaction still reverts to the value from BEFORE
	// the first LOCAL change (the bootstrap superuser), not to the first
	// LOCAL role.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL ROLE some_nonsuper_role")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET LOCAL ROLE another_nonsuper_role")
	readUntilReady(t, conn)
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after COMMIT of two chained SET LOCAL ROLE: is_superuser=%q, want %q", got, "on")
	}

	// Regression guard: a plain (non-LOCAL) SET ROLE must keep persisting
	// past COMMIT — only LOCAL gets transaction-scoped revert.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET ROLE some_nonsuper_role")
	readUntilReady(t, conn)
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "off" {
		t.Fatalf("after COMMIT of non-LOCAL SET ROLE: is_superuser=%q, want %q (must persist)", got, "off")
	}
	writeQuery(t, conn, "RESET ROLE")
	readUntilReady(t, conn)
	if got := queryIsSuperuser(t, conn); got != "on" {
		t.Fatalf("after RESET ROLE: is_superuser=%q, want %q", got, "on")
	}
}
