package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// newBinaryCopyExecutor plans `sql` (a COPY ... FROM STDIN WITH (FORMAT
// BINARY) statement) and returns the executor the wire protocol would drive.
func newBinaryCopyExecutor(t *testing.T, ctx *Context, sql string) *CopyFromExecutor {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	cp, ok := plan.(*optimizer.Copy)
	if !ok {
		t.Fatalf("Plan(%q) produced %T, want *optimizer.Copy", sql, plan)
	}
	fe, err := NewCopyFromExecutor(ctx, cp)
	if err != nil {
		t.Fatalf("NewCopyFromExecutor: %v", err)
	}
	return fe
}

// binaryCopyStream frames rows the way a client does: header, one row per
// entry (already-encoded field payloads), trailer.
func binaryCopyStream(t *testing.T, cols []catalog.Column, rows []Row) []byte {
	t.Helper()
	out := CopyBinaryHeader()
	for _, r := range rows {
		var err error
		out, err = AppendCopyBinaryRow(out, r, cols)
		if err != nil {
			t.Fatalf("AppendCopyBinaryRow: %v", err)
		}
	}
	return AppendCopyBinaryTrailer(out)
}

// TestCopyBinaryAppliesDefaultsAndConstraints is the review/260831-2 EC-4
// guard. PushBinaryData scattered each decoded row straight into
// writeHeapRowReturning, inlining its own copy of the text path's write and
// skipping everything in between: DEFAULT filling for unlisted columns, NOT
// NULL, CHECK and domain constraints. PG runs one CopyFrom() loop for all
// three formats (copyfrom.c: defmap/defexprs fill omitted columns and
// ExecConstraints runs per row regardless of format), so a binary stream was a
// way to put rows into a table that PG would have rejected — and to store NULL
// where PG stores a default.
func TestCopyBinaryAppliesDefaultsAndConstraints(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE bc (a int NOT NULL, b int DEFAULT 7, c int CHECK (c > 0))"); err != nil {
		t.Fatal(err)
	}

	// (1) An unlisted column must take its DEFAULT, not NULL.
	fe := newBinaryCopyExecutor(t, ctx, "COPY bc (a, c) FROM STDIN WITH (FORMAT BINARY)")
	listed := fe.listedColumns()
	stream := binaryCopyStream(t, listed, []Row{{
		{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 5},
	}})
	if _, err := fe.PushBinaryData(stream); err != nil {
		t.Fatalf("PushBinaryData: %v", err)
	}
	rows := runQuery(t, ctx, "SELECT a, b, c FROM bc")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][1].IsNull() || rows[0][1].Int != 7 {
		t.Errorf("column b = %v, want the DEFAULT 7", rows[0][1])
	}

	// (2) A NULL in a NOT NULL column must be rejected (23502).
	fe = newBinaryCopyExecutor(t, ctx, "COPY bc (a, c) FROM STDIN WITH (FORMAT BINARY)")
	listed = fe.listedColumns()
	stream = binaryCopyStream(t, listed, []Row{{NullDatum, {Kind: KindInt, Int: 5}}})
	_, err := fe.PushBinaryData(stream)
	if err == nil {
		t.Error("binary COPY of a NULL into a NOT NULL column succeeded, want 23502")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "23502" {
		t.Errorf("NOT NULL violation error = %v, want SQLSTATE 23502", err)
	}

	// (3) A CHECK violation must be rejected (23514).
	fe = newBinaryCopyExecutor(t, ctx, "COPY bc (a, c) FROM STDIN WITH (FORMAT BINARY)")
	listed = fe.listedColumns()
	stream = binaryCopyStream(t, listed, []Row{{
		{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: -1},
	}})
	_, err = fe.PushBinaryData(stream)
	if err == nil {
		t.Error("binary COPY of a CHECK-violating row succeeded, want 23514")
	} else if ee, ok := err.(*ExecError); !ok || !strings.HasPrefix(ee.Code, "23") {
		t.Errorf("CHECK violation error = %v, want a class-23 SQLSTATE", err)
	}

	// Neither rejected row may have landed.
	rows = runQuery(t, ctx, "SELECT a FROM bc")
	if len(rows) != 1 {
		t.Errorf("table holds %d rows, want only the first (valid) one", len(rows))
	}
}
