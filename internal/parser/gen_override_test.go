package parser

import (
	"testing"
)

func TestPartitionColGeneratedExprParsed(t *testing.T) {
	sql := `CREATE TABLE parttbl2 PARTITION OF parttbl
  (d WITH OPTIONS GENERATED ALWAYS AS (a + b + 1000) STORED)
  FOR VALUES IN (2)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(stmts) == 0 {
		t.Fatal("no stmts")
	}
	ct := stmts[0].(*CreateTableStmt)
	poc := ct.PartitionOf
	if poc == nil {
		t.Fatal("no PartitionOf")
	}
	if len(poc.ColGeneratedExprs) != 1 {
		t.Fatalf("expected 1 ColGeneratedExprs, got %d: %v", len(poc.ColGeneratedExprs), poc.ColGeneratedExprs)
	}
	cg := poc.ColGeneratedExprs[0]
	if cg.ColName != "d" {
		t.Errorf("expected ColName=d, got %q", cg.ColName)
	}
	if cg.Expr != "a + b + 1000" {
		t.Errorf("expected Expr='a + b + 1000', got %q", cg.Expr)
	}
}

// TestPartitionChildWithStorageParams covers DU-002 slice 191: a leaf partition
// child may carry a WITH (storage params) clause after FOR VALUES. The parser
// previously stopped at FOR VALUES and rejected the trailing WITH.
func TestPartitionChildWithStorageParams(t *testing.T) {
	sql := `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) WITH (fillfactor=70)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.PartitionOf == nil {
		t.Fatal("no PartitionOf")
	}
	if got := ct.With["fillfactor"]; got != "70" {
		t.Errorf("expected With[fillfactor]=70, got %q (With=%v)", got, ct.With)
	}
}

// TestPartitionChildWithStorageParamsAfterSubPartitionBy ensures the WITH clause
// is parsed even when the child is itself sub-partitioned (the executor rejects
// storage params on a partitioned child, but the parse must succeed so the
// executor can emit the proper error).
func TestPartitionChildWithStorageParamsAfterSubPartitionBy(t *testing.T) {
	sql := `CREATE TABLE mid PARTITION OF top FOR VALUES IN (1) PARTITION BY RANGE (id) WITH (fillfactor=70)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.PartitionBy == nil {
		t.Fatal("expected PartitionBy on sub-partitioned child")
	}
	if got := ct.With["fillfactor"]; got != "70" {
		t.Errorf("expected With[fillfactor]=70, got %q (With=%v)", got, ct.With)
	}
}

// TestPartitionChildTablespaceClause covers DU-002 slice 192: a leaf partition
// child may carry a trailing TABLESPACE clause (after FOR VALUES, any WITH, and
// ON COMMIT), exactly like the non-partition CREATE TABLE path. The parser
// previously stopped before TABLESPACE, leaving it unconsumed so the statement
// failed with a syntax error. The name is accepted and discarded (goopg's
// storage manager does not honour tablespaces), so the parse must simply succeed.
func TestPartitionChildTablespaceClause(t *testing.T) {
	sql := `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) TABLESPACE pg_default`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.PartitionOf == nil {
		t.Fatal("no PartitionOf")
	}
}

// TestPartitionChildTablespaceAfterWith ensures the TABLESPACE clause is parsed
// when it follows a WITH (storage params) clause — the PG grammar order is
// OptWith → OnCommitOption → OptTableSpace, so both trailers must be consumed.
func TestPartitionChildTablespaceAfterWith(t *testing.T) {
	sql := `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) WITH (fillfactor=70) TABLESPACE pg_default`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.PartitionOf == nil {
		t.Fatal("no PartitionOf")
	}
	if got := ct.With["fillfactor"]; got != "70" {
		t.Errorf("expected With[fillfactor]=70, got %q (With=%v)", got, ct.With)
	}
}
