package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestTextComparesAsText pins M0146-0053 against PG 18.3. Records, arrays
// and tids travel as text Datums, so compareDatum guesses from a value's
// shape: two strings that start with `(` compared element-wise as rows,
// `{` as arrays, and pg_lsn- / uuid-shaped strings by those types' rules.
// A real text value got the same treatment, so `'(999,9)'::text >
// '(1241,10)'::text` was false and `'(1,2)'::text = '(01,2)'::text` true.
// Comparisons whose operands are statically character strings now compare
// as text; records, arrays and tids keep their element-wise order. Every
// want is PG's output. Both builders run each query: the slab builder
// evaluates BinaryOp through the compiled twin (exprnode.go), the legacy
// builder through the interpreted one (expr.go).
func TestTextComparesAsText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE tc (x text)")
	runSQL(t, ctx, "INSERT INTO tc VALUES ('(999,9)'), ('(1241,10)'), ('(2,1)'), ('{10}'), ('{9}'), ('(01,2)'), ('(1,2)')")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ sql, want string }{
		{"select '(999,9)'::text > '(1241,10)'::text as t1", "t"},
		{"select '(999,9)'::text > '(1241,10)'::text collate \"C\" as t2", "t"},
		{"select '(1,2)'::text = '(01,2)'::text as t3", "f"},
		{"select '{10}'::text < '{9}'::text as t4", "t"},
		{"select '{1,2}'::text = '{01,2}'::text as t5", "f"},
		{"select '(a,b)'::text < '(a,c)'::text as t6", "t"},
		{"select max(x) from (values ('(999,9)'::text), ('(1241,10)')) v(x)", "(999,9)"},
		{"select min(x) from (values ('{10}'::text), ('{9}')) v(x)", "{10}"},
		{"select string_agg(x, ' ' order by x) from (values ('(999,9)'::text), ('(1241,10)'), ('(2,1)')) v(x)", "(1241,10) (2,1) (999,9)"},
		{"select string_agg(x::text, ' ' order by x) from (values (row(999,9)), (row(1241,10)), (row(2,1))) v(x)", "(2,1) (999,9) (1241,10)"},
		{"select max(r)::text from (values (row(999,9)), (row(1241,10))) v(r)", "(1241,10)"},
		{"select row(999,9) > row(1241,10) as rec", "f"},
		{"select '(999,9)'::tid > '(1241,10)'::tid as tidcmp", "f"},
		{"select max(a)::text from (values (array[10]), (array[9])) v(a)", "{10}"},
		{"select string_agg(x::text, ' ' order by x) from (values (array[10]), (array[9]), (array[100])) v(x)", "{9} {10} {100}"},
		{"select x from (values ('(999,9)'::text), ('(1241,10)')) v(x) where x > '(1000,0)'", "(999,9);(1241,10)"},
		{"select count(distinct x) from (values ('(1,2)'::text), ('(01,2)')) v(x)", "2"},
		{"select '(1,2)'::varchar < '(01,2)'::varchar as vc", "f"},
		{"select x from (values (row(1,'a b')), (row(0,'z'))) v(x) order by x", "(0,z);(1,\"a b\")"},
		{"select x::text from (values ('{10}'::text), ('{9}')) v(x) order by x", "{10};{9}"},
		{"select x from (values (array[10]), (array[9])) v(x) order by x", "{9};{10}"},
		{"select distinct x from (values ('(10,1)'::text), ('(9,1)')) v(x) order by x", "(10,1);(9,1)"},
		{"select x, y from (values ('(1,2)'::text), ('(01,2)')) v(x) join (values ('(01,2)'::text)) w(y) on x = y", "(01,2)|(01,2)"},
		{"select greatest('(999,9)'::text, '(1241,10)'::text), least('{10}'::text, '{9}'::text)", "(999,9)|{10}"},
		{"select '(999,9)'::text is distinct from '(0999,9)'::text", "t"},
		{"select row('(999,9)'::text) < row('(1241,10)'::text)", "f"},
		{"select '0/10'::text < '0/9'::text as lsnlike", "t"},
		{"select 'A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11'::text = 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::text as uuidlike", "f"},
		// Table data: scan-absorbed predicates, arena-backed strings.
		{"select string_agg(x, ' ' order by x) from tc where x > '(1000,0)'", "(1241,10) (2,1) (999,9) {10} {9}"},
		{"select string_agg(x, ' ' order by x) from tc", "(01,2) (1,2) (1241,10) (2,1) (999,9) {10} {9}"},
		{"select min(x), max(x) from tc", "(01,2)|{9}"},
		{"select count(*) from tc a, tc b where a.x < b.x", "21"},
		{"select string_agg(x, ' ' order by x desc) from (select distinct x from tc) s", "{9} {10} (999,9) (2,1) (1241,10) (1,2) (01,2)"},
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
