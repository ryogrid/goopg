package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// The ON COMMIT {DELETE ROWS|DROP} end-of-transaction feature for temp tables
// (M0134-0072). Facet tests: registration at CREATE TEMP TABLE, the commit-time
// pass (RunOnCommitActions), the 42P16 non-temp guard, the CTAS path, and the
// FK-compat 0A000 message. Mirrors tablecmds.c register_on_commit_action /
// PreCommit_on_commit_actions and catalog/heap.c:3738.

// onCommitFixture returns a fixture context whose Session is a BasicSession so
// CREATE TEMP TABLE ... ON COMMIT registrations land and the commit pass can be
// driven directly.
func onCommitFixture(t *testing.T) (*Context, *BasicSession, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	sess := NewBasicSession()
	ctx.Session = sess
	return ctx, sess, cleanup
}

func TestOnCommitDeleteRowsTruncatesAtCommit(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) ON COMMIT DELETE ROWS"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	actions := sess.OnCommitActions()
	if len(actions) != 1 || actions[0].Action != parser.OnCommitDeleteRows {
		t.Fatalf("registered=%+v, want one DELETE ROWS entry", actions)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("temp table t not in catalog")
	}
	if err := runDDL(t, ctx, "INSERT INTO t (a) VALUES (1), (2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v", err)
	}
	rows := scanAllRows(t, ctx, tbl)
	if len(rows) != 0 {
		t.Fatalf("post-commit rows=%d, want 0 (ON COMMIT DELETE ROWS truncated)", len(rows))
	}
	// The DELETE ROWS entry persists across commits (PG preserves committed
	// entries; AtEOXact_on_commit_actions, tablecmds.c:19427): it must fire
	// again at the next commit.
	if got := len(sess.OnCommitActions()); got != 1 {
		t.Errorf("entries after commit=%d, want 1 (persists across commits)", got)
	}
}

func TestOnCommitDropDropsAtCommit(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	if got := len(sess.OnCommitActions()); got != 1 {
		t.Fatalf("registered=%d, want 1", got)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); !ok {
		t.Fatal("temp table t should exist before commit")
	}

	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); ok {
		t.Error("temp table t still in catalog after ON COMMIT DROP")
	}
	// The DROP entry was consumed by the drop itself (dropTableByRefImmediate →
	// RemoveOnCommitAction, mirroring remove_on_commit_action at heap.c:1902).
	if got := len(sess.OnCommitActions()); got != 0 {
		t.Errorf("entries after drop=%d, want 0", got)
	}
}

func TestOnCommitNonTempGuard42P16(t *testing.T) {
	ctx, _, cleanup := onCommitFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLE t (a int) ON COMMIT DELETE ROWS")
	if err == nil {
		t.Fatal("non-temp CREATE TABLE ... ON COMMIT: expected error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err=%T, want *ExecError", err)
	}
	if ee.Code != "42P16" {
		t.Errorf("Code=%q, want 42P16", ee.Code)
	}
	if ee.Message != "ON COMMIT can only be used on temporary tables" {
		t.Errorf("Message=%q, want %q", ee.Message, "ON COMMIT can only be used on temporary tables")
	}
}

func TestOnCommitCTASDrop(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	// CTAS lookahead: optional ON COMMIT between the (col) alias list and AS.
	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (col) ON COMMIT DROP AS SELECT 1"); err != nil {
		t.Fatalf("CTAS with ON COMMIT: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); !ok {
		t.Fatal("CTAS temp table t should exist before commit")
	}
	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); ok {
		t.Error("CTAS temp table t still in catalog after ON COMMIT DROP")
	}
}

func TestOnCommitDeleteRowsFKMessage(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int PRIMARY KEY) ON COMMIT DELETE ROWS"); err != nil {
		t.Fatalf("CREATE TEMP TABLE t: %v", err)
	}
	// A second table references t via FK but is NOT in the ON COMMIT set.
	if err := runDDL(t, ctx, "CREATE TEMP TABLE c (a int REFERENCES t(a))"); err != nil {
		t.Fatalf("CREATE TEMP TABLE c: %v", err)
	}

	err := RunOnCommitActions(ctx, sess)
	if err == nil {
		t.Fatal("expected ON COMMIT FK error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err=%T, want *ExecError", err)
	}
	if ee.Code != "0A000" {
		t.Errorf("Code=%q, want 0A000", ee.Code)
	}
	if ee.Message != "unsupported ON COMMIT and foreign key combination" {
		t.Errorf("Message=%q, want %q", ee.Message, "unsupported ON COMMIT and foreign key combination")
	}
	if !strings.Contains(ee.Detail, "do not have the same ON COMMIT setting") {
		t.Errorf("Detail=%q, want it to mention the ON COMMIT setting", ee.Detail)
	}
	if ee.Hint != "" {
		t.Errorf("Hint=%q, want empty (tempTables branch has no HINT)", ee.Hint)
	}
}

func TestOnCommitDropRemovedByManualTableDrop(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	// Dropping the temp table before commit must remove its ON COMMIT entry
	// (mirrors remove_on_commit_action via heap.c:1902) so a later commit pass
	// does not try to drop a gone table.
	if err := runDDL(t, ctx, "DROP TABLE t"); err != nil {
		t.Fatalf("DROP TABLE: %v", err)
	}
	if got := len(sess.OnCommitActions()); got != 0 {
		t.Errorf("entries after DROP TABLE=%d, want 0", got)
	}
	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions after manual drop: %v", err)
	}
}

