package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestRunCopyToTableDefaultColumns: COPY items TO STDOUT emits all
// declared columns in declared order.
func TestRunCopyToTableDefaultColumns(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// M0129-S8.3: advance the command counter so COPY sees the seed rows.
	advanceStmtCounter(ctx)

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyTo,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdout,
	}
	var lines []string
	count, _, err := RunCopyTo(ctx, plan, func(b []byte) error {
		lines = append(lines, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("count=%d want 3", count)
	}
	want := []string{"1\talpha\n", "2\tbeta\n", "3\tgamma\n"}
	if len(lines) != len(want) {
		t.Fatalf("lines=%d want %d", len(lines), len(want))
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line[%d]=%q want %q", i, lines[i], w)
		}
	}
}

// TestRunCopyToTableProjectionAndReorder: COPY items (label, id) TO
// STDOUT projects + reorders the SeqScan output, so each line lists
// label first.
func TestRunCopyToTableProjectionAndReorder(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// M0129-S8.3: advance the command counter so COPY sees the seed rows.
	advanceStmtCounter(ctx)

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyTo,
		Table:       tbl,
		ColumnIndex: []int{1, 0},
		Endpoint:    optimizer.CopyEndpointStdout,
	}
	var got []string
	if _, _, err := RunCopyTo(ctx, plan, func(b []byte) error {
		got = append(got, string(b))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha\t1\n", "beta\t2\n", "gamma\t3\n"}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestRunCopyToQueryForm: COPY (SELECT 1) TO STDOUT emits exactly
// "1\n" — same shape as the existing wire-protocol test. Driven
// through the real parser+planner pipeline so the test exercises
// the same call site the wire layer will use.
func TestRunCopyToQueryForm(t *testing.T) {
	ctx := NewContext()
	stmts, err := parser.Parse("COPY (SELECT 1) TO STDOUT")
	if err != nil {
		t.Fatal(err)
	}
	cat := catalog.NewInMemory()
	node, err := optimizer.Plan(stmts[0], cat)
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := node.(*optimizer.Copy)
	if !ok {
		t.Fatalf("planner returned %T, want *planner.Copy", node)
	}
	var lines []string
	count, _, err := RunCopyTo(ctx, plan, func(b []byte) error {
		lines = append(lines, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(lines) != 1 || lines[0] != "1\n" {
		t.Errorf("count=%d lines=%v", count, lines)
	}
}

// TestCopyFromExecutorRoundTrip drives a CopyFromExecutor with two
// pgbench-shaped lines and verifies they materialise via SeqScan.
func TestCopyFromExecutorRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := cf.PushLine([]byte("10\thello")); err != nil {
		t.Fatal(err)
	}
	if err := cf.PushLine([]byte("20\tworld")); err != nil {
		t.Fatal(err)
	}
	if got := cf.RowsInserted(); got != 2 {
		t.Errorf("RowsInserted=%d want 2", got)
	}

	// Read back via the existing CopyTo path.
	toPlan := &optimizer.Copy{
		Direction:   optimizer.CopyTo,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdout,
	}
	var out []string
	if _, _, err := RunCopyTo(ctx, toPlan, func(b []byte) error {
		out = append(out, string(b))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"10\thello\n", "20\tworld\n"}
	if strings.Join(out, "") != strings.Join(want, "") {
		t.Errorf("readback got %v want %v", out, want)
	}
}

// TestCopyFromExecutorBadFieldCount surfaces a clean SQLSTATE 22P04
// error when the line's field count doesn't match the column list.
func TestCopyFromExecutorBadFieldCount(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	err = cf.PushLine([]byte("only-one-field"))
	if err == nil {
		t.Fatal("expected error")
	}
	xe, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T want *ExecError", err)
	}
	if xe.Code != "22P04" {
		t.Errorf("code=%q want 22P04", xe.Code)
	}
}

// TestRunCopyToFileEndpointRejected: file/PROGRAM endpoints should be
// rejected with a stable feature_not_supported error rather than
// silently succeeding.
func TestRunCopyToFileEndpointRejected(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	for _, ep := range []optimizer.CopyEndpoint{optimizer.CopyEndpointFile, optimizer.CopyEndpointProgram} {
		plan := &optimizer.Copy{
			Direction:   optimizer.CopyTo,
			Table:       tbl,
			ColumnIndex: []int{0, 1},
			Endpoint:    ep,
		}
		_, _, err := RunCopyTo(ctx, plan, func([]byte) error { return nil })
		if err == nil {
			t.Errorf("ep=%v: expected error", ep)
			continue
		}
		xe, ok := err.(*ExecError)
		if !ok || xe.Code != "0A000" {
			t.Errorf("ep=%v: got %T %v want 0A000", ep, err, err)
		}
	}
}
