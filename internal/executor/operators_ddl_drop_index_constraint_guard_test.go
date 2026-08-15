package executor

// operators_ddl_drop_index_constraint_guard_test.go pins `DROP INDEX`'s 2BP01
// dependency guard (M0134-0002 C2): an index backing a UNIQUE / PRIMARY KEY /
// EXCLUDE constraint cannot be dropped directly — PG raises `cannot drop index
// %s because constraint %s on table %s requires it` +
// `You can drop constraint %s on table %s instead.`
// (postgres/src/backend/catalog/dependency.c:780-795, performDeletion). A bare
// `CREATE UNIQUE INDEX` (no constraint) must still drop cleanly.

import (
	"fmt"
	"testing"
)

// TestDropIndexConstraintGuard is table-driven over every constraint kind that
// backs an index (UNIQUE / PK / EXCLUDE → 2BP01) plus the two droppable
// non-constraint shapes (bare unique index, plain index). Each row runs on a
// fresh table so index names cannot collide across subtests.
func TestDropIndexConstraintGuard(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name     string
		table    string
		setup    []string // DDL to create the index (+ constraint) before the DROP
		dropSQL  string
		wantErr  bool // true → expect *ExecError with the pinned Code/Message/Hint
		wantCode string
		wantMsg  string
		wantHint string
	}{
		{
			name:     "unique constraint backing index 2BP01",
			table:    "dig_unique",
			setup:    []string{"ALTER TABLE dig_unique ADD CONSTRAINT dig_unique_a UNIQUE (a)"},
			dropSQL:  "DROP INDEX dig_unique_a",
			wantErr:  true,
			wantCode: "2BP01",
			wantMsg:  "cannot drop index dig_unique_a because constraint dig_unique_a on table dig_unique requires it",
			wantHint: "You can drop constraint dig_unique_a on table dig_unique instead.",
		},
		{
			name:     "primary key backing index 2BP01",
			table:    "dig_pk",
			setup:    []string{"ALTER TABLE dig_pk ADD CONSTRAINT dig_pk_a PRIMARY KEY (a)"},
			dropSQL:  "DROP INDEX dig_pk_a",
			wantErr:  true,
			wantCode: "2BP01",
			wantMsg:  "cannot drop index dig_pk_a because constraint dig_pk_a on table dig_pk requires it",
			wantHint: "You can drop constraint dig_pk_a on table dig_pk instead.",
		},
		{
			name:     "exclude constraint backing index 2BP01",
			table:    "dig_excl",
			setup:    []string{"ALTER TABLE dig_excl ADD CONSTRAINT dig_excl_a EXCLUDE USING btree (a WITH =)"},
			dropSQL:  "DROP INDEX dig_excl_a",
			wantErr:  true,
			wantCode: "2BP01",
			wantMsg:  "cannot drop index dig_excl_a because constraint dig_excl_a on table dig_excl requires it",
			wantHint: "You can drop constraint dig_excl_a on table dig_excl instead.",
		},
		{
			name:    "bare unique index still drops",
			table:   "dig_bare",
			setup:   []string{"CREATE UNIQUE INDEX dig_bare_a ON dig_bare (a)"},
			dropSQL: "DROP INDEX dig_bare_a",
			wantErr: false,
		},
		{
			name:    "plain index still drops",
			table:   "dig_plain",
			setup:   []string{"CREATE INDEX dig_plain_a ON dig_plain (a)"},
			dropSQL: "DROP INDEX dig_plain_a",
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runDDL(t, ctx, fmt.Sprintf("CREATE TABLE %s (a int, b int)", tc.table)); err != nil {
				t.Fatalf("CREATE TABLE %s: %v", tc.table, err)
			}
			for _, ddl := range tc.setup {
				if err := runDDL(t, ctx, ddl); err != nil {
					t.Fatalf("setup %q: %v", ddl, err)
				}
			}
			err := runDDL(t, ctx, tc.dropSQL)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", tc.dropSQL, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s: got nil error, want SQLSTATE %s", tc.dropSQL, tc.wantCode)
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("%s: err = %v (%T), want *ExecError", tc.dropSQL, err, err)
			}
			if ee.Code != tc.wantCode || ee.Message != tc.wantMsg || ee.Hint != tc.wantHint {
				t.Errorf("%s: got Code=%q Message=%q Hint=%q, want Code=%q Message=%q Hint=%q",
					tc.dropSQL, ee.Code, ee.Message, ee.Hint, tc.wantCode, tc.wantMsg, tc.wantHint)
			}
		})
	}
}

// TestDropIndexConstraintGuardPGMessageShapes pins the exact unquoted PG error
// text (mirrors TestAlterTableRenameConstraintPGMessageShapes's shape
// assertions): names are bare `%s`, never `%q`-quoted, matching
// getObjectDescription in dependency.c.
func TestDropIndexConstraintGuardPGMessageShapes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dig_shape (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE dig_shape ADD CONSTRAINT dig_shape_a UNIQUE (a)"); err != nil {
		t.Fatalf("ADD CONSTRAINT UNIQUE: %v", err)
	}

	err := runDDL(t, ctx, "DROP INDEX dig_shape_a")
	if err == nil {
		t.Fatal("dropping a constraint-backed index should error")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err = %v (%T), want *ExecError", err, err)
	}
	wantMsg := "cannot drop index dig_shape_a because constraint dig_shape_a on table dig_shape requires it"
	wantHint := "You can drop constraint dig_shape_a on table dig_shape instead."
	if ee.Code != "2BP01" || ee.Message != wantMsg || ee.Hint != wantHint {
		t.Errorf("err = %v, want 2BP01 %q / hint %q", err, wantMsg, wantHint)
	}
}
