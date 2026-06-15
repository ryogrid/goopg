package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestPartitionChildGeneratedExprOverride(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	sqls := []string{
		`CREATE TABLE parttbl (a int, b int, c int, d int GENERATED ALWAYS AS (a + b) STORED) PARTITION BY LIST (a)`,
		`CREATE TABLE parttbl1 PARTITION OF parttbl FOR VALUES IN (1)`,
		`CREATE TABLE parttbl2 PARTITION OF parttbl (d WITH OPTIONS GENERATED ALWAYS AS (a + b + 1000) STORED) FOR VALUES IN (2)`,
	}
	for _, sql := range sqls {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("SQL %q: %v", sql, err)
		}
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "parttbl2"})
	if !ok {
		t.Fatal("parttbl2 not found")
	}
	var genExpr string
	for _, col := range tbl.Columns {
		if strings.EqualFold(col.Name, "d") {
			genExpr = col.GeneratedExpr
		}
	}
	if genExpr != "a + b + 1000" {
		t.Errorf("expected GeneratedExpr='a + b + 1000', got %q", genExpr)
	}

	// Now insert and verify d is computed with the child's formula.
	runSQL(t, ctx, `INSERT INTO parttbl VALUES (2, 2, 2)`)
	rows := runSQL(t, ctx, `SELECT * FROM parttbl WHERE a = 2`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// row should be (2, 2, 2, 1004)
	d := rows[0][3]
	if d.Kind != KindInt || d.Int != 1004 {
		t.Errorf("expected d=1004, got d=%v (kind=%v)", d.Int, d.Kind)
	}
}
