package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestBuildTPCHQueries parses, plans, and `executor.Build`s every
// HammerDB TPC-H query template (Q1..Q22) against the eight-table
// catalog. Build constructs the operator tree without touching real
// storage — failures here surface plan-node → operator-node gaps and
// any obviously-malformed plan output before we layer on a real data
// fixture.
//
// The test fails closed: every query that plans must also build, so
// regressions in either the planner's emitted node shape or the
// executor's switch in `Build` show up as a hard failure with a
// descriptive Q-number.
func TestBuildTPCHQueries(t *testing.T) {
	cat, err := tpch.Catalog()
	if err != nil {
		t.Fatalf("tpch.Catalog: %v", err)
	}
	queries := tpch.Queries()
	for q := 1; q <= 22; q++ {
		q := q
		t.Run(qName(q), func(t *testing.T) {
			stmts, perr := parser.Parse(queries[q])
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			if len(stmts) == 0 {
				t.Fatalf("no statements parsed")
			}
			node, err := optimizer.Plan(stmts[0], cat)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			op, err := Build(node)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if op == nil {
				t.Fatalf("build returned nil operator")
			}
		})
	}
}

func qName(q int) string {
	if q < 10 {
		return "Q0" + intToStr(q)
	}
	return "Q" + intToStr(q)
}

func intToStr(q int) string {
	// avoid strconv import for tiny helper
	if q == 0 {
		return "0"
	}
	var b strings.Builder
	for q > 0 {
		b.WriteByte(byte('0' + q%10))
		q /= 10
	}
	s := b.String()
	rs := []byte(s)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return string(rs)
}
