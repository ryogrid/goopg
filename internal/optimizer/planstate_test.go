package optimizer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestConcurrentPlansKeepTheirOwnCTEScope pins P0-E8: sessions planning at
// the same time must not see each other's planner scope. When the WITH list
// in scope was the package-level planCTEs, a concurrent Plan's restore left
// another statement's `FROM x` unresolved — `relation "x" does not exist` on
// a valid `WITH x AS (...) SELECT a FROM x`. The correlated EXISTS exercises
// the other two scope channels that were globals: the outer resolveContext
// (planParent) and the analyzer's outer scope. Each goroutine plans directly
// (no plan cache in between), which is the cold-cache burst the server failed
// on. Run it under -race as well: the scope now lives on the statement's
// planState (planstate.go).
func TestConcurrentPlansKeepTheirOwnCTEScope(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "pl_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	const goroutines, iterations = 8, 200
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Distinct CTE names per goroutine, plus a plain SELECT on every
			// other one, so a leaked scope is visible both ways: a missing
			// name and a foreign one.
			cte := fmt.Sprintf("x%d", g)
			sqls := []string{
				fmt.Sprintf("WITH %s AS (SELECT 1 AS a) SELECT a FROM %s", cte, cte),
				fmt.Sprintf("WITH %s AS (SELECT a FROM pl_t) SELECT a FROM %s WHERE a IN (SELECT a FROM %s)", cte, cte, cte),
				"SELECT a FROM pl_t",
				fmt.Sprintf("WITH %s AS (SELECT a FROM pl_t) SELECT t.a FROM pl_t t WHERE EXISTS (SELECT 1 FROM %s u WHERE u.a = t.a)", cte, cte),
				"SELECT t.a FROM pl_t t WHERE t.a > (SELECT min(u.a) FROM pl_t u WHERE u.a <> t.a)",
			}
			for i := 0; i < iterations; i++ {
				sql := sqls[i%len(sqls)]
				stmts, err := parser.Parse(sql)
				if err != nil {
					errs <- fmt.Errorf("parse %q: %v", sql, err)
					return
				}
				if _, err := Plan(stmts[0], cat); err != nil {
					errs <- fmt.Errorf("goroutine %d iteration %d: plan %q: %v", g, i, sql, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
