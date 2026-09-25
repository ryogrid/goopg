package optimizer_test

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSetOpOutputTypeIsCommonType pins that a set operation's output type is
// resolved across BOTH members, as PostgreSQL's select_common_type does
// (postgres/src/backend/parser/parse_coerce.c:1342), rather than taken from the
// first member.
//
// goopg used to report the FIRST member's type and coerce only the RIGHT branch
// to it. The consequence reached the wire, which is why this is pinned at the
// planner's output SCHEMA and not merely on values: `SELECT 1 UNION ALL SELECT
// 2.5` was described as bigint while carrying a row of 2.5, so a typed client
// (JDBC, psycopg) was told int8 for a numeric column. The VALUES were correct
// throughout, so no row-count or value gate could see it — the schema is the
// only witness.
//
// Every `want` below was MEASURED on a live PG 18.3 rather than derived from
// the algorithm, including the reversed-order pair, which is the case that
// distinguishes a real common-type resolution from "first member wins": if the
// order of the members changes the answer, the resolution is positional.
func TestSetOpOutputTypeIsCommonType(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"int and numeric literal", `SELECT 1 AS a UNION ALL SELECT 2.5`, "numeric"},
		{"int2 and int8", `SELECT 1::int2 AS a UNION ALL SELECT 2::int8`, "int8"},
		{"int4 and float4", `SELECT 1::int4 AS a UNION ALL SELECT 2.0::float4`, "float4"},
		{"float4 and float8", `SELECT 1::float4 AS a UNION ALL SELECT 2.0::float8`, "float8"},
		// The symmetry case: wider member FIRST. A positional rule would answer
		// float8 here for the wrong reason, so it is paired with the row above.
		{"float8 first, int2 second", `SELECT 2.0::float8 AS a UNION ALL SELECT 1::int2`, "float8"},
		// All-same is upstream's own fast path and must stay untouched.
		{"same type is unchanged", `SELECT 'a'::text AS a UNION ALL SELECT 'b'::text`, "text"},
	}
	cat := catalog.NewInMemory()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stmts, err := parser.Parse(c.sql)
			if err != nil {
				t.Fatalf("parse %q: %v", c.sql, err)
			}
			plan, err := optimizer.Plan(stmts[0], cat)
			if err != nil {
				t.Fatalf("plan %q: %v", c.sql, err)
			}
			out := plan.Output()
			if len(out) == 0 {
				t.Fatalf("plan %q produced an empty schema", c.sql)
			}
			if got := out[0].Type.Name; got != c.want {
				t.Errorf("%s\n  output type = %q, want %q (measured on PG 18.3)",
					c.sql, got, c.want)
			}
		})
	}
}
