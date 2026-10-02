package executor

import (
	"strings"
	"testing"
)

// TestArrayAggOverArraysMatchesPG pins M0146-0033 against PG 18.3:
// array_agg(anyarray) is array_agg_array_transfn — every input becomes a
// sub-array of a result one dimension deeper (`{{1},{2}}`), where goopg
// returned the text array of the inputs' text (`{"{1}","{2}"}`). The
// accumArrayResultArr errors follow PG's order: a NULL input, an empty
// FIRST input, and any later input (empty included) whose dimensions differ
// from the first. Wants are PG's own output.
func TestArrayAggOverArraysMatchesPG(t *testing.T) {
	cases := []struct {
		sql, want, err string
	}{
		{sql: "SELECT array_agg(x) FROM (VALUES (array[1]),(array[2])) v(x)", want: "{{1},{2}}"},
		{sql: "SELECT array_agg(x ORDER BY x DESC) FROM (VALUES (array[1,2]),(array[3,4])) v(x)", want: "{{3,4},{1,2}}"},
		{sql: `SELECT array_agg(x) FROM (VALUES ('{"a,b",c}'::text[]),('{d,e}'::text[])) v(x)`, want: `{{"a,b",c},{d,e}}`},
		{sql: `SELECT array_agg(t) FROM (VALUES ('{x,"y z"}'::text[]), ('{NULL,w}'::text[])) v(t)`, want: `{{x,"y z"},{NULL,w}}`},
		{sql: "SELECT array_agg(ar) FROM (VALUES ('{{1,2},{3,4}}'::int[]), ('{{5,6},{7,8}}'::int[])) v(ar)", want: "{{{1,2},{3,4}},{{5,6},{7,8}}}"},
		{sql: "SELECT array_agg(x) FILTER (WHERE x IS NOT NULL) FROM (VALUES (array[1]),(NULL)) v(x)", want: "{{1}}"},
		{sql: "SELECT array_agg(x) FROM (VALUES (1),(2)) v(x)", want: "{1,2}"},
		{sql: "SELECT array_agg(x) FROM (VALUES (array[1]),(NULL::int[])) v(x)", err: "cannot accumulate null arrays"},
		{sql: "SELECT array_agg(x) FROM (VALUES ('{}'::int[]),('{1}'::int[])) v(x)", err: "cannot accumulate empty arrays"},
		{sql: "SELECT array_agg(x) FROM (VALUES (array[1]),('{}'::int[])) v(x)", err: "cannot accumulate arrays of different dimensionality"},
		{sql: "SELECT array_agg(x) FROM (VALUES (array[1]),(array[1,2])) v(x)", err: "cannot accumulate arrays of different dimensionality"},
	}
	for _, c := range cases {
		rows, err := runSQLErr(t, c.sql)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.sql, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Errorf("%s: rows %v", c.sql, rows)
			continue
		}
		if got := rows[0][0].Format(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.sql, got, c.want)
		}
	}
}

// TestArrayTextShape pins the dimension reader behind the mismatch check.
func TestArrayTextShape(t *testing.T) {
	for in, want := range map[string][]int{
		"{}":                    nil,
		"{1}":                   {1},
		`{"a,b",c}`:             {2},
		`{"{x}",y,"z\"}"}`:      {3},
		"{{1,2},{3,4},{5,6}}":   {3, 2},
		"{{{1},{2}},{{3},{4}}}": {2, 2, 1},
	} {
		got, ok := arrayTextShape(in)
		if !ok || !sameArrayShape(got, want) {
			t.Errorf("arrayTextShape(%s) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := arrayTextShape("[2:3]={1,2}"); ok {
		t.Errorf("explicit lower bounds must report !ok")
	}
}
