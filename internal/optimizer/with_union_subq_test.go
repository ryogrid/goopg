package optimizer_test

import (
	"testing"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestWithCTEUnionInsideSubquery verifies that a WITH clause inside a derived
// table subquery makes the CTE visible to both branches of a UNION. M0097-0098.
func TestWithCTEUnionInsideSubquery(t *testing.T) {
	tests := []string{
		`SELECT count(*) FROM (
  WITH q1(x) AS (SELECT 1)
    SELECT * FROM q1
  UNION ALL
    SELECT * FROM q1
) ss`,
		`SELECT count(*) FROM (
  WITH q1(x) AS (SELECT generate_series(1,5))
    SELECT * FROM q1
  UNION
    SELECT * FROM q1
) ss`,
	}
	cat := catalog.NewInMemory()
	for _, sql := range tests {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse error for %q: %v", sql, err)
		}
		if _, err := optimizer.Plan(stmts[0], cat); err != nil {
			t.Errorf("Plan error for %q: %v", sql, err)
		}
	}
}
