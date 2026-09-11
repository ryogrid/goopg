package executor

// LIMIT applies above DISTINCT (R83): a Limit below Unique truncates
// pre-distinct rows — wrong whenever duplicates exceed the limit.
// PG never pushes Limit below Unique (ExecSetTupleBound stops at any
// node that can combine rows). Values probe: 150×'a' + 50×'b' with
// DISTINCT + LIMIT 100 must return both values.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

func TestDistinctLimitAppliesAboveDistinct(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dl (v text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO dl VALUES ")
	vals := make([]string, 0, 200)
	for i := 0; i < 150; i++ {
		vals = append(vals, "('a')")
	}
	for i := 0; i < 50; i++ {
		vals = append(vals, "('b')")
	}
	sb.WriteString(strings.Join(vals, ", "))
	if err := runDDL(t, ctx, sb.String()); err != nil {
		t.Fatalf("insert: %v", err)
	}

	const q = "SELECT DISTINCT v FROM dl ORDER BY v LIMIT 100"
	rows := runQuery(t, ctx, q)
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r[0].Format())
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("DISTINCT+LIMIT 100 over 150×a+50×b = %v, want [a b]", got)
	}

	// Shape pin: LIMIT sits above the Distinct, never below it.
	plan := planOne(t, q, ctx.Catalog)
	if _, ok := plan.(*optimizer.Limit); !ok {
		t.Fatalf("plan root is %T, want *optimizer.Limit above DISTINCT", plan)
	}
}
