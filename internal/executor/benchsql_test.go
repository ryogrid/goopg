package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// benchExecSQL parses, plans, builds and fully drains one statement against a
// fixture context. Benchmarks that need a real table to measure an operator
// over (review/260831 EO2-1, EO2-2) build their fixtures with it. Draining
// stops on the EOF sentinel, which is what operators return at end of stream.
func benchExecSQL(tb testing.TB, ctx *Context, sql string) {
	tb.Helper()
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	stmts, err := parser.Parse(sql)
	if err != nil {
		tb.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		tb.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		tb.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		tb.Fatalf("Open(%q): %v", sql, err)
	}
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			tb.Fatalf("Next(%q): %v", sql, err)
		}
		if row == nil {
			break
		}
	}
	if err := op.Close(); err != nil {
		tb.Fatalf("Close(%q): %v", sql, err)
	}
}
