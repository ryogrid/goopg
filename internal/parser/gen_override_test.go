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

// TestPartitionChildUsingClause covers DU-002 slice 193: a leaf partition child
// may carry a USING <access_method> clause (after FOR VALUES / PARTITION BY,
// before WITH), exactly like the non-partition CREATE TABLE path. The PG grammar
// is OptPartitionSpec table_access_method_clause OptWith OnCommitOption
// OptTableSpace, so USING precedes WITH. The parser previously stopped before
// USING, leaving it unconsumed so the statement failed with a syntax error.
// goopg has a single heap access method, so the name is accepted and discarded;
// the parse must simply succeed.
func TestPartitionChildUsingClause(t *testing.T) {
	sql := `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) USING heap`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if ct.PartitionOf == nil {
		t.Fatal("no PartitionOf")
	}
}

// TestPartitionChildUsingBeforeWith ensures USING is consumed when it precedes a
// WITH (storage params) clause — the PG grammar order is table_access_method_clause
// → OptWith, so both trailers must be parsed and WITH must still be captured.
func TestPartitionChildUsingBeforeWith(t *testing.T) {
	sql := `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) USING heap WITH (fillfactor=70) TABLESPACE pg_default`
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

// TestGeneratedColumnStorageStrategy covers DU-002 slice 194: a generated
// column records its declared storage strategy (STORED vs VIRTUAL) so
// pg_attribute.attgenerated can report the PG-faithful 'v'/'s' code and pg_dump
// re-emits the original keyword. PG18's default (no keyword) is VIRTUAL.
func TestGeneratedColumnStorageStrategy(t *testing.T) {
	cases := []struct {
		name        string
		decl        string
		wantVirtual bool
	}{
		{"stored", "GENERATED ALWAYS AS (a + 1) STORED", false},
		{"virtual", "GENERATED ALWAYS AS (a + 1) VIRTUAL", true},
		{"bare_defaults_virtual", "GENERATED ALWAYS AS (a + 1)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := "CREATE TABLE g (a integer, b integer " + tc.decl + ")"
			stmts, err := Parse(sql)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			ct := stmts[0].(*CreateTableStmt)
			var bcol *ColumnDef
			for i := range ct.Columns {
				if ct.Columns[i].Name == "b" {
					bcol = &ct.Columns[i]
				}
			}
			if bcol == nil {
				t.Fatal("column b not found")
			}
			if !bcol.GeneratedAlways {
				t.Errorf("expected GeneratedAlways=true")
			}
			if bcol.GeneratedExpr != "a + 1" {
				t.Errorf("expected GeneratedExpr='a + 1', got %q", bcol.GeneratedExpr)
			}
			if bcol.GeneratedVirtual != tc.wantVirtual {
				t.Errorf("GeneratedVirtual=%v, want %v", bcol.GeneratedVirtual, tc.wantVirtual)
			}
		})
	}
}
