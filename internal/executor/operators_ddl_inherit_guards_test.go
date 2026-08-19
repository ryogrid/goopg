package executor

// operators_ddl_inherit_guards_test.go pins the M0134-0002 C9 inheritance
// guards on the ALTER-TABLE executor: goopg must REFUSE the inherited-DDL
// operations PostgreSQL refuses, byte-exact message + SQLSTATE, instead of
// silently mutating inherited state.
//
//	DROP COLUMN on an inherited column
//	    → 42P16 cannot drop inherited column "a"
//	    (ATExecDropColumn, tablecmds.c:9347-9351)
//	RENAME COLUMN on an inherited column
//	    → 42P16 cannot rename inherited column "a"
//	    (renameatt_internal, tablecmds.c:3960-3964)
//	RENAME COLUMN ONLY on a parent with children
//	    → 42P16 inherited column "a" must be renamed in child tables too
//	    (renameatt_internal, tablecmds.c:3912-3917)
//	ADD COLUMN ONLY on a parent with children
//	    → 42P16 column must be added to child tables too
//	    (ATExecAddColumn, tablecmds.c:7600-7603)
//	DROP CONSTRAINT on an inherited check
//	    → 42P16 cannot drop inherited constraint "con1" of relation "part_c"
//	    (dropconstraint_internal, tablecmds.c:14102-14107)
//	RENAME CONSTRAINT on an inherited check / ONLY on a parent
//	    → 42P16 cannot rename inherited constraint "con1"
//	    → 42P16 inherited constraint "con1" must be renamed in child tables too
//	    (rename_constraint_internal, tablecmds.c:4114-4126)
//
// Every failure case builds a REAL `INHERITS` or `PARTITION OF` relationship
// through the DDL executor so the inherited flags (catalog.Column.Inherited,
// NamedCheckConstraint.InhCount) are genuinely set by CREATE TABLE /
// ADD CONSTRAINT propagation — never faked in the fixture.

import (
	"testing"
)

// wantExecError asserts err is an *ExecError carrying exactly code+message.
func wantExecError(t *testing.T, err error, code, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected *ExecError{%s %q}, got nil", code, message)
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err = %v (%T), want *ExecError{%s %q}", err, err, code, message)
	}
	if ee.Code != code {
		t.Errorf("Code = %q, want %q (message %q)", ee.Code, code, ee.Message)
	}
	if ee.Message != message {
		t.Errorf("Message = %q, want %q", ee.Message, message)
	}
}

// TestAlterTableInheritGuardsDropColumn pins the DROP COLUMN inherited guard:
// an inherited column cannot be dropped from a child (with or without ONLY),
// while a parent's own (non-inherited) column and a child's locally-declared
// column still drop fine.
func TestAlterTableInheritGuardsDropColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE drop_p (a int, b int)",
		"CREATE TABLE drop_c () INHERITS (drop_p)",
		"CREATE TABLE drop_c2 (lcol int) INHERITS (drop_p)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		code    string
		message string
	}{
		{
			name:    "inherited column refused",
			sql:     "ALTER TABLE drop_c DROP COLUMN a",
			wantErr: true,
			code:    "42P16",
			message: `cannot drop inherited column "a"`,
		},
		{
			name:    "inherited column refused even with only",
			sql:     "ALTER TABLE ONLY drop_c DROP COLUMN b",
			wantErr: true,
			code:    "42P16",
			message: `cannot drop inherited column "b"`,
		},
		{
			// Parent's own column is not inherited — drops fine (no recursion
			// in goopg, but that is a separate gap from the guard).
			name: "parent local column drop ok",
			sql:  "ALTER TABLE drop_p DROP COLUMN a",
		},
		{
			name: "child local column drop ok",
			sql:  "ALTER TABLE drop_c2 DROP COLUMN lcol",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: %v", tc.sql, err)
				}
				return
			}
			wantExecError(t, err, tc.code, tc.message)
		})
	}
}

