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
		{
			// DEFERRABLE (immediate) appends ` DEFERRABLE` only; INITIALLY
			// IMMEDIATE is the default and is omitted. DU-002 slice 139.
			name: "unique deferrable initially immediate",
			idx:  &catalog.Index{Columns: []string{"a"}, Deferrable: true},
			want: "UNIQUE (a) DEFERRABLE",
		},
		{
			// DEFERRABLE INITIALLY DEFERRED appends both clauses. DU-002 slice 139.
			name: "unique deferrable initially deferred",
			idx:  &catalog.Index{Columns: []string{"a"}, Deferrable: true, InitiallyDeferred: true},
			want: "UNIQUE (a) DEFERRABLE INITIALLY DEFERRED",
		},
		{
			// The DEFERRABLE clause trails INCLUDE, matching ruleutils.c order.
			name: "unique include deferrable deferred",
			idx:  &catalog.Index{Columns: []string{"a"}, IncludeColumns: []string{"b"}, Deferrable: true, InitiallyDeferred: true},
			want: "UNIQUE (a) INCLUDE (b) DEFERRABLE INITIALLY DEFERRED",
		},
	}
	for _, c := range cases {
		if got := buildConstraintDefString(c.idx); got != c.want {
			t.Errorf("%s: buildConstraintDefString = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestBuildConstraintDefExclusionWhere pins the pg_get_constraintdef rendering of
// a PARTIAL EXCLUDE constraint (DU-002 slice 310). PG renders the exclusion def
// via pg_get_indexdef_worker, which appends ` WHERE (%s)` (ruleutils.c:1564)
// after the operator/INCLUDE list and BEFORE the DEFERRABLE clauses the shared
// tail adds. The predicate must NOT appear when the index carries no
// PredicateString, and a non-partial EXCLUDE must round-trip unchanged.
func TestBuildConstraintDefExclusionWhere(t *testing.T) {
	cases := []struct {
		name string
		idx  *catalog.Index
		want string
	}{
		{
			name: "partial exclusion where",
			idx:  &catalog.Index{Columns: []string{"a"}, Method: "btree", IsExclusion: true, ExclusionOp: "=", PredicateString: "(b > 0)"},
			want: "EXCLUDE USING btree (a WITH =) WHERE (b > 0)",
		},
		{
			// WHERE trails INCLUDE and precedes DEFERRABLE, matching the
			// pg_get_indexdef_worker order + the shared deferrable tail.
			name: "partial exclusion include where deferrable deferred",
			idx: &catalog.Index{
				Columns: []string{"a"}, Method: "btree", IsExclusion: true, ExclusionOp: "=",
				IncludeColumns: []string{"c"}, PredicateString: "(b > 0)",
				Deferrable: true, InitiallyDeferred: true,
			},
			want: "EXCLUDE USING btree (a WITH =) INCLUDE (c) WHERE (b > 0) DEFERRABLE INITIALLY DEFERRED",
		},
		{
			name: "non-partial exclusion unchanged",
			idx:  &catalog.Index{Columns: []string{"a"}, Method: "btree", IsExclusion: true, ExclusionOp: "="},
			want: "EXCLUDE USING btree (a WITH =)",
		},
	}
	for _, c := range cases {
		if got := buildConstraintDefString(c.idx); got != c.want {
			t.Errorf("%s: buildConstraintDefString = %q, want %q", c.name, got, c.want)
		}
	}
}
