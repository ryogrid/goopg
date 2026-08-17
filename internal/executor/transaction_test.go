package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

func runTransactionStmt(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): got %d statements", sql, len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], catalog.NewInMemory())
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

func TestTransactionBeginCommitLifecycle(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	sess := NewBasicSession()
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if !sess.InExplicitTransaction() {
		t.Fatal("session should be in explicit transaction after BEGIN")
	}
	if ctx.TxnMgr.ActiveCount() != 1 {
		t.Fatalf("active count=%d want=1", ctx.TxnMgr.ActiveCount())
	}
	tx, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("CurrentTransaction should be set after BEGIN")
	}
	// M0093: BEGIN returns a tx with a non-zero Handle but XID = 0
	// (read-only fast-path). Snapshot does NOT include this tx in
	// InProgress because there's no XID to track yet.
	if tx.Handle == 0 {
		t.Fatal("Handle must be allocated by BEGIN")
	}
	if tx.XID != 0 {
		t.Fatalf("xid=%d want 0 (read-only — XID only materialised on first write)", tx.XID)
	}
	if ctx.Tx.Handle != tx.Handle {
		t.Fatalf("ctx.Tx.Handle=%d want=%d", ctx.Tx.Handle, tx.Handle)
	}

	if err := runTransactionStmt(t, ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if sess.InExplicitTransaction() {
		t.Fatal("session should exit explicit transaction after COMMIT")
	}
	if ctx.TxnMgr.ActiveCount() != 0 {
		t.Fatalf("active count=%d want=0", ctx.TxnMgr.ActiveCount())
	}
	if ctx.Tx.XID != 0 {
		t.Fatalf("ctx.Tx should be cleared, got xid=%d", ctx.Tx.XID)
	}

	// COMMIT outside a transaction is a no-op.
	if err := runTransactionStmt(t, ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT outside tx: %v", err)
	}
}

func TestTransactionRollbackLifecycle(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	ctx.Session = NewBasicSession()

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if err := runTransactionStmt(t, ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	if ctx.TxnMgr.ActiveCount() != 0 {
		t.Fatalf("active count=%d want=0", ctx.TxnMgr.ActiveCount())
	}
	if ctx.Session.InExplicitTransaction() {
		t.Fatal("session should not remain in explicit tx after ROLLBACK")
	}

	// ROLLBACK outside a transaction is a no-op.
	if err := runTransactionStmt(t, ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK outside tx: %v", err)
	}
}

func TestTransactionBeginInsideTransactionIsNoOp(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	sess := NewBasicSession()
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN 1: %v", err)
	}
	tx1, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("expected active transaction after first BEGIN")
	}

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN 2: %v", err)
	}
	tx2, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("expected active transaction after second BEGIN")
	}
	if tx1.XID != tx2.XID {
		t.Fatalf("nested BEGIN should keep xid, got %d then %d", tx1.XID, tx2.XID)
	}
	if ctx.TxnMgr.ActiveCount() != 1 {
		t.Fatalf("active count=%d want=1", ctx.TxnMgr.ActiveCount())
	}
}

func TestTransactionBeginUsesSessionIsolation(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	sess := NewBasicSession()
	if err := sess.SetIsolationLevel(transam.IsolationRepeatableRead); err != nil {
		t.Fatal(err)
	}
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	tx, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("expected active transaction after BEGIN")
	}
	if tx.Isolation != transam.IsolationRepeatableRead {
		t.Fatalf("tx isolation=%v want=%v", tx.Isolation, transam.IsolationRepeatableRead)
	}
}

// TestTransactionBeginSerializableSession pins M0104-0001: a session
// configured to SERIALIZABLE acquires a Transaction whose Isolation tag
// is IsolationSerializable, not IsolationRepeatableRead. Downstream SSI
// hooks (M0104-0002+) branch on this tag, so it must round-trip through
// the executor BEGIN path.
func TestTransactionBeginSerializableSession(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	sess := NewBasicSession()
	if err := sess.SetIsolationLevel(transam.IsolationSerializable); err != nil {
		t.Fatal(err)
	}
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	tx, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("expected active transaction after BEGIN")
	}
	if tx.Isolation != transam.IsolationSerializable {
		t.Fatalf("tx isolation=%v want=%v", tx.Isolation, transam.IsolationSerializable)
	}
}

func TestTransactionRequiresContextHooks(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = transam.NewManager()
	err := runTransactionStmt(t, ctx, "BEGIN")
	if err == nil {
		t.Fatal("expected BEGIN to fail without Session")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "XX000" {
		t.Fatalf("err=%v want ExecError XX000", err)
	}
}
