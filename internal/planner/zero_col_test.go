package planner

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestZeroColumnSelect(t *testing.T) {
	cat := catalog.NewInMemory()
	tests := []string{
		"SELECT FROM generate_series(1,5) AS s",
		"SELECT FROM generate_series(1,5) AS s UNION SELECT FROM generate_series(1,3) AS s",
		"WITH cte AS (SELECT s FROM generate_series(1,5) s) SELECT FROM cte UNION SELECT FROM cte",
		"WITH cte AS MATERIALIZED (SELECT s FROM generate_series(1,5) s) SELECT FROM cte UNION SELECT FROM cte",
		"WITH cte AS NOT MATERIALIZED (SELECT s FROM generate_series(1,5) s) SELECT FROM cte UNION SELECT FROM cte",
	}
	for _, sql := range tests {
		node, err := Plan(mustParse(t, sql), cat)
		if err != nil {
			t.Errorf("Plan(%q): %v", sql, err)
			continue
		}
		schema := node.Output()
		fmt.Printf("plan type: %T, schema len=%d\n", node, len(schema))
	}
}
