package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuildConstraintDefNullsNotDistinct pins the pg_get_constraintdef rendering
// of a UNIQUE constraint declared `NULLS NOT DISTINCT` (PostgreSQL 15+, DU-002
// slice 135). ruleutils.c pg_get_constraintdef_worker emits the clause BETWEEN
// the keyword and the column list (`UNIQUE NULLS NOT DISTINCT (cols)`), unlike
// pg_get_indexdef where it trails the columns. The clause must NOT appear for a
// PRIMARY KEY (whose columns are NOT NULL) nor for a default-distinct UNIQUE.
func TestBuildConstraintDefNullsNotDistinct(t *testing.T) {
	cases := []struct {
		name string
		idx  *catalog.Index
		want string
	}{
		{
			name: "unique nulls not distinct",
			idx:  &catalog.Index{Columns: []string{"a"}, NullsNotDistinct: true},
			want: "UNIQUE NULLS NOT DISTINCT (a)",
		},
		{
			name: "unique nulls not distinct with include",
			idx:  &catalog.Index{Columns: []string{"a"}, IncludeColumns: []string{"b"}, NullsNotDistinct: true},
			want: "UNIQUE NULLS NOT DISTINCT (a) INCLUDE (b)",
		},
		{
			name: "default-distinct unique unchanged",
			idx:  &catalog.Index{Columns: []string{"a"}},
			want: "UNIQUE (a)",
		},
		{
			// A PRIMARY KEY never carries NULLS NOT DISTINCT even if the flag is
			// somehow set — its columns are NOT NULL so the option is meaningless.
			name: "primary key never gains the clause",
			idx:  &catalog.Index{Columns: []string{"a"}, Primary: true, NullsNotDistinct: true},
			want: "PRIMARY KEY (a)",
		},
	}
	for _, c := range cases {
		if got := buildConstraintDefString(c.idx); got != c.want {
			t.Errorf("%s: buildConstraintDefString = %q, want %q", c.name, got, c.want)
		}
	}
}
