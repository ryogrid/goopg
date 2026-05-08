package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// newSavepointFixture spins up a storage fixture with an explicit transaction
// and a BasicSession wired into the context — required for savepoint tests.
func newSavepointFixture(t *testing.T) (*Context, *catalog.InMemory, *BasicSession, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "val", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	mgrMVCC := mvcc.NewManager()
	sess := NewBasicSession()

	tx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	sess.BeginExplicitTransaction(tx, snap)

	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.Session = sess

	cleanup := func() {
		if sess.InExplicitTransaction() {
			tx2, _, _ := sess.CurrentTransaction()
			_ = mgrMVCC.Rollback(tx2)
		}
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, sess, cleanup
}

// runSavepointStmt parses and executes a single savepoint-family statement
// (SAVEPOINT / RELEASE / ROLLBACK TO SAVEPOINT) against ctx.
func runSavepointStmt(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): got %d statements", sql, len(stmts))
	}
	plan, err := planner.Plan(stmts[0], catalog.NewInMemory())
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	_, err = Run(op, ctx)
	return err
}

// insertRow inserts a single row into the items table using the current ctx.Tx.XID.
func insertRow(t *testing.T, ctx *Context, cat catalog.Catalog, id int64, val string) {
	t.Helper()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	ins := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{&planner.IntegerConst{Value: id}, &planner.StringConst{Value: val}},
			},
		},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(ins)
	if err != nil {
		t.Fatalf("Build insert: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Insert.Open: %v", err)
	}
	if _, err := NextRow(op); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()
}

// seqScanAll returns all visible rows from items in the given context.
func seqScanAll(t *testing.T, ctx *Context, cat catalog.Catalog) []Row {
	t.Helper()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	scan := &planner.SeqScan{Table: tbl}
	op, err := Build(scan)
	if err != nil {
		t.Fatalf("Build seqscan: %v", err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("SeqScan Run: %v", err)
	}
	return rows
}

// TestSavepointDoD verifies the worked example from the design doc:
//
//	BEGIN; INSERT a; SAVEPOINT s; INSERT b; ROLLBACK TO s; INSERT c; COMMIT
//
// After COMMIT, only rows 'a' and 'c' are visible; 'b' was aborted.
func TestSavepointDoD(t *testing.T) {
	ctx, cat, sess, cleanup := newSavepointFixture(t)
	defer cleanup()

	// INSERT a (uses top-level XID)
	insertRow(t, ctx, cat, 1, "a")
	topXID := sess.tx.XID

	// SAVEPOINT s — allocates sub-XID, updates ctx.Tx.XID
	if err := runSavepointStmt(t, ctx, "SAVEPOINT s"); err != nil {
		t.Fatalf("SAVEPOINT s: %v", err)
	}
	subXID1 := ctx.Tx.XID
	if subXID1 == topXID {
		t.Fatal("SAVEPOINT must allocate a distinct sub-XID")
	}

	// INSERT b (uses sub-XID subXID1)
	insertRow(t, ctx, cat, 2, "b")

	// Within savepoint: all three rows should be visible to current session
	snap2, err := ctx.TxnMgr.SnapshotFor(sess.tx)
	if err != nil {
		t.Fatalf("SnapshotFor mid-subxact: %v", err)
	}
	ctx.Snap = snap2
	rows := seqScanAll(t, ctx, cat)
	if len(rows) != 2 {
		t.Fatalf("before ROLLBACK TO: want 2 rows, got %d", len(rows))
	}

	// ROLLBACK TO s — marks subXID1 aborted, fresh sub-XID
	if err := runSavepointStmt(t, ctx, "ROLLBACK TO SAVEPOINT s"); err != nil {
		t.Fatalf("ROLLBACK TO s: %v", err)
	}
	subXID2 := ctx.Tx.XID
	if subXID2 == subXID1 {
		t.Fatal("ROLLBACK TO must allocate a new sub-XID for the fresh entry")
	}

	// INSERT c (uses sub-XID subXID2)
	insertRow(t, ctx, cat, 3, "c")

	// Within the fresh subxact: only 'a' (top-level) and 'c' (subXID2) should
	// be visible. 'b' (subXID1) is aborted.
	snap3, err := ctx.TxnMgr.SnapshotFor(sess.tx)
	if err != nil {
		t.Fatalf("SnapshotFor after rollback: %v", err)
	}
	ctx.Snap = snap3
	rows = seqScanAll(t, ctx, cat)
	if len(rows) != 2 {
		t.Fatalf("after ROLLBACK TO (within tx): want 2 rows, got %d: %v", len(rows), rows)
	}
	wantVals := map[string]bool{"a": true, "c": true}
	for _, r := range rows {
		v := r[1].StringValue()
		if !wantVals[v] {
			t.Errorf("unexpected row val %q in post-rollback scan", v)
		}
	}

	// COMMIT — commits top-level transaction
	tx, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("no current transaction")
	}
	if err := ctx.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sess.EndExplicitTransaction()

	// Begin a new read transaction and verify only 'a' and 'c' are visible.
	tx2, err := ctx.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin tx2: %v", err)
	}
	defer func() { _ = ctx.TxnMgr.Rollback(tx2) }()
	snap4, err := ctx.TxnMgr.SnapshotFor(tx2)
	if err != nil {
		t.Fatalf("SnapshotFor tx2: %v", err)
	}
	ctx.Tx = tx2
	ctx.Snap = snap4
	// No Session needed for a plain seqscan.
	ctx.Session = nil

	rows = seqScanAll(t, ctx, cat)
	if len(rows) != 2 {
		t.Fatalf("post-commit: want 2 rows (a, c), got %d: %v", len(rows), rows)
	}
	for _, r := range rows {
		v := r[1].StringValue()
		if !wantVals[v] {
			t.Errorf("post-commit: unexpected row val %q", v)
		}
	}
}

