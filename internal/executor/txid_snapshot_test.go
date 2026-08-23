package executor

import "testing"

// TestTxidSnapshotCastValidation pins the txid_snapshot/pg_snapshot text-form
// validation landed for M0134-0080. Mirrors parse_snapshot (xid8funcs.c):
// xmin/xmax must be nonzero with xmin<=xmax, and each xip value must lie in
// [xmin,xmax) and be non-decreasing (equal-valued duplicates collapse rather
// than reject). Cases captured from the PG 18.3 reference cluster (port
// 65432) via regress-sql txid.sql, 2026-08-23.
func TestTxidSnapshotCastValidation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	valid := []struct {
		sql  string
		want string
	}{
		{"SELECT '12:13:'::txid_snapshot", "12:13:"},
		{"SELECT '12:18:14,16'::txid_snapshot", "12:18:14,16"},
		// Equal-valued duplicate xip collapses to one entry.
		{"SELECT '12:16:14,14'::txid_snapshot", "12:16:14"},
		{"SELECT txid_snapshot '1000100010001000:1000100010001100:1000100010001012,1000100010001013'",
			"1000100010001000:1000100010001100:1000100010001012,1000100010001013"},
	}
	for _, c := range valid {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}

	invalid := []string{
		// xmin==0 (InvalidFullTransactionId sentinel).
		"SELECT '0:1:'::txid_snapshot",
		// xip == xmax boundary (must be strictly < xmax).
		"SELECT '12:13:0'::txid_snapshot",
		// xmin > xmax.
		"SELECT '31:12:'::txid_snapshot",
		// out-of-order xip (13 after 14).
		"SELECT '12:16:14,13'::txid_snapshot",
		// 64-bit overflow on xmax.
		"SELECT txid_snapshot '1:9223372036854775808:3'",
	}
	for _, sql := range invalid {
		t.Run(sql, func(t *testing.T) {
			_, err := runQueryErr(t, ctx, sql)
			if err == nil {
				t.Fatalf("%s: expected error, got none", sql)
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("%s: got %T, want *ExecError", sql, err)
			}
			if ee.Code != "22P02" {
				t.Errorf("%s: SQLSTATE = %q, want 22P02", sql, ee.Code)
			}
		})
	}
}

// TestTxidSnapshotFunctions pins the txid_snapshot_xmin/xmax and
// txid_visible_in_snapshot function arms landed for M0134-0080 (mirrors
// pg_snapshot_xmin/pg_snapshot_xmax/pg_visible_in_snapshot, xid8funcs.c).
func TestTxidSnapshotFunctions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT txid_snapshot_xmin('12:20:13,15,18'::txid_snapshot)", "12"},
		{"SELECT txid_snapshot_xmax('12:20:13,15,18'::txid_snapshot)", "20"},
		// Below xmin: always visible.
		{"SELECT txid_visible_in_snapshot(11, '12:20:13,15,18'::txid_snapshot)", "t"},
		// At/above xmax: never visible.
		{"SELECT txid_visible_in_snapshot(20, '12:20:13,15,18'::txid_snapshot)", "f"},
		// In [xmin,xmax) and listed in xip: in-progress, not visible.
		{"SELECT txid_visible_in_snapshot(13, '12:20:13,15,18'::txid_snapshot)", "f"},
		// In [xmin,xmax) but not listed: committed, visible.
		{"SELECT txid_visible_in_snapshot(14, '12:20:13,15,18'::txid_snapshot)", "t"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

// TestTxidCurrentIfAssigned pins txid_current_if_assigned()'s NULL-vs-value
// split: NULL when ctx.Tx.XID is unset (mirroring
// GetTopFullTransactionIdIfAny returning an invalid FullTransactionId before
// the transaction has taken an xid), the same value txid_current() returns
// once it has. M0134-0080 (pg_proc OID 3348).
func TestTxidCurrentIfAssigned(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "SELECT txid_current_if_assigned() IS NULL")
	if len(rows) != 1 || rows[0][0].Format() != "t" {
		t.Fatalf("txid_current_if_assigned() before any xid = %v, want NULL", rows)
	}

	ctx.Tx.XID = 42
	rows = runQuery(t, ctx, "SELECT txid_current(), txid_current_if_assigned()")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got, want := rows[0][1].Format(), rows[0][0].Format(); got != want {
		t.Errorf("txid_current_if_assigned() = %q, want txid_current() = %q", got, want)
	}
	if rows[0][0].Format() != "42" {
		t.Errorf("txid_current() = %q, want \"42\"", rows[0][0].Format())
	}
}
