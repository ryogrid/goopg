package executor

import (
	"strings"
	"testing"
)

// TestArrayConstructQuotesElements pins M0146-0045 against PG 18.3: ARRAY[...]
// prints each element through its output function and array_out's quoting,
// so the text re-reads as the same elements; sub-arrays splice unquoted.
func TestArrayConstructQuotesElements(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ sql, want string }{
		{`SELECT array['a,b','c']::text, array['x y']::text[]::text, array['']::text[]::text, array[null::text,'NULL']::text`,
			`{"a,b",c}|{"x y"}|{""}|{NULL,"NULL"}`},
		{`SELECT array['a"b']::text, array['a\b']::text, array['{x}']::text`,
			`{"a\"b"}|{"a\\b"}|{"{x}"}`},
		{`SELECT array[1,2,3]::text, array[1.5,-2]::text, array[true,false]::text, array['2020-01-01 10:00:00'::timestamp]::text, array[date '2020-01-02']::text`,
			`{1,2,3}|{1.5,-2}|{t,f}|{"2020-01-01 10:00:00"}|{2020-01-02}`},
		{`SELECT array[array[1,2],array[3,4]]::text, array[array['a b','c'],array['d','e']]::text, array[array['x,y']]::text`,
			`{{1,2},{3,4}}|{{"a b",c},{d,e}}|{{"x,y"}}`},
		{`SELECT cardinality(array['a,b','c']), (array['a,b','c'])[1]`, `2|a,b`},
	} {
		rows := runSQL(t, ctx, c.sql)
		parts := make([]string, len(rows[0]))
		for i, d := range rows[0] {
			parts[i] = datumTestString(d)
		}
		if got := strings.Join(parts, "|"); got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
