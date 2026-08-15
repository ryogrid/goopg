package executor

// operators_ddl_rename_constraint_test.go pins `ALTER TABLE name RENAME
// CONSTRAINT old TO new` (M0134-0002 C2). Constraint lookup spans the same four
// stores as DROP CONSTRAINT (CHECK → FK → UNIQUE → EXCLUDE → PK, see
// execAlterTableDropConstraint); UNIQUE/PK/EXCLUDE re-key the backing index
// because the constraint name IS the index name (rename_constraint_internal →
// RenameRelationInternal on conindid, tablecmds.c:4129-4134). Error codes match
// PG: 42704 not-found (pg_constraint.c:1234), 42710 CHECK/FK name collision
// (pg_constraint.c:1025), 42P07 index-backed collision (tablecmds.c:4303-4307).

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableRenameConstraint is table-driven over every per-kind rename
// mechanic plus the error-code split. Each row runs on a fresh table so renames
// cannot collide across subtests.
func TestAlterTableRenameConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	for _, tc := range []struct {
		name      string
		table     string
		setup     []string // DDL run on `table` before the rename
		renameSQL string
		wantErr   string // expected SQLSTATE on failure; "" means success
		// verify runs after a successful rename to assert the new state.
		verify func(t *testing.T, tbl *catalog.Table)
	}{
		{
			name:  "check rename keeps OID and siblings",
			table: "rc_check",
			setup: []string{
				"ALTER TABLE rc_check ADD CONSTRAINT chk_a CHECK (a > 0)",
				"ALTER TABLE rc_check ADD CONSTRAINT chk_b CHECK (b > 0)",
			},
			renameSQL: "ALTER TABLE rc_check RENAME CONSTRAINT chk_a TO chk_a_new",
			verify: func(t *testing.T, tbl *catalog.Table) {
				found := false
				for i := range tbl.NamedChecks {
					switch tbl.NamedChecks[i].Name {
					case "chk_a_new":
						if tbl.NamedChecks[i].OID == 0 {
							t.Errorf("renamed CHECK has zero OID")
						}
						found = true
					case "chk_b":
						// sibling untouched
					case "chk_a":
						t.Errorf("old name chk_a still present: %+v", tbl.NamedChecks)
					}
				}
				if !found {
					t.Fatalf("NamedChecks after rename = %+v, want chk_a_new", tbl.NamedChecks)
				}
			},
		},
		{
			name:  "fk rename keeps OID",
			table: "rc_fk",
			setup: []string{
				"CREATE TABLE rc_fk_ref (id int)",
				"ALTER TABLE rc_fk ADD CONSTRAINT fk_a FOREIGN KEY (a) REFERENCES rc_fk_ref(id)",
			},
			renameSQL: "ALTER TABLE rc_fk RENAME CONSTRAINT fk_a TO fk_a_new",
			verify: func(t *testing.T, tbl *catalog.Table) {
				for i := range tbl.ForeignKeys {
					if tbl.ForeignKeys[i].Name == "fk_a_new" {
						if tbl.ForeignKeys[i].OID == 0 {
							t.Errorf("renamed FK has zero OID")
						}
						return
					}
				}
				t.Fatalf("ForeignKeys after rename = %+v, want fk_a_new", tbl.ForeignKeys)
			},
		},
		{
			name:      "unique rename re-keys backing index",
			table:     "rc_unique",
			setup:     []string{"ALTER TABLE rc_unique ADD CONSTRAINT onek_unique1 UNIQUE (a)"},
			renameSQL: "ALTER TABLE rc_unique RENAME CONSTRAINT onek_unique1 TO onek_unique1_new",
			verify: func(t *testing.T, tbl *catalog.Table) {
				// Backing index renamed: look it up under the new name, and the
				// old name must be gone.
				idx, ok := im.LookupIndex(parser.ObjectName{Name: "onek_unique1_new"})
				if !ok {
					t.Fatalf("catalog has no index onek_unique1_new after rename")
				}
				if idx.Table == nil || idx.Table.Name != "rc_unique" {
					t.Fatalf("renamed index lost its owning table")
				}
				if !idx.Unique || !idx.IsConstraint {
					t.Errorf("renamed index lost constraint flags: %+v", idx)
				}
				if _, still := im.LookupIndex(parser.ObjectName{Name: "onek_unique1"}); still {
					t.Errorf("old index name onek_unique1 still present")
				}
				// DROP INDEX by the new name must succeed — the backing index is
				// really there under the new name.
				if err := runDDL(t, ctx, "DROP INDEX onek_unique1_new"); err != nil {
					t.Errorf("DROP INDEX onek_unique1_new: %v", err)
				}
			},
		},
		{
			name:      "pk rename re-keys backing index",
			table:     "rc_pk",
			setup:     []string{"ALTER TABLE rc_pk ADD CONSTRAINT pk_a PRIMARY KEY (a)"},
			renameSQL: "ALTER TABLE rc_pk RENAME CONSTRAINT pk_a TO pk_a_new",
			verify: func(t *testing.T, tbl *catalog.Table) {
				idx, ok := im.LookupIndex(parser.ObjectName{Name: "pk_a_new"})
				if !ok {
					t.Fatalf("catalog has no index pk_a_new after rename")
				}
				if !idx.Primary {
					t.Errorf("renamed index lost Primary flag: %+v", idx)
				}
				if _, still := im.LookupIndex(parser.ObjectName{Name: "pk_a"}); still {
					t.Errorf("old index name pk_a still present")
				}
			},
		},
		{
			name:      "not found 42704",
			table:     "rc_missing",
			renameSQL: "ALTER TABLE rc_missing RENAME CONSTRAINT nosuchconstraint TO whatever",
			wantErr:   "42704",
		},
		{
			name:  "duplicate check 42710",
			table: "rc_dupcheck",
			setup: []string{
				"ALTER TABLE rc_dupcheck ADD CONSTRAINT dup1 CHECK (a > 0)",
				"ALTER TABLE rc_dupcheck ADD CONSTRAINT dup2 CHECK (b > 0)",
			},
			renameSQL: "ALTER TABLE rc_dupcheck RENAME CONSTRAINT dup1 TO dup2",
			wantErr:   "42710",
		},
		{
			name:  "duplicate fk 42710",
			table: "rc_dupfk",
			setup: []string{
				"CREATE TABLE rc_dupfk_ref (id int)",
				"ALTER TABLE rc_dupfk ADD CONSTRAINT fk_1 FOREIGN KEY (a) REFERENCES rc_dupfk_ref(id)",
				"ALTER TABLE rc_dupfk ADD CONSTRAINT fk_2 FOREIGN KEY (b) REFERENCES rc_dupfk_ref(id)",
			},
			renameSQL: "ALTER TABLE rc_dupfk RENAME CONSTRAINT fk_1 TO fk_2",
			wantErr:   "42710",
		},
		{
			name:  "duplicate unique 42P07",
			table: "rc_dupuniq",
			setup: []string{
				"ALTER TABLE rc_dupuniq ADD CONSTRAINT uq_1 UNIQUE (a)",
				"ALTER TABLE rc_dupuniq ADD CONSTRAINT uq_2 UNIQUE (b)",
			},
			renameSQL: "ALTER TABLE rc_dupuniq RENAME CONSTRAINT uq_1 TO uq_2",
			wantErr:   "42P07",
		},
		{
			name:  "duplicate pk 42P07 via unique index name",
			table: "rc_duppk",
			setup: []string{
				"ALTER TABLE rc_duppk ADD CONSTRAINT pk_1 PRIMARY KEY (a)",
				"ALTER TABLE rc_duppk ADD CONSTRAINT uq_other UNIQUE (b)",
			},
			renameSQL: "ALTER TABLE rc_duppk RENAME CONSTRAINT pk_1 TO uq_other",
			wantErr:   "42P07",
		},
		{
			name:      "exclude rename re-keys backing index",
			table:     "rc_excl",
			setup:     []string{"ALTER TABLE rc_excl ADD CONSTRAINT excl_a EXCLUDE USING btree (a WITH =)"},
			renameSQL: "ALTER TABLE rc_excl RENAME CONSTRAINT excl_a TO excl_a_new",
			verify: func(t *testing.T, tbl *catalog.Table) {
				idx, ok := im.LookupIndex(parser.ObjectName{Name: "excl_a_new"})
				if !ok {
					t.Fatalf("catalog has no index excl_a_new after rename")
				}
				if !idx.IsExclusion {
					t.Errorf("renamed index lost IsExclusion flag: %+v", idx)
				}
				if _, still := im.LookupIndex(parser.ObjectName{Name: "excl_a"}); still {
					t.Errorf("old index name excl_a still present")
				}
			},
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
			err := runDDL(t, ctx, tc.renameSQL)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("%s: got nil error, want SQLSTATE %s", tc.renameSQL, tc.wantErr)
				}
				if ee, ok := err.(*ExecError); !ok || ee.Code != tc.wantErr {
					t.Errorf("%s: err = %v, want *ExecError{Code: %s}", tc.renameSQL, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.renameSQL, err)
			}
			tbl, found := im.LookupTable(parser.ObjectName{Name: tc.table})
			if !found {
				t.Fatalf("table %s not found after rename", tc.table)
			}
			if tc.verify != nil {
				tc.verify(t, tbl)
			}
		})
	}
}

