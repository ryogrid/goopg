package parser

import (
	"strings"
	"testing"
)

// TestFigureIndexColname pins FigureIndexColname against PG 18.3's
// FigureColnameInternal (the generated column names of the same SELECT list
// on PG; an index expression column takes the same name). "" is PG's "no
// name", which ChooseIndexColumnNames turns into "expr".
func TestFigureIndexColname(t *testing.T) {
	for _, c := range []struct{ cols, want string }{
		{"abs(b), lower(a), (a::int)", "abs lower a"},
		{"(1::int), (1::integer), (1::bigint), (1::double precision), (1::varchar(3)), (1::boolean)", "int4 int4 int8 float8 varchar bool"},
		{"(1::real), (1::numeric(3,1)), (1::char(2)), (1::timestamptz), (1::int4), (1::float)", "float4 numeric bpchar timestamptz int4 float8"},
		{"coalesce(a,b), nullif(a,b), greatest(a,b), (current_date), (case when a then 1 end), (case when a then 1 else b end)", "coalesce nullif greatest current_date case b"},
		{"(a + b), (row(a,b)), (array[a]), (a collate \"C\")", " row array a"},
	} {
		st, err := Parse("create index on t (" + c.cols + ")")
		if err != nil {
			t.Fatalf("%s: %v", c.cols, err)
		}
		ci := st[0].(*CreateIndexStmt)
		var got []string
		for i, e := range ci.ColExprs {
			if e == nil {
				got = append(got, ci.Columns[i])
				continue
			}
			got = append(got, FigureIndexColname(e))
		}
		if g := strings.Join(got, " "); g != c.want {
			t.Errorf("%s: got %q, want %q", c.cols, g, c.want)
		}
	}
}
