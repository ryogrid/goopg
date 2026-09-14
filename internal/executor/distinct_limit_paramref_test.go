package executor

// M0137-0010's duplicate-sensitive values check (M5 in
// docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md
// §2.5, open problem C1 in 02-open-problems.md): TestDistinctLimitAppliesAboveDistinct
// (distinct_limit_test.go) only pins the R83 fix for a literal `LIMIT 100`,
// which resolves to *optimizer.IntegerConst. `limitBoundMovable`
// (tuplefraction.go) special-cased only that shape and left `LIMIT $1`
// (*optimizer.ParamRef, extended-protocol bound parameter) on the
// pre-R83 truncate-before-DISTINCT order — the exact bug class R83 fixed,
// just reachable through a different LIMIT expression shape. A ParamRef
// bound value is evaluated once per execution and never varies per row or
// by outer scope, so it is exactly as position-independent as an
// IntegerConst for this purpose; there was no correctness reason to leave
// it on the old order.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

func TestDistinctLimitAppliesAboveDistinct_ParamRef(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dlp (v text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO dlp VALUES ")
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

	const q = "SELECT DISTINCT v FROM dlp ORDER BY v LIMIT $1"
	plan := planOne(t, q, ctx.Catalog)
	if _, ok := plan.(*optimizer.Limit); !ok {
		t.Fatalf("plan root is %T, want *optimizer.Limit above DISTINCT (LIMIT $1 must move exactly like LIMIT 100)", plan)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", q, err)
	}
	ctx.Params = []Datum{{Kind: KindInt, Int: 100}}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("Run(%q): %v", q, err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r[0].Format())
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("DISTINCT+LIMIT $1(=100) over 150×a+50×b = %v, want [a b] "+
			"(a Limit planned below Unique truncates pre-distinct rows: "+
			"only 100 physical rows, all 'a', ever reach DISTINCT)", got)
	}
}