// TestAlterTableRenameConstraintPGMessageShapes pins the exact PG error text
// split (mirrors TestAlterDomainRenameConstraint's shape assertions): 42704
// uses "for table", 42710 uses "for relation".
func TestAlterTableRenameConstraintPGMessageShapes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rc_shape (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE rc_shape ADD CONSTRAINT chk_x CHECK (a > 0)"); err != nil {
		t.Fatalf("ADD CONSTRAINT CHECK: %v", err)
	}

	// Not found: `constraint "..." for table "..." does not exist` (42704).
	err := runDDL(t, ctx, "ALTER TABLE rc_shape RENAME CONSTRAINT nosuchcon TO whatever")
	if err == nil {
		t.Fatal("renaming an unknown constraint should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" || ee.Message != `constraint "nosuchcon" for table "rc_shape" does not exist` {
		t.Errorf("err = %v, want 42704 %q", err, `constraint "nosuchcon" for table "rc_shape" does not exist`)
	}

	// Duplicate CHECK: `constraint "..." for relation "..." already exists` (42710).
	if err := runDDL(t, ctx, "ALTER TABLE rc_shape ADD CONSTRAINT chk_y CHECK (b > 0)"); err != nil {
		t.Fatalf("ADD CONSTRAINT CHECK: %v", err)
	}
	err = runDDL(t, ctx, "ALTER TABLE rc_shape RENAME CONSTRAINT chk_x TO chk_y")
	if err == nil {
		t.Fatal("renaming to a colliding CHECK name should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" || ee.Message != `constraint "chk_y" for relation "rc_shape" already exists` {
		t.Errorf("err = %v, want 42710 %q", err, `constraint "chk_y" for relation "rc_shape" already exists`)
	}
}
