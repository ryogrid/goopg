package executor

import (
	"reflect"
	"strings"
	"testing"
)

// TestFieldSelection pins M0146-0047b against PG 18.3: composite field
// selection `(expr).field` was a syntax error. Every want is PG's output for
// the same statement — values, and for the refused forms the SQLSTATE and
// message.
func TestFieldSelection(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE wb (x int, y text)",
		"INSERT INTO wb VALUES (1, 'a'), (2, 'b c')",
		"CREATE TYPE ctyp AS (p int, q text)",
		"CREATE TABLE cmt (id int, c ctyp)",
		"INSERT INTO cmt VALUES (1, ROW(1, 'a')), (2, ROW(5, 'b')), (3, NULL)",
		"CREATE FUNCTION mk47() RETURNS ctyp LANGUAGE sql AS $$ SELECT ROW(5, 'f')::ctyp $$",
		// regress rowtypes' nested composite fixture.
		"CREATE TYPE complex AS (r float8, i float8)",
		"CREATE TYPE quad AS (c1 complex, c2 complex)",
		"CREATE TABLE quadtable (f1 int, q quad)",
		"INSERT INTO quadtable VALUES (1, ((3.3,4.4),(5.5,6.6)))",
		"INSERT INTO quadtable VALUES (2, ((null,4.4),(5.5,6.6)))",
	} {
		runSQL(t, ctx, q)
	}
	for _, c := range []struct{ sql, want string }{
		// Whole-row reference to a relation: the column.
		{"SELECT (b).x FROM wb b ORDER BY 1", "1;2"},
		{"SELECT (b).y, (b).x + 1 FROM wb b ORDER BY 1", "a|2;b c|3"},
		{"SELECT (wb).x FROM wb ORDER BY 1", "1;2"},
		{"SELECT (b.*).x FROM wb b ORDER BY 1", "1;2"},
		{"SELECT x FROM wb b WHERE (b).y = 'a'", "1"},
		{"SELECT x FROM wb b ORDER BY (b).y DESC", "2;1"},
		{"SELECT (b).y, count(*) FROM wb b GROUP BY (b).y ORDER BY 1", "a|1;b c|1"},
		{"SELECT (b).y, count(*) FROM wb b GROUP BY y ORDER BY 1", "a|1;b c|1"},
		// Anonymous record: fields f1..fN.
		{"SELECT (ROW(1,2)).f1", "1"},
		{"SELECT (ROW(1,2)).f2 + 1", "3"},
		// Composite-typed values.
		{"SELECT (c).p FROM (SELECT ROW(1,'z')::ctyp AS c) s", "1"},
		{"SELECT (c).q FROM (SELECT ROW(1,'z w')::ctyp AS c) s", "z w"},
		{"SELECT ((ROW(1,'z')::ctyp)).q", "z"},
		{"SELECT (mk47()).p, (mk47()).q", "5|f"},
		{"SELECT (c).q FROM (SELECT ROW(1, NULL)::ctyp AS c) s", "NULL"},
		{"SELECT (c).q IS NULL FROM (SELECT NULL::ctyp AS c) s", "t"},
		{`SELECT (c).q FROM (SELECT ROW(2, 'a"b,c')::ctyp AS c) s`, `a"b,c`},
		{"SELECT ((c).p + 1) * 2 FROM (SELECT ROW(20,'x')::ctyp AS c) s", "42"},
		{"SELECT id, (c).p, (c).q FROM cmt WHERE (c).p > 1", "2|5|b"},
		{"SELECT id FROM cmt WHERE (c).p IS NULL", "3"},
		// Chained selection: (q).c1 is itself composite.
		{"SELECT f1, (q).c1, (qq.q).c1.i FROM quadtable qq ORDER BY f1", "1|(3.3,4.4)|4.4;2|(,4.4)|4.4"},
		{"SELECT f1, (q).c2.r + 1 FROM quadtable ORDER BY f1", "1|6.5;2|6.5"},
	} {
		rows, err := runQueryWithErr(ctx, c.sql)
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if got := strings.Join(renderRows(rows), ";"); got != c.want {
			t.Errorf("%s\ngot  %s\nwant %s", c.sql, got, c.want)
		}
	}
	// FigureColname: the output column is named after the field.
	if node := planForTest(t, ctx, "SELECT (ROW(1, 2.0)).f2, (b).x, (c).q FROM wb b, (SELECT ROW(1,'z')::ctyp AS c) s"); node != nil {
		var names []string
		for _, col := range node.Output() {
			names = append(names, col.Name)
		}
		if got := strings.Join(names, ","); got != "f2,x,q" {
			t.Errorf("output column names = %s, want f2,x,q (PG)", got)
		}
	}
	for _, c := range []struct{ sql, code, msg string }{
		{"SELECT (c).nope FROM (SELECT ROW(1,'z')::ctyp AS c) s", "42703", `column "nope" not found in data type ctyp`},
		{"SELECT (b).zz FROM wb b", "42703", `column "zz" not found in data type wb`},
		{"SELECT (1).x", "42809", "column notation .x applied to type integer, which is not a composite type"},
		{"SELECT (ROW(1,2)).f3", "42703", `could not identify column "f3" in record data type`},
		// A whole-row reference no grouping column covers: PG's error, where
		// the grouped resolver used to panic the backend.
		{"SELECT b, count(*) FROM wb b GROUP BY x", "42803", `column "b.*" must appear in the GROUP BY clause or be used in an aggregate function`},
	} {
		_, err := runQueryWithErr(ctx, c.sql)
		if err == nil {
			t.Errorf("%s: no error, want %s %s", c.sql, c.code, c.msg)
			continue
		}
		if !strings.Contains(err.Error(), c.msg) || !strings.Contains(err.Error(), c.code) {
			t.Errorf("%s:\ngot  %v\nwant %s %s", c.sql, err, c.code, c.msg)
		}
	}
}

// TestParseRecordText pins the field tokenizer against record_in's rules
// (rowtypes.c): an empty unquoted field is NULL, `""` is an empty string,
// quotes group commas and parentheses, a doubled quote inside quotes is a
// quote, a backslash escapes the next character.
func TestParseRecordText(t *testing.T) {
	for _, c := range []struct {
		in     string
		fields []string
		nulls  []bool
	}{
		{`(1,"z w")`, []string{"1", "z w"}, []bool{false, false}},
		{`(3,)`, []string{"3", ""}, []bool{false, true}},
		{`(3,"")`, []string{"3", ""}, []bool{false, false}},
		{`(7,"x\\y""z")`, []string{"7", `x\y"z`}, []bool{false, false}},
		{`(a b,"(1,2)")`, []string{"a b", "(1,2)"}, []bool{false, false}},
		{`(,)`, []string{"", ""}, []bool{true, true}},
		{` (1)`, []string{"1"}, []bool{false}},
	} {
		f, n, err := parseRecordText(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(f, c.fields) || !reflect.DeepEqual(n, c.nulls) {
			t.Errorf("%s: got %q %v, want %q %v", c.in, f, n, c.fields, c.nulls)
		}
	}
	for _, bad := range []string{`1,2`, `(1,2`, `(1,"2)`} {
		if _, _, err := parseRecordText(bad); err == nil {
			t.Errorf("%s: no error, want malformed record literal", bad)
		}
	}
}
