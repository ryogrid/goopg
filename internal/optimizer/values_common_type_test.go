package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0134-0156. PostgreSQL resolves a VALUES column's type with
// select_common_type (parse_coerce.c:1342), which *skips* unknown-type
// literals — a bare string literal never wins over a row that supplies a real
// type, no matter which row it appears in — and falls back to text only when
// every row is unknown (parse_coerce.c:1451).
//
// goopg's exprType resolves *StringConst straight to "text", so before this
// fix both VALUES planners got it wrong in different ways: the standalone
// planner unified across rows but let "text" swallow the real type, and the
// subquery planner ignored every row after the first. Both spellings are
// asserted here so the sibling paths cannot drift again.
func TestValuesColumnTypeSelectCommonType(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		// Standalone VALUES: the typed row is first, then last.
		{"standalone/typed-first", "VALUES ('2015-04-01'::date), ('2015-01-02')", "date"},
		{"standalone/typed-last", "VALUES ('2015-01-02'), ('2015-04-01'::date)", "date"},
		// A VALUES subquery in FROM must use the same rule.
		{"subquery/typed-first", "SELECT a FROM (VALUES ('2015-04-01'::date), ('2015-01-02')) t(a)", "date"},
		{"subquery/typed-last", "SELECT a FROM (VALUES ('2015-01-02'), ('2015-04-01'::date)) t(a)", "date"},
		// All-unknown resolves to text, not to the bottom type.
		{"standalone/all-unknown", "VALUES ('x'), ('y')", "text"},
		{"subquery/all-unknown", "SELECT a FROM (VALUES ('x'), ('y')) t(a)", "text"},
		// A NULL literal is unknown too and must not defeat the real type.
		{"standalone/null-first", "VALUES (NULL), ('2015-04-01'::date)", "date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			node, err := Plan(stmts[0], catalog.NewInMemory())
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			out := node.Output()
			if len(out) != 1 {
				t.Fatalf("Plan(%q): got %d output columns, want 1", tc.sql, len(out))
			}
			if got := out[0].Type.Name; got != tc.want {
				t.Errorf("Plan(%q): column type = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// M0134-0156. PG has one implicit-column-label routine (FigureColname,
// parse_target.c) and a GROUP BY does not change what it returns:
// `SELECT EXTRACT(year FROM d), count(*) ... GROUP BY 1` labels the first
// column "extract" exactly as it would without the grouping. goopg's
// groupExprName used to be an independent mini-copy that knew only ColumnRef
// and FuncCall, so grouped EXTRACT/CASE targets degraded to "?column?".
func TestGroupByTargetLabelMatchesUngrouped(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "ctv"}, []catalog.Column{
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "d", Type: catalog.Type{Name: "date"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	labelOf := func(t *testing.T, sql string) string {
		t.Helper()
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		node, err := Plan(stmts[0], cat)
		if err != nil {
			t.Fatalf("Plan(%q): %v", sql, err)
		}
		out := node.Output()
		if len(out) == 0 {
			t.Fatalf("Plan(%q): no output columns", sql)
		}
		return out[0].Name
	}

	cases := []struct {
		name      string
		ungrouped string
		grouped   string
		want      string
	}{
		{
			name:      "extract",
			ungrouped: "SELECT EXTRACT(year FROM d) FROM ctv",
			grouped:   "SELECT EXTRACT(year FROM d), count(*) FROM ctv GROUP BY 1",
			want:      "extract",
		},
		{
			name:      "funccall",
			ungrouped: "SELECT lower(v) FROM ctv",
			grouped:   "SELECT lower(v), count(*) FROM ctv GROUP BY 1",
			want:      "lower",
		},
		{
			name:      "column-ref",
			ungrouped: "SELECT v FROM ctv",
			grouped:   "SELECT v, count(*) FROM ctv GROUP BY 1",
			want:      "v",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelOf(t, tc.ungrouped); got != tc.want {
				t.Errorf("ungrouped label = %q, want %q", got, tc.want)
			}
			if got := labelOf(t, tc.grouped); got != tc.want {
				t.Errorf("grouped label = %q, want %q (GROUP BY must not change FigureColname)", got, tc.want)
			}
		})
	}
}
