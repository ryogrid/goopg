package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestExplainStatement pins EXPLAIN — 2,013 fragments in the regress corpus,
// the largest unrouted class. Bare ANALYZE / VERBOSE and the parenthesised
// option list; every boolean option sets its flag AND its Set marker (absent
// value = true), FORMAT takes TEXT/XML/JSON/YAML, unknown names and values are
// legacy's own errors. Only FORMAT_LA needs its own token alternative — FORMAT,
// json, text and xml are all ColIds, and spelling them out again was 407
// reduce/reduce.
func TestExplainStatement(t *testing.T) {
	for _, q := range []string{
		"explain select 1",
		"explain analyze select 1",
		"explain verbose select 1",
		"explain analyze verbose select 1",
		"explain (costs off) select 1",
		"explain (verbose, costs off) select 1",
		"explain (analyze, costs off, summary off, timing off, buffers off) select 1",
		"explain (verbose true, costs false) select 1",
		"explain (verbose on, costs off) select 1",
		"explain (verbose, format json, costs off) select 1",
		"explain (costs off, format json, verbose) select 1",
		"explain (format text) select 1",
		"explain (format xml) select 1",
		"explain (settings on) select 1",
		"explain (generic_plan) select $1",
		"explain (memory) select 1",
		"explain (wal) select 1",
		"explain (costs off) insert into t values (1)",
		"explain (costs off) create table t as select 1",
		"explain (costs off) with c as (select 1) select * from c",
		"explain execute p",
		"explain (costs off) copy t from stdin",
		"explain (costs off) merge into t using u on true when matched then delete",
		"EXPLAIN (COSTS OFF) SELECT * FROM t WHERE a = 1 ORDER BY b",
	} {
		assertParity(t, q)
	}
	assertBothReject(t, "explain (serialize) select 1")
}

// TestExplainRoutingFollowsInner — an EXPLAIN is routed only when the
// statement it wraps would be; an unported inner class stays on legacy rather
// than surfacing a 42601.
func TestExplainRoutingFollowsInner(t *testing.T) {
	routed := func(q string) bool {
		toks, err := parser.Lex(q)
		if err != nil {
			t.Fatalf("lex %q: %v", q, err)
		}
		return fragmentRouted(toks)
	}
	for _, q := range []string{
		"explain select 1",
		"explain (costs off) insert into t values (1)",
		"explain analyze verbose update t set a = 1",
		"explain (costs off) with c as (select 1) select * from c",
		"explain execute p",
		"explain (costs off) copy t from stdin",
		"explain (costs off) merge into t using u on true when matched then delete",
	} {
		if !routed(q) {
			t.Errorf("not routed: %q", q)
		}
	}
	for _, q := range []string{
	} {
		if routed(q) {
			t.Errorf("unexpectedly routed: %q", q)
		}
	}
}

// TestArbiterOpclass — an ON CONFLICT arbiter element may carry an operator
// class and a COLLATE; legacy drops both and keeps the expression.
func TestArbiterOpclass(t *testing.T) {
	for _, q := range []string{
		"insert into t values (1) on conflict (key text_pattern_ops) do nothing",
		"insert into t values (1) on conflict (lower(key) text_pattern_ops, b) do nothing",
		`insert into t values (1) on conflict (key COLLATE "C" text_pattern_ops) do nothing`,
		"insert into t values (1) on conflict (key) do nothing",
		"explain (costs off) insert into insertconflicttest values (0, 'Bilberry') on conflict (key text_pattern_ops) do update set fruit = 'x'",
	} {
		assertParity(t, q)
	}
}
