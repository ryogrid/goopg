package executor

import (
	"strings"
	"testing"
)

// TestToastUpdateSetSelfReferenceDetoasts is the M0134-0129 discovery
// (indirect_toast.sql): an UPDATE whose SET-clause references a TOASTed
// column of the row being updated (`SET f1 = '-'||f1||'-'`) must evaluate
// against the real detoasted value, not a raw KindToastPointer datum.
// Before the fix, the scan row handed to SET-expression evaluation still
// carried the unresolved 12-byte TOAST pointer, so string concatenation
// stringified the *pointer* via AppendValueText's `?datum kind=N?`
// fallback and durably wrote that garbage back as the column's new value
// — silent, permanent data corruption (not just a display bug: a fresh
// SELECT after VACUUM FREEZE kept showing the garbage). See
// docs/design/m0134-0129-toast-update-selfref.md.
func TestToastUpdateSetSelfReferenceDetoasts(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE indtoasttest(descr text, cnt int DEFAULT 0, f1 text, f2 text)"); err != nil {
		t.Fatal(err)
	}

	f1 := strings.Repeat("1234567890", 30000) // 300000 bytes, well over ToastThreshold
	f2 := strings.Repeat("1234567890", 50000) // 500000 bytes
	insertSQL := "INSERT INTO indtoasttest(descr, f1, f2) VALUES('two-toasted', '" + f1 + "', '" + f2 + "')"
	if _, err := runQueryWithErr(ctx, insertSQL); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Sanity: the freshly-inserted row detoasts correctly on a plain scan.
	rows := runQuery(t, ctx, "SELECT f1, f2 FROM indtoasttest")
	if len(rows) != 1 || rows[0][0].StringValue() != f1 || rows[0][1].StringValue() != f2 {
		t.Fatalf("pre-UPDATE sanity SELECT mismatch")
	}

	// UPDATE that concatenates the TOASTed column with itself, RETURNING the
	// whole-row cast so both the in-command RETURNING projection and the
	// on-disk write are exercised in one statement.
	retRows := runQuery(t, ctx, "UPDATE indtoasttest SET cnt = cnt + 1, f1 = '-'||f1||'-' RETURNING f1, f2")
	if len(retRows) != 1 {
		t.Fatalf("expected 1 RETURNING row, got %d", len(retRows))
	}
	wantF1 := "-" + f1 + "-"
	if got := retRows[0][0].StringValue(); got != wantF1 {
		t.Errorf("RETURNING f1: want %d bytes starting %q, got %d bytes starting %q",
			len(wantF1), wantF1[:20], len(got), firstN(got, 20))
	}
	if got := retRows[0][1].StringValue(); got != f2 {
		t.Errorf("RETURNING f2 (untouched column): want %d bytes, got %d bytes starting %q",
			len(f2), len(got), firstN(got, 20))
	}

	// Re-SELECT (fresh scan, post-commit-equivalent within this fixture) must
	// show the same correct values — proves the on-disk write itself carries
	// the real value, not just the in-command RETURNING projection.
	rows = runQuery(t, ctx, "SELECT f1, f2 FROM indtoasttest")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row post-UPDATE, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != wantF1 {
		t.Errorf("post-UPDATE SELECT f1: want %d bytes starting %q, got %d bytes starting %q",
			len(wantF1), wantF1[:20], len(got), firstN(got, 20))
	}
	if got := rows[0][1].StringValue(); got != f2 {
		t.Errorf("post-UPDATE SELECT f2 (untouched column): want %d bytes, got %d bytes starting %q",
			len(f2), len(got), firstN(got, 20))
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
