package executor

// operators_ddl_c11a_view_guard_test.go — M0134-0002 C11a: ALTER TABLE
// structural actions aimed at a view must be refused with PG's exact
// ATSimplePermissions error instead of silently succeeding.
//
//	ALTER TABLE <view> DROP COLUMN / ALTER COLUMN ... SET|DROP NOT NULL
//	    → 42809 ALTER action <action> cannot be performed on relation "<view>"
//	      DETAIL: This operation is not supported for views.
//	    (ATSimplePermissions, tablecmds.c:6739; the ATT_VIEW bit in the
//	     AT_* switch, tablecmds.c:4943-5282)
//
// Views ARE still allowed to go through the SAME execAlterTable dispatch for
// RENAME, ALTER COLUMN SET DEFAULT, and reloptions SET/RESET — those are
// covered as positive cases so an over-broad refuse-list would be caught.
//
// Regress fixture: postgres/src/test/regress/sql/alter_table.sql:1160-1167,
// 1519-1520 (the `myview`/atacc1 shape).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func viewGuardFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, s := range []string{
		"CREATE TABLE atacc1 (a int, test int, d int)",
		"CREATE VIEW myview AS SELECT * FROM atacc1",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			cleanup()
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	return ctx, cleanup
}

// TestAlterTableOnViewRelkindGuard covers the three failing regress cases
// plus the positive (still-allowed) actions on a view.
func TestAlterTableOnViewRelkindGuard(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		ctx, cleanup := viewGuardFixture(t)
		defer cleanup()

		for _, tc := range []struct {
			name    string
			sql     string
			message string
		}{
			{
				name:    "drop not null",
				sql:     "ALTER TABLE myview ALTER COLUMN test DROP NOT NULL",
				message: `ALTER action ALTER COLUMN ... DROP NOT NULL cannot be performed on relation "myview"`,
			},
			{
				name:    "set not null",
				sql:     "ALTER TABLE myview ALTER COLUMN test SET NOT NULL",
				message: `ALTER action ALTER COLUMN ... SET NOT NULL cannot be performed on relation "myview"`,
			},
			{
				name:    "drop column",
				sql:     "ALTER TABLE myview DROP d",
				message: `ALTER action DROP COLUMN cannot be performed on relation "myview"`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := runDDL(t, ctx, tc.sql)
				wantExecError(t, err, "42809", tc.message)
				ee := err.(*ExecError)
				if ee.Detail != "This operation is not supported for views." {
					t.Errorf("Detail = %q, want %q", ee.Detail, "This operation is not supported for views.")
				}
				// ATSimplePermissions/errdetail_relkind_not_supported never call
				// errposition (postgres/src/backend/commands/tablecmds.c),
				// so PG's regress .out carries no "LINE N:" cursor here.
				if ee.Pos != 0 {
					t.Errorf("Pos = %d, want 0", ee.Pos)
				}

				// Acceptance criterion 2: the refusal happens before any
				// catalog/storage mutation — the view's column set is
				// unchanged after the refused ALTER.
				view, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "myview"})
				if !ok {
					t.Fatal("myview not found in catalog after refused ALTER")
				}
				wantCols := []string{"a", "test", "d"}
				if len(view.Columns) != len(wantCols) {
					t.Fatalf("myview has %d columns after refused ALTER, want %d (%v)", len(view.Columns), len(wantCols), view.Columns)
				}
				for i, name := range wantCols {
					if view.Columns[i].Name != name || view.Columns[i].Dropped {
						t.Errorf("myview.Columns[%d] = %+v, want name=%q not dropped", i, view.Columns[i], name)
					}
				}
			})
		}
	})

	// Additional structural refusals beyond the three regress cases, still
	// within the Scope's named action set (ADD COLUMN / ADD CONSTRAINT /
	// DROP CONSTRAINT), guarding against a too-narrow refuse-list.
	t.Run("refused additional structural actions", func(t *testing.T) {
		ctx, cleanup := viewGuardFixture(t)
		defer cleanup()

		for _, tc := range []struct {
			name    string
			sql     string
			message string
		}{
			{
				name:    "add column",
				sql:     "ALTER TABLE myview ADD COLUMN e int",
				message: `ALTER action ADD COLUMN cannot be performed on relation "myview"`,
			},
			{
				name:    "alter column type",
				sql:     "ALTER TABLE myview ALTER COLUMN test TYPE text",
				message: `ALTER action ALTER COLUMN ... SET DATA TYPE cannot be performed on relation "myview"`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := runDDL(t, ctx, tc.sql)
				wantExecError(t, err, "42809", tc.message)
			})
		}
	})

	// Acceptance criterion 3: allowed actions must keep working — no
	// over-refusal.
	t.Run("still allowed", func(t *testing.T) {
		ctx, cleanup := viewGuardFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, "ALTER TABLE myview RENAME TO myview2"); err != nil {
			t.Errorf("RENAME TO on view: %v", err)
		}
		if err := runDDL(t, ctx, "ALTER TABLE myview2 ALTER COLUMN test SET DEFAULT 0"); err != nil {
			t.Errorf("ALTER COLUMN SET DEFAULT on view: %v", err)
		}
		if err := runDDL(t, ctx, "ALTER VIEW myview2 SET (security_barrier = true)"); err != nil {
			t.Errorf("ALTER VIEW SET (security_barrier) on view: %v", err)
		}
	})
}
