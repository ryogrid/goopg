package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// These tests pin PostgreSQL's MERGE duplicate-source cardinality rule
// (M0100-0007): when more than one source row matches the same target row and
// the WHEN MATCHED action actually modifies it (UPDATE or DELETE), MERGE raises
// SQLSTATE 21000 (ERRCODE_CARDINALITY_VIOLATION) "MERGE command cannot affect
// row a second time" with hint "Ensure that not more than one source row
// matches any one target row." — see PostgreSQL
// src/backend/executor/nodeModifyTable.c ExecMergeMatched. goopg detects the
// duplicate during the target scan (operators_merge.go, hasDuplicate) and
// raises the error only AFTER applying the first modification (and after any
// epqWait blocking), matching upstream's observable ordering.
//
// The cross-partition-move case is the specific scenario the
// unimplemented_feat.json M0100-0007 backlog entry flagged as unverified:
// the first source row's UPDATE relocates the target row to a different leaf
// partition (implemented as delete + re-insert), yet the second matching
// source row must still trip the duplicate guard.

// runMergeStmt runs a single MERGE statement end-to-end and returns any
// runtime error (unlike runSQL, which t.Fatals on error).
func runMergeStmt(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		return err
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return err
	}
	op, err := Build(plan)
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != EOF {
		return err
	}
	return op.Close()
}

// assertDupSourceError checks the error is the 21000 duplicate-source error
// carrying PostgreSQL's exact message and hint.
func assertDupSourceError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected MERGE duplicate-source error, got nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "21000" {
		t.Errorf("SQLSTATE = %q, want 21000", ee.Code)
	}
	if ee.Message != "MERGE command cannot affect row a second time" {
		t.Errorf("message = %q, want PG's duplicate-source message", ee.Message)
	}
	if ee.Hint != "Ensure that not more than one source row matches any one target row." {
		t.Errorf("hint = %q, want PG's duplicate-source hint", ee.Hint)
	}
}

func TestMergeDupSourceUpdateNonPartitioned(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TABLE tgt (id int PRIMARY KEY, v int)`,
		`INSERT INTO tgt VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	err := runMergeStmt(t, ctx,
		`MERGE INTO tgt USING (VALUES (1,100),(1,200)) AS s(id,v) ON tgt.id = s.id `+
			`WHEN MATCHED THEN UPDATE SET v = s.v`)
	assertDupSourceError(t, err)

	// The first matching source row's UPDATE is applied before the error is
	// raised — matching PG, which discards the whole statement on rollback but
	// applies the first action before detecting the duplicate.
	rows := runSQL(t, ctx, `SELECT id, v FROM tgt`)
	if len(rows) != 1 || rows[0][1].Int != 100 {
		t.Fatalf("post-state = %v, want single row v=100 (first source row applied)", rows)
	}
}

func TestMergeDupSourceDeleteNonPartitioned(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TABLE dt (id int PRIMARY KEY, v int)`,
		`INSERT INTO dt VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	err := runMergeStmt(t, ctx,
		`MERGE INTO dt USING (VALUES (1,0),(1,0)) AS s(id,v) ON dt.id = s.id `+
			`WHEN MATCHED THEN DELETE`)
	assertDupSourceError(t, err)
}

func TestMergeDupSourceUpdateWithinOnePartition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TABLE ptgt (id int, v int) PARTITION BY RANGE (id)`,
		`CREATE TABLE ptgt1 PARTITION OF ptgt FOR VALUES FROM (0) TO (100)`,
		`INSERT INTO ptgt VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	err := runMergeStmt(t, ctx,
		`MERGE INTO ptgt USING (VALUES (1,100),(1,200)) AS s(id,v) ON ptgt.id = s.id `+
			`WHEN MATCHED THEN UPDATE SET v = s.v`)
	assertDupSourceError(t, err)
	rows := runSQL(t, ctx, `SELECT id, v FROM ptgt`)
	if len(rows) != 1 || rows[0][1].Int != 100 {
		t.Fatalf("post-state = %v, want single row v=100", rows)
	}
}

// TestMergeDupSourceCrossPartitionMove is the M0100-0007 scenario: the first
// source row's UPDATE changes the partition key so the target row moves from
// ptgt1 to ptgt2 (delete + re-insert); the second matching source row must
// still trip the duplicate-source guard.
func TestMergeDupSourceCrossPartitionMove(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TABLE ptgt (id int, v int) PARTITION BY RANGE (id)`,
		`CREATE TABLE ptgt1 PARTITION OF ptgt FOR VALUES FROM (0) TO (100)`,
		`CREATE TABLE ptgt2 PARTITION OF ptgt FOR VALUES FROM (100) TO (200)`,
		`INSERT INTO ptgt VALUES (5, 10)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}
	err := runMergeStmt(t, ctx,
		`MERGE INTO ptgt USING (VALUES (5,150),(5,250)) AS s(oid,nid) ON ptgt.id = s.oid `+
			`WHEN MATCHED THEN UPDATE SET id = s.nid`)
	assertDupSourceError(t, err)

	// The first source row's UPDATE relocated the row to ptgt2 (id 5 -> 150)
	// before the duplicate for the second source row was detected.
	rows := runSQL(t, ctx, `SELECT id, v FROM ptgt`)
	if len(rows) != 1 || rows[0][0].Int != 150 {
		t.Fatalf("post-state = %v, want single row id=150 (row moved to ptgt2)", rows)
	}
}