// TestOnCommitDropPartitionParentCascadesWithoutError: temp.sql's "Using ON
// COMMIT DROP on a parent removes the whole set" — the parent (DROP) cascades to
// a DELETE ROWS partition and a DROP partition. The explicit DROP on the
// already-cascaded partition must be a no-op (PG's PERFORM_DELETION_QUIETLY,
// tablecmds.c:19394), not an error.
func TestOnCommitDropPartitionParentCascadesWithoutError(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) PARTITION BY LIST (a) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE partitioned temp table: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TEMP TABLE t1 PARTITION OF t FOR VALUES IN (1) ON COMMIT DELETE ROWS"); err != nil {
		t.Fatalf("CREATE partition t1: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TEMP TABLE t2 PARTITION OF t FOR VALUES IN (2) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE partition t2: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1), (2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v (PG drops the whole set without error)", err)
	}
	for _, n := range []string{"t", "t1", "t2"} {
		if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: n}); ok {
			t.Errorf("%s still in catalog after ON COMMIT DROP on parent", n)
		}
	}
}

// TestOnCommitDeleteRowsPartitionParentPreservesPreserveRowsPartition: temp.sql's
// "ON COMMIT DELETE does not remove all rows if partitions preserve their data".
// The DELETE ROWS pass truncates ONLY the registered relations and skips
// storage-less partitioned tables (heap_truncate_one_rel returns early for
// RELKIND_PARTITIONED_TABLE, catalog/heap.c:3631); a PRESERVE ROWS partition
// keeps its row, a DROP partition goes away.
func TestOnCommitDeleteRowsPartitionParentPreservesPreserveRowsPartition(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) PARTITION BY LIST (a) ON COMMIT DELETE ROWS"); err != nil {
		t.Fatalf("CREATE partitioned temp table: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TEMP TABLE t1 PARTITION OF t FOR VALUES IN (1) ON COMMIT PRESERVE ROWS"); err != nil {
		t.Fatalf("CREATE preserve partition: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TEMP TABLE t2 PARTITION OF t FOR VALUES IN (2) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE drop partition: %v", err)
	}
	// The direct-op fixture does not route INSERT INTO t (partitioned parent) to
	// its partitions, so insert into each partition's heap directly.
	if err := runDDL(t, ctx, "INSERT INTO t1 (a) VALUES (1)"); err != nil {
		t.Fatalf("INSERT into preserve partition: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t2 (a) VALUES (2)"); err != nil {
		t.Fatalf("INSERT into drop partition: %v", err)
	}

	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v", err)
	}
	// t2 (DROP) is gone; t (storage-less parent) and t1 (PRESERVE ROWS) remain.
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t2"}); ok {
		t.Error("t2 (ON COMMIT DROP) still in catalog")
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"}); !ok {
		t.Fatal("parent t missing after commit")
	}
	t1, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t1"})
	if !ok {
		t.Fatal("preserve partition t1 missing after commit")
	}
	rows := scanAllRows(t, ctx, t1)
	if len(rows) != 1 {
		t.Fatalf("t1 post-commit rows=%d, want 1 (PRESERVE ROWS partition keeps its row)", len(rows))
	}
}

// TestOnCommitInheritanceChildDrop: the `()` empty-column-list INHERITS child
// form (temp.sql's second inheritance scenario — parent DELETE ROWS, child DROP).
// The child's ON COMMIT DROP must be registered (parser captures it via
// consumeCreateTableSuffix) and fire at commit; the parent survives truncated.
func TestOnCommitInheritanceChildDrop(t *testing.T) {
	ctx, sess, cleanup := onCommitFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TEMP TABLE t (a int) ON COMMIT DELETE ROWS"); err != nil {
		t.Fatalf("CREATE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TEMP TABLE c () INHERITS (t) ON COMMIT DROP"); err != nil {
		t.Fatalf("CREATE child: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO c VALUES (1)"); err != nil {
		t.Fatalf("INSERT child: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (2)"); err != nil {
		t.Fatalf("INSERT parent: %v", err)
	}
	if got := len(sess.OnCommitActions()); got != 2 {
		t.Fatalf("registered=%d, want 2 (parent DELETE ROWS + child DROP)", got)
	}

	if err := RunOnCommitActions(ctx, sess); err != nil {
		t.Fatalf("RunOnCommitActions: %v", err)
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "c"}); ok {
		t.Error("child c (ON COMMIT DROP) still in catalog")
	}
	// Parent survives with DELETE ROWS applied (its own rows gone).
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	if !ok {
		t.Fatal("parent t missing after commit")
	}
	if rows := scanAllRows(t, ctx, tbl); len(rows) != 0 {
		t.Errorf("parent rows=%d, want 0 (ON COMMIT DELETE ROWS)", len(rows))
	}
}

// scanAllRows drains a sequential scan over tbl.
func scanAllRows(t *testing.T, ctx *Context, tbl *catalog.Table) []Row {
	t.Helper()
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, _ := drainScan(scan)
	return rows
}