// TestSavepointOutsideTransaction verifies SAVEPOINT / RELEASE / ROLLBACK TO
// return SQLSTATE 25P01 when called outside an explicit transaction block.
func TestSavepointOutsideTransaction(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	sess := NewBasicSession()
	ctx.Session = sess

	for _, sql := range []string{
		"SAVEPOINT x",
		"RELEASE SAVEPOINT x",
		"ROLLBACK TO SAVEPOINT x",
	} {
		err := runSavepointStmt(t, ctx, sql)
		if err == nil {
			t.Errorf("%q: expected error, got nil", sql)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%q: want *ExecError, got %T: %v", sql, err, err)
			continue
		}
		if ee.Code != "25P01" {
			t.Errorf("%q: want SQLSTATE 25P01, got %s", sql, ee.Code)
		}
	}
}

// TestSavepointReleaseNotFound verifies RELEASE on a non-existent savepoint
// returns SQLSTATE 3B001.
func TestSavepointReleaseNotFound(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	sess := NewBasicSession()
	ctx.Session = sess

	// Begin a transaction so we're in the tx block.
	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}

	err := runSavepointStmt(t, ctx, "RELEASE SAVEPOINT nosuchpoint")
	if err == nil {
		t.Fatal("expected error for non-existent savepoint")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "3B001" {
		t.Errorf("want SQLSTATE 3B001, got %v", err)
	}
}

// TestSavepointPlannerParsesVerbs verifies the planner correctly translates
// all three savepoint AST node types into Transaction plan nodes.
func TestSavepointPlannerParsesVerbs(t *testing.T) {
	cases := []struct {
		sql  string
		verb planner.TransactionVerb
		name string
	}{
		{"SAVEPOINT sp1", planner.TxSavepoint, "sp1"},
		{"RELEASE SAVEPOINT sp1", planner.TxRelease, "sp1"},
		{"ROLLBACK TO SAVEPOINT sp1", planner.TxRollbackTo, "sp1"},
		{"RELEASE sp1", planner.TxRelease, "sp1"},
		{"ROLLBACK TO sp1", planner.TxRollbackTo, "sp1"},
	}
	for _, tc := range cases {
		stmts, err := parser.Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		node, err := planner.Plan(stmts[0], catalog.NewInMemory())
		if err != nil {
			t.Errorf("Plan(%q): %v", tc.sql, err)
			continue
		}
		txn, ok := node.(*planner.Transaction)
		if !ok {
			t.Errorf("%q: want *planner.Transaction, got %T", tc.sql, node)
			continue
		}
		if txn.Verb != tc.verb {
			t.Errorf("%q: verb=%d want %d", tc.sql, txn.Verb, tc.verb)
		}
		if txn.Name != tc.name {
			t.Errorf("%q: name=%q want %q", tc.sql, txn.Name, tc.name)
		}
	}
}