// TestAlterTableInheritGuardsRenameColumn pins the RENAME COLUMN guards: an
// inherited column cannot be renamed from a child; `ONLY` on a parent with
// children is refused with the "must be renamed in child tables too" message;
// local columns and childless ONLY tables still rename fine.
func TestAlterTableInheritGuardsRenameColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rn_p (a int, b int)",
		"CREATE TABLE rn_c () INHERITS (rn_p)",
		"CREATE TABLE rn_c2 (lcol int) INHERITS (rn_p)",
		"CREATE TABLE rn_leaf (a int)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		code    string
		message string
	}{
		{
			name:    "inherited column cannot be renamed",
			sql:     "ALTER TABLE rn_c RENAME COLUMN a TO z",
			wantErr: true,
			code:    "42P16",
			message: `cannot rename inherited column "a"`,
		},
		{
			name:    "only on parent with children refused",
			sql:     "ALTER TABLE ONLY rn_p RENAME COLUMN a TO z",
			wantErr: true,
			code:    "42P16",
			message: `inherited column "a" must be renamed in child tables too`,
		},
		{
			name: "recursive parent rename ok",
			sql:  "ALTER TABLE rn_p RENAME COLUMN a TO z",
		},
		{
			name: "child local column rename ok",
			sql:  "ALTER TABLE rn_c2 RENAME COLUMN lcol TO l2",
		},
		{
			name: "only on leaf table ok",
			sql:  "ALTER TABLE ONLY rn_leaf RENAME COLUMN a TO z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: %v", tc.sql, err)
				}
				return
			}
			wantExecError(t, err, tc.code, tc.message)
		})
	}
}

// TestAlterTableInheritGuardsAddColumnOnly pins the ADD COLUMN ONLY guard:
// `ADD COLUMN` with ONLY on a parent that has children is refused; the
// recursive (non-ONLY) form and a childless ONLY table still work.
func TestAlterTableInheritGuardsAddColumnOnly(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE add_p (a int)",
		"CREATE TABLE add_c () INHERITS (add_p)",
		"CREATE TABLE add_leaf (a int)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		wantErr bool
		code    string
		message string
	}{
		{
			name:    "only on parent with children refused",
			sql:     "ALTER TABLE ONLY add_p ADD COLUMN x int",
			wantErr: true,
			code:    "42P16",
			message: "column must be added to child tables too",
		},
		{
			name: "recursive add column ok",
			sql:  "ALTER TABLE add_p ADD COLUMN x int",
		},
		{
			name: "only on leaf table ok",
			sql:  "ALTER TABLE ONLY add_leaf ADD COLUMN x int",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("%s: %v", tc.sql, err)
				}
				return
			}
			wantExecError(t, err, tc.code, tc.message)
		})
	}
}

// TestAlterTableInheritGuardsDropConstraint pins the DROP CONSTRAINT inherited
// guard on a PARTITION OF child whose parent got an inherited CHECK via ADD
// CONSTRAINT propagation. ALTER TABLE ... ADD CONSTRAINT CHECK now cascades
// to the full descendant set — plain-INHERITS children AND partitions
// (M0134-0005as) — but a CREATE TABLE ... INHERITS of an ALREADY-EXISTING
// parent CHECK still does not copy it onto the new child (that CREATE-time
// gap is untouched by this brief).
func TestAlterTableInheritGuardsDropConstraint(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE part_p (a int) PARTITION BY LIST (a)",
		"CREATE TABLE part_c PARTITION OF part_p FOR VALUES IN (1)",
		"ALTER TABLE part_p ADD CONSTRAINT con1 CHECK (a > 0)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	err := runDDL(t, ctx, "ALTER TABLE part_c DROP CONSTRAINT con1")
	wantExecError(t, err, "42P16", `cannot drop inherited constraint "con1" of relation "part_c"`)
}

