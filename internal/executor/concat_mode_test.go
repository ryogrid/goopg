package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestConcatResolvesByStaticType pins M0146-0074 against PG 18.3. `||`
// decided between array concatenation and text concatenation from the
// values alone, so any `{…}`-shaped text was spliced as an array element:
// `'a' || '{9}'::text` returned `{a,9}`. jsonb objects were merged the same
// way, and `jsonb || jsonb` over two jsonb columns was rejected as an
// unknown operator. `||` now follows the operand types:
//   - character strings concatenate as text (textcat, anytextcat);
//   - jsonb operands use jsonb_concat;
//   - arrays, and operands whose type is unknown, keep array concatenation.
//
// Every want is PG's output. The slab builder runs each query through the
// compiled twin (exprnode.go), the legacy builder through the interpreted
// one (expr.go).
func TestConcatResolvesByStaticType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ct (t text, a int[], j jsonb, v varchar(5))")
	runSQL(t, ctx, `INSERT INTO ct VALUES ('{p}', '{1,2}', '{"k":1}', '{q}')`)
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ sql, want string }{
		{"select 'a' || '{9}'::text", "a{9}"},
		{"select '{1}'::text || '{2}'::text", "{1}{2}"},
		{"select 'ab'::varchar || '{c}'", "ab{c}"},
		{"select '{c}'::varchar || 5, 5 || '{c}'::varchar", "{c}5|5{c}"},
		{"select ('{' || 'x') || '}'", "{x}"},
		{"select concat_ws(',', '{1}') || '{2}'", "{1}{2}"},
		{"select t || u from (values ('{1}'::text, '{2}'::text)) v(t, u)", "{1}{2}"},
		{"select x.n || '{z}' from (select '{y}'::text as n) x", "{y}{z}"},
		{"select t || t, v || t, t || 'x' from ct", "{p}{p}|{q}{p}|{p}x"},
		{"select upper(t) || '{w}', coalesce(t, '') || '{w}', format('%s', t) || t from ct", "{P}{w}|{p}{w}|{p}{p}"},
		// Arrays keep array concatenation.
		{"select array[1] || '{2}', '{2}' || array[1]", "{1,2}|{2,1}"},
		{"select array[1] || 3, 3 || array[1]", "{1,3}|{3,1}"},
		{"select '{a}'::text[] || '{b}'::text[], array['a'] || 'b'::text", "{a,b}|{a,b}"},
		{"select a || a, a || 9 from ct", "{1,2,1,2}|{1,2,9}"},
		{"select array(select 1) || 2, (select array_agg(g) from generate_series(1,2) g) || 3", "{1,2}|{1,2,3}"},
		// jsonb_concat.
		{"select '[1]'::jsonb || '[2]'::jsonb, '1'::jsonb || '2'::jsonb", "[1, 2]|[1, 2]"},
		{`select '{"a":1}'::jsonb || '{"b":2}', '{"a":1,"b":2}'::jsonb || '{"a":3}'::jsonb`, `{"a": 1, "b": 2}|{"a": 3, "b": 2}`},
		{`select '{"a":1}'::jsonb || '[1]'::jsonb, '[1]'::jsonb || '{"a":1}'::jsonb`, `[{"a": 1}, 1]|[1, {"a": 1}]`},
		{`select '"s"'::jsonb || '{"a":1}'`, `["s", {"a": 1}]`},
		{"select j || j, j || '[1]' from ct", `{"k": 1}|[{"k": 1}, 1]`},
		{"select '{}'::jsonb || 'x'::text", "{}x"},
	} {
		stmts, err := parser.Parse(c.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", c.sql, err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatalf("plan %q: %v", c.sql, err)
		}
		for _, slab := range []bool{false, true} {
			advanceStmtCounter(ctx)
			var op Operator
			if slab {
				op, err = BuildFastIterator(plan)
			} else {
				op, err = Build(plan)
			}
			if err != nil {
				t.Fatalf("build %q: %v", c.sql, err)
			}
			rows, err := Run(op, ctx)
			if err != nil {
				t.Fatalf("run %q (slab=%v): %v", c.sql, slab, err)
			}
			if got := strings.Join(renderRows(rows), ";"); got != c.want {
				t.Errorf("%s (slab=%v)\ngot  %s\nwant PG's %s", c.sql, slab, got, c.want)
			}
		}
	}
}
