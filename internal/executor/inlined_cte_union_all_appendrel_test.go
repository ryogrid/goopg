package executor

// M0146-0065: an inlined single-reference CTE over a UNION ALL is an
// appendrel.
//
// PG's inline_cte turns the reference into an RTE_SUBQUERY before
// pull_up_subqueries runs, so pull_up_simple_union_all flattens it into an
// appendrel and the join sees a (Parallel) Append. TPC-DS Q2's `wswscs`
// reads `wscs`, whose body selects from an unaliased `web_sales UNION ALL
// catalog_sales` subquery: PG plans Finalize HashAggregate -> Gather ->
// Parallel Hash Join -> Parallel Append there.
//
// goopg kept every such reference a CTE Scan leaf, which the search cannot
// mark appendrel, and planned the join serially:
//   - a CTE body that IS the UNION ALL was declined by the FROM pull-up
//     (it splices only a simple SELECT body);
//   - a body selecting FROM a UNION ALL subquery was declined because that
//     FROM item is not a plain relation;
//   - a reference inside a later sibling's body saw the CTE's
//     single-reference gate unstamped (cterefcount and the SELECT owner
//     were recorded only after the whole WITH list was preplanned).

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasCTEScanNamed reports whether any *optimizer.CTEScan for the CTE
// `name` survives anywhere in the plan tree (reflective walk over every
// Node-typed field, slice and pointer).
func planHasCTEScanNamed(n optimizer.Node, name string) bool {
	found := false
	seen := map[uintptr]bool{}
	nodeType := reflect.TypeOf((*optimizer.Node)(nil)).Elem()
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if found || depth > 200 || !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.Ptr:
			if v.IsNil() || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
			if v.CanInterface() {
				if cs, ok := v.Interface().(*optimizer.CTEScan); ok && cs.Name == name {
					found = true
					return
				}
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Field(i)
				if f.Kind() == reflect.Interface && f.Type().Implements(nodeType) ||
					f.Kind() == reflect.Ptr || f.Kind() == reflect.Slice {
					walk(f, depth+1)
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(n), 0)
	return found
}

func TestInlinedCTEUnionAllIsAppendrel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE ws (sd int, p numeric)",
		"CREATE TABLE cs (sd int, p numeric)",
		"CREATE TABLE dd (dk int PRIMARY KEY, wk int)",
		"INSERT INTO ws SELECT g % 20, g FROM generate_series(1, 180) g",
		"INSERT INTO cs SELECT g % 20, g FROM generate_series(1, 360) g",
		"INSERT INTO dd SELECT g, g / 7 FROM generate_series(1, 30) g",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	// Each want is PG 18.3's answer on the same data.
	for _, tc := range []struct {
		name, cte, sql, want string
	}{
		{
			// The body is the UNION ALL itself.
			name: "body-is-union-all", cte: "w",
			sql: `WITH w AS (SELECT sd, p FROM ws UNION ALL SELECT sd, p FROM cs)
				SELECT count(*), sum(p), sum(wk) FROM w, dd WHERE dk = sd`,
			want: "513|76950|513",
		},
		{
			// The body selects FROM an aliased UNION ALL subquery.
			name: "body-over-union-all-subquery", cte: "w",
			sql: `WITH w AS (SELECT sd, p FROM (SELECT sd, p FROM ws UNION ALL SELECT sd, p FROM cs) s)
				SELECT count(*), sum(p), sum(wk) FROM w, dd WHERE dk = sd`,
			want: "513|76950|513",
		},
		{
			// TPC-DS Q2's shape: an unaliased UNION ALL subquery, read by a
			// later sibling's body.
			name: "q2-sibling-reference", cte: "wscs",
			sql: `WITH wscs AS (SELECT sd, p FROM (SELECT sd, p FROM ws UNION ALL SELECT sd, p FROM cs)),
				wswscs AS (SELECT wk, sum(p) AS s FROM wscs, dd WHERE dk = sd GROUP BY wk)
				SELECT count(*), sum(s) FROM wswscs`,
			want: "3|76950",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if planHasCTEScanNamed(plan, tc.cte) {
				t.Errorf("CTE %s is still a CTE Scan leaf; PG inlines it as an appendrel", tc.cte)
			}
			if got := renderRows(runSQL(t, ctx, tc.sql)); len(got) != 1 || got[0] != tc.want {
				t.Errorf("result = %v, want PG's %s", got, tc.want)
			}
		})
	}
}
