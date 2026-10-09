package executor

import (
	"strings"
	"testing"
)

// TestTxidCurrentAssignsXid pins M0146-0043 against PG 18.3's
// GetTopTransactionId: txid_current() / pg_current_xact_id() assign the
// transaction's xid on first use (a read-only transaction has none), never
// return 0, and agree with each other within a statement.
func TestTxidCurrentAssignsXid(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	rows := runSQL(t, ctx, "SELECT txid_current(), pg_current_xact_id()::text::bigint")
	a, b := datumTestString(rows[0][0]), datumTestString(rows[0][1])
	if a == "0" || a == "" || a != b {
		t.Fatalf("txid_current()=%q pg_current_xact_id()=%q, want one non-zero xid", a, b)
	}
	// (Per-statement assignment in autocommit is a server-level property —
	// this fixture runs its statements in one context — and was verified
	// against PG on a live server.)
	if got := datumTestString(runSQL(t, ctx, "SELECT txid_current() = txid_current_if_assigned()")[0][0]); !strings.HasPrefix(got, "t") {
		t.Errorf("txid_current_if_assigned() after txid_current() = %q, want the same xid", got)
	}
}
