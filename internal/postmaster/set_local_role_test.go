package postmaster

import (
	"net"
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

// showValue runs `SHOW name` over the simple protocol and returns the single
// resulting cell as a string. Test helper shared with queryIsSuperuser's
// pattern (extended_set_role_test.go).
func showValue(t *testing.T, conn net.Conn, name string) string {
	t.Helper()
	writeQuery(t, conn, "SHOW "+name)
	frames := readUntilReady(t, conn)
	for _, f := range frames {
		if f.Type == libpq.MsgDataRow {
			row := decodeDataRow(t, f.Payload)
			if len(row) != 1 {
				t.Fatalf("SHOW %s: cell count=%d, want 1", name, len(row))
			}
			return string(row[0])
		}
	}
	t.Fatalf("SHOW %s: no DataRow in %+v", name, frames)
	return ""
}

// TestSimpleProtocolPlainSetRevertsOnRollback is the SQL-level end-to-end
// guard for 0134-0001 P6/S15: a plain (non-LOCAL) `SET` inside an explicit
// transaction that ROLLBACKs must revert to the pre-BEGIN value, exercising
// the Context.EndLocalTransaction(committed) hook rewiring (Part 1) together
// with the SessionRegistry undo journal (Part 2) — not just the registry in
// isolation. min_parallel_table_scan_size's PostgreSQL default is 8MB.
func TestSimpleProtocolPlainSetRevertsOnRollback(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	if got := showValue(t, conn, "min_parallel_table_scan_size"); got != "8MB" {
		t.Fatalf("initial min_parallel_table_scan_size=%q, want %q", got, "8MB")
	}

	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET min_parallel_table_scan_size = 0")
	readUntilReady(t, conn)
	if got := showValue(t, conn, "min_parallel_table_scan_size"); got != "0" {
		t.Fatalf("in txn after SET: min_parallel_table_scan_size=%q, want %q", got, "0")
	}
	writeQuery(t, conn, "ROLLBACK")
	readUntilReady(t, conn)
	if got := showValue(t, conn, "min_parallel_table_scan_size"); got != "8MB" {
		t.Fatalf("after ROLLBACK: min_parallel_table_scan_size=%q, want %q (plain SET must revert)", got, "8MB")
	}

	// Regression guard: a plain SET inside a COMMITted transaction stays.
	writeQuery(t, conn, "BEGIN")
	readUntilReady(t, conn)
	writeQuery(t, conn, "SET min_parallel_table_scan_size = 0")
	readUntilReady(t, conn)
	writeQuery(t, conn, "COMMIT")
	readUntilReady(t, conn)
	if got := showValue(t, conn, "min_parallel_table_scan_size"); got != "0" {
		t.Fatalf("after COMMIT: min_parallel_table_scan_size=%q, want %q (plain SET must persist)", got, "0")
	}
}