// TestAlterTableInheritGuardsRenameConstraint pins the RENAME CONSTRAINT
// inherited guards (rename_constraint_internal, tablecmds.c:4114-4126): an
// inherited constraint cannot be renamed from the child, and `ONLY` on a parent
// with children is refused with the "must be renamed in child tables too"
// message.
func TestAlterTableInheritGuardsRenameConstraint(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE part_p (a int) PARTITION BY LIST (a)",
		"CREATE TABLE part_c PARTITION OF part_p FOR VALUES IN (1)",
		"ALTER TABLE part_p ADD CONSTRAINT con1 CHECK (a > 0)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tests := []struct {
		name    string
		sql     string
		code    string
		message string
	}{
		{
			name:    "inherited constraint cannot be renamed",
			sql:     "ALTER TABLE part_c RENAME CONSTRAINT con1 TO con1foo",
			code:    "42P16",
			message: `cannot rename inherited constraint "con1"`,
		},
		{
			name:    "only on parent with children refused",
			sql:     "ALTER TABLE ONLY part_p RENAME CONSTRAINT con1 TO con1foo",
			code:    "42P16",
			message: `inherited constraint "con1" must be renamed in child tables too`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runDDL(t, ctx, tc.sql)
			wantExecError(t, err, tc.code, tc.message)
		})
	}
}

// TestAlterTableInheritGuardsRenameConstraintNoInherit pins PG's NO INHERIT
// exemption in rename_constraint_internal: the whole guard block is gated on
// `contype == CHECK || NOTNULL && !con->connoinherit` (tablecmds.c:4086-4089),
// so `ONLY` on a parent with children must NOT refuse renaming a NO INHERIT
// constraint.
func TestAlterTableInheritGuardsRenameConstraintNoInherit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rn_p (a int)",
		"CREATE TABLE rn_c () INHERITS (rn_p)",
		"ALTER TABLE rn_p ADD CONSTRAINT con1 CHECK (a > 0) NO INHERIT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	if err := runDDL(t, ctx, "ALTER TABLE ONLY rn_p RENAME CONSTRAINT con1 TO con1foo"); err != nil {
		t.Fatalf("rename NO INHERIT constraint with ONLY on a parent: %v", err)
	}
}

// TestAlterTableInheritGuardsNoInheritClearsFlag pins the NO INHERIT flag
// clearing (the atacc3 regress case): after `NO INHERIT` the column is local in
// PG (attinhcount 1→0), so renaming/dropping it must succeed — the C9 inherited
// guard must NOT fire on the stale flag.
func TestAlterTableInheritGuardsNoInheritClearsFlag(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE ni_p (a int)",
		"CREATE TABLE ni_c () INHERITS (ni_p)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	// Before NO INHERIT the column IS inherited — the guard fires.
	err := runDDL(t, ctx, "ALTER TABLE ni_c RENAME COLUMN a TO z")
	wantExecError(t, err, "42P16", `cannot rename inherited column "a"`)
	// After NO INHERIT the column is local: rename and drop both succeed.
	if err := runDDL(t, ctx, "ALTER TABLE ni_c NO INHERIT ni_p"); err != nil {
		t.Fatalf("NO INHERIT: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE ni_c RENAME COLUMN a TO z"); err != nil {
		t.Fatalf("rename after NO INHERIT: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE ni_c DROP COLUMN z"); err != nil {
		t.Fatalf("drop after NO INHERIT: %v", err)
	}
}

// TestAlterTableInheritGuardsParentSideDrop pins the live-hierarchy narrowing:
// after the parent drops a column (ONLY, so no recursion), the child's copy is
// no longer inherited in the live hierarchy (PG: attinhcount 1→0), so the child
// can drop it — the guard must NOT fire on the stale flag. A column still
// present in the parent remains guarded.
func TestAlterTableInheritGuardsParentSideDrop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE ps_p (a int, b int)",
		"CREATE TABLE ps_c () INHERITS (ps_p)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	// Parent-side ONLY drop removes a from the parent; the child's copy is now
	// local (stale Inherited flag must not fire the guard).
	if err := runDDL(t, ctx, "ALTER TABLE ONLY ps_p DROP COLUMN a"); err != nil {
		t.Fatalf("parent-only drop: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE ps_c DROP COLUMN a"); err != nil {
		t.Fatalf("child drop after parent-only drop: %v", err)
	}
	// The still-live column b remains inherited — the guard still fires.
	err := runDDL(t, ctx, "ALTER TABLE ps_c DROP COLUMN b")
	wantExecError(t, err, "42P16", `cannot drop inherited column "b"`)
}
