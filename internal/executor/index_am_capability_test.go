package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCheckIndexAMCapabilities guards the DefineIndex capability gate wired in
// M0134-0167. No upstream regress case exercises these four errors (verified:
// none of the strings appears anywhere under postgres/src/test or contrib), so
// this is the only coverage they have — before the fix goopg enforced ONLY the
// amcaninclude arm and silently accepted UNIQUE / multicolumn / ordered
// indexes on AMs whose IndexAmRoutine forbids them.
//
// Expectations are transcribed from a live PG 18.3 oracle run of the same
// statements, and from postgres/src/backend/commands/indexcmds.c:868-892
// (unique / include / multicol, in that order) and :2222-2236 (per-column
// ASC/DESC then NULLS FIRST/LAST).
func TestCheckIndexAMCapabilities(t *testing.T) {
	desc := parser.IndexColOrder{Descending: true, NullsFirst: true}
	ascNullsFirst := parser.IndexColOrder{Descending: false, NullsFirst: true}
	plain := parser.IndexColOrder{}

	cases := []struct {
		name   string
		method string
		stmt   parser.CreateIndexStmt
		want   string // "" means the statement must be accepted
	}{
		// amcanunique: only btree sets it.
		{"unique spgist", "spgist", parser.CreateIndexStmt{Unique: true, Columns: []string{"p"}},
			`access method "spgist" does not support unique indexes`},
		{"unique gist", "gist", parser.CreateIndexStmt{Unique: true, Columns: []string{"p"}},
			`access method "gist" does not support unique indexes`},
		{"unique gin", "gin", parser.CreateIndexStmt{Unique: true, Columns: []string{"b"}},
			`access method "gin" does not support unique indexes`},
		{"unique brin", "brin", parser.CreateIndexStmt{Unique: true, Columns: []string{"a"}},
			`access method "brin" does not support unique indexes`},
		{"unique hash", "hash", parser.CreateIndexStmt{Unique: true, Columns: []string{"a"}},
			`access method "hash" does not support unique indexes`},
		{"unique btree", "btree", parser.CreateIndexStmt{Unique: true, Columns: []string{"a"}}, ""},

		// amcaninclude: btree, gist and spgist set it; hash/gin/brin do not.
		// This arm is the one goopg already had, as a hardcoded AM-name list.
		{"include hash", "hash", parser.CreateIndexStmt{Columns: []string{"a"}, IncludeColumns: []string{"b"}},
			`access method "hash" does not support included columns`},
		{"include gin", "gin", parser.CreateIndexStmt{Columns: []string{"b"}, IncludeColumns: []string{"a"}},
			`access method "gin" does not support included columns`},
		{"include brin", "brin", parser.CreateIndexStmt{Columns: []string{"a"}, IncludeColumns: []string{"b"}},
			`access method "brin" does not support included columns`},
		{"include spgist", "spgist", parser.CreateIndexStmt{Columns: []string{"p"}, IncludeColumns: []string{"a"}}, ""},

		// amcanmulticol: hash and spgist are the two that lack it.
		{"multicol spgist", "spgist", parser.CreateIndexStmt{Columns: []string{"p", "box1"}},
			`access method "spgist" does not support multicolumn indexes`},
		{"multicol hash", "hash", parser.CreateIndexStmt{Columns: []string{"a", "b"}},
			`access method "hash" does not support multicolumn indexes`},
		{"multicol gist", "gist", parser.CreateIndexStmt{Columns: []string{"p", "box1"}}, ""},

		// amcanorder: only btree. A DESC key and an explicit NULLS FIRST on an
		// ascending key are the two orderings this AST still distinguishes
		// from SORTBY_DEFAULT.
		{"desc spgist", "spgist", parser.CreateIndexStmt{Columns: []string{"p"}, ColOrders: []parser.IndexColOrder{desc}},
			`access method "spgist" does not support ASC/DESC options`},
		{"desc brin", "brin", parser.CreateIndexStmt{Columns: []string{"a"}, ColOrders: []parser.IndexColOrder{desc}},
			`access method "brin" does not support ASC/DESC options`},
		{"nulls first spgist", "spgist", parser.CreateIndexStmt{Columns: []string{"p"}, ColOrders: []parser.IndexColOrder{ascNullsFirst}},
			`access method "spgist" does not support NULLS FIRST/LAST options`},
		{"desc btree", "btree", parser.CreateIndexStmt{Columns: []string{"a"}, ColOrders: []parser.IndexColOrder{desc}}, ""},
		{"plain spgist", "spgist", parser.CreateIndexStmt{Columns: []string{"p"}, ColOrders: []parser.IndexColOrder{plain}}, ""},

		// Upstream checks unique BEFORE include and include BEFORE multicol,
		// so a statement that trips several must report the first one.
		{"order unique before multicol", "spgist",
			parser.CreateIndexStmt{Unique: true, Columns: []string{"p", "box1"}},
			`access method "spgist" does not support unique indexes`},
		{"order include before multicol", "hash",
			parser.CreateIndexStmt{Columns: []string{"a", "b"}, IncludeColumns: []string{"p"}},
			`access method "hash" does not support included columns`},

		// An AM name outside the six in-tree index AMs has no capability row;
		// the diagnosis belongs to the access-method existence check, not here.
		{"unknown am", "nosuchmethod", parser.CreateIndexStmt{Unique: true, Columns: []string{"a", "b"}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmt := tc.stmt
			err := checkIndexAMCapabilities(tc.method, &stmt)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("%s: want accepted, got error %q", tc.name, err.Message)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s: want error %q, got accepted", tc.name, tc.want)
			}
			if err.Message != tc.want {
				t.Fatalf("%s: want error %q, got %q", tc.name, tc.want, err.Message)
			}
			// Every one of these is ERRCODE_FEATURE_NOT_SUPPORTED upstream.
			if err.Code != "0A000" {
				t.Fatalf("%s: want SQLSTATE 0A000, got %q", tc.name, err.Code)
			}
		})
	}
}
