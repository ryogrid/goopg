package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// BenchmarkStringAggUnordered measures the unordered string_agg accumulator
// (review/260831 EO2-1). It used to concatenate with `+=`, recopying the whole
// accumulator per input row (O(n^2) bytes); it now appends into a byte slice.
// The row count is deliberately large enough for the quadratic term to
// dominate — a regression here shows up as a super-linear slowdown.
func BenchmarkStringAggUnordered(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()

	run := func(sql string) {
		b.Helper()
		ctx.CommandCounterIncrement()
		ctx.CmdID = ctx.GetCurrentCommandId(true)
		stmts, err := parser.Parse(sql)
		if err != nil {
			b.Fatalf("Parse(%q): %v", sql, err)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			b.Fatalf("Plan(%q): %v", sql, err)
		}
		op, err := Build(plan)
		if err != nil {
			b.Fatalf("Build(%q): %v", sql, err)
		}
		if err := op.Open(ctx); err != nil {
			b.Fatalf("Open(%q): %v", sql, err)
		}
		for {
			row, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				b.Fatalf("Next(%q): %v", sql, err)
			}
			if row == nil {
				break
			}
		}
		if err := op.Close(); err != nil {
			b.Fatalf("Close(%q): %v", sql, err)
		}
	}

	run("CREATE TABLE sagg (id int, t text)")
	const n = 4000
	var vals strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, 'abcdefghijklmnopqrstuvwxyz')", i)
	}
	run("INSERT INTO sagg VALUES " + vals.String())

	b.ReportAllocs()
	for b.Loop() {
		run("SELECT string_agg(t, ',') FROM sagg")
	}
}
