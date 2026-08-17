package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuildIndexDefColumn pins the colno-gated per-column rendering added for
// the SQL-callable pg_get_indexdef(indexrelid, colno, pretty) form
// (pg_get_indexdef_ext, ruleutils.c:1198-1217). colno == 0 must reproduce the
// full CREATE INDEX statement (catalog.BuildIndexDef, unchanged); colno != 0
// selects the bare column name / parenthesized expression text of the
// colno-th index attribute (1-based, key columns then INCLUDE columns) with
// none of the CREATE INDEX wrapper or per-column decoration — attrsOnly=true
// in pg_get_indexdef_worker skips the "Print additional decoration" block
// entirely (ruleutils.c:1459), so no COLLATE/opclass/ASC-DESC/NULLS text is
// emitted even for a column that carries it.
//
// An out-of-range colno on a VALID index oid returns an empty string, not
// NULL — verified against a live PG 18.3 instance (initdb + pg_ctl under
// postgres/local_install), since no regress-suite .sql exercises this case:
// pg_get_indexdef_worker never range-checks colno, it simply never satisfies
// `colno == keyno + 1` in the attribute loop and returns the still-valid
// (empty) StringInfo buffer. NULL is reserved for the oid itself not
// resolving to an index (the caller's missing_ok branch, exercised
// separately by the existing "unknown OID" behavior of the pg_get_indexdef
// case in expr.go, unchanged by this slice).
func TestBuildIndexDefColumn(t *testing.T) {
	tbl := &catalog.Table{Schema: "public", Name: "foo"}

	single := &catalog.Index{
		Name: "foo_a_idx", Schema: "public", Table: tbl,
		Columns: []string{"a"}, Method: "btree",
	}
	twoCol := &catalog.Index{
		Name: "foo_ab_idx", Schema: "public", Table: tbl,
		Columns: []string{"a", "b"}, Method: "btree",
	}
	cases := []struct {
		name  string
		idx   *catalog.Index
		colno int
		want  string
	}{
		{
			name:  "colno_0_full_statement_single",
			idx:   single,
			colno: 0,
			want:  "CREATE INDEX foo_a_idx ON public.foo USING btree (a)",
		},
		{
			name:  "colno_1_bare_column_single",
			idx:   single,
			colno: 1,
			want:  "a",
		},
		{
			name:  "colno_1_first_of_two",
			idx:   twoCol,
			colno: 1,
			want:  "a",
		},
		{
			name:  "colno_2_second_of_two",
			idx:   twoCol,
			colno: 2,
			want:  "b",
		},
		{
			name:  "colno_out_of_range_empty_not_null",
			idx:   twoCol,
			colno: 3,
			want:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.colno == 0 {
				if got := catalog.BuildIndexDef(c.idx); got != c.want {
					t.Errorf("BuildIndexDef = %q, want %q", got, c.want)
				}
				return
			}
			if got := catalog.BuildIndexDefColumn(c.idx, c.colno); got != c.want {
				t.Errorf("BuildIndexDefColumn(colno=%d) = %q, want %q", c.colno, got, c.want)
			}
		})
	}
}

// TestBuildIndexDefColumnExpression pins the per-column form for an
// expression index column: the raw deparsed expression text, parenthesized
// per ruleutils.c's looks_like_function rule (a non-bare-function-call
// expression keeps its wrapping parens).
func TestBuildIndexDefColumnExpression(t *testing.T) {
	tbl := &catalog.Table{Schema: "public", Name: "foo"}
	idx := &catalog.Index{
		Name: "foo_expr_idx", Schema: "public", Table: tbl,
		Columns:        []string{""},
		ColExprStrings: []string{"a + b"},
		Method:         "btree",
	}
	if got := catalog.BuildIndexDefColumn(idx, 1); got != "(a + b)" {
		t.Errorf("BuildIndexDefColumn(expr, colno=1) = %q, want %q", got, "(a + b)")
	}
}
