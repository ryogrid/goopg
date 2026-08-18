package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableAddPrimaryKeyBareRegistersNotNull and its siblings pin
// M0134-0005o: `ALTER TABLE t ADD [CONSTRAINT n] PRIMARY KEY (cols)` must
// synthesize a NOT NULL constraint for every PK column (already correct
// in-memory before this slice) AND cascade it to already-existing
// inheritance children (missing before this slice —
// execAlterTableAddPrimaryKey never called cascadeNotNullToChildren, unlike
// its AlterTableSetNotNull/AlterTableAddNotNull siblings, M0134-0005i). The
// on-disk pg_attribute heap re-sync (acceptance criteria 1/2) is guarded at
// the testport level (internal/testport/addpk_notnull_test.go) since
// newDDLFixture here has no registered non-virtual pg_attribute heap table
// for catalogHeapSyncAvailable to find.

func TestAlterTableAddPrimaryKeyBareRegistersNotNull(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_bare (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_bare ADD PRIMARY KEY (a)`); err != nil {
		t.Fatalf("ALTER TABLE ADD PRIMARY KEY: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pk_bare"})
	if !ok {
		t.Fatal("pk_bare not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Fatalf("pk_bare.a NotNull = false after bare ADD PRIMARY KEY, want true")
	}
	found := false
	for _, nc := range tbl.NotNullConstraints {
		if nc.ColName == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no NOT NULL constraint registered for pk_bare.a: %+v", tbl.NotNullConstraints)
	}
}

// TestAlterTableAddConstraintPrimaryKeyRegistersNotNull verifies the SAME
// applies via the named spelling `ADD CONSTRAINT n PRIMARY KEY (cols)` —
// both spellings dispatch through the same parser.AlterTableAddPrimaryKey
// arm (internal/parser/ddl.go:9865), so one fix must cover both.
func TestAlterTableAddConstraintPrimaryKeyRegistersNotNull(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_named (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE pk_named ADD CONSTRAINT pk_named_pkey PRIMARY KEY (a)`); err != nil {
		t.Fatalf("ALTER TABLE ADD CONSTRAINT ... PRIMARY KEY: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pk_named"})
	if !ok {
		t.Fatal("pk_named not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Fatalf("pk_named.a NotNull = false after named ADD CONSTRAINT PRIMARY KEY, want true")
	}
}

// TestAlterTableAddPrimaryKeyCascadesToExistingChild is acceptance criterion
// 3: a child created via INHERITS BEFORE the parent's ADD PRIMARY KEY ends
// up with the SAME not-null state on the inherited column as one created
// after. Pre-fix, execAlterTableAddPrimaryKey never called
// cascadeNotNullToChildren, so the pre-existing child's column stayed
// nullable — verified by stashing the fix (see report.md) and reproducing
// exactly this failure.
func TestAlterTableAddPrimaryKeyCascadesToExistingChild(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE pk_parent (a int, b int)`,
		`CREATE TABLE pk_child () INHERITS (pk_parent)`,
		`ALTER TABLE pk_parent ADD PRIMARY KEY (a)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "pk_child"})
	if !ok {
		t.Fatal("pk_child not found")
	}
	colIdx := -1
	for i, c := range child.Columns {
		if c.Name == "a" {
			colIdx = i
		}
	}
	if colIdx == -1 {
		t.Fatal("pk_child has no column a")
	}
	if !child.Columns[colIdx].NotNull {
		t.Fatalf("pk_child.a NotNull = false after parent ADD PRIMARY KEY, want true (cascade to pre-existing child did not happen)")
	}
	found := false
	for _, nc := range child.NotNullConstraints {
		if nc.ColName == "a" {
			found = true
			if nc.InhCount != 1 {
				t.Errorf("pk_child's inherited NOT NULL constraint InhCount = %d, want 1", nc.InhCount)
			}
		}
	}
	if !found {
		t.Fatalf("no NOT NULL constraint recorded for pk_child.a: %+v", child.NotNullConstraints)
	}
}

// TestAlterTableAddPrimaryKeyOnAlreadyNotNullColumnDoesNotDuplicate guards
// against a duplicate-cascade or duplicate-constraint-row regression: a PK
// added on a column that is already NOT NULL via a prior explicit
// constraint must not register a second row (the existing alreadyHadNotNull
// guard at operators_ddl.go must still short-circuit before the new cascade
// call this slice adds).
func TestAlterTableAddPrimaryKeyOnAlreadyNotNullColumnDoesNotDuplicate(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE pk_dup (a int NOT NULL, b int)`,
		`ALTER TABLE pk_dup ADD PRIMARY KEY (a)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pk_dup"})
	if !ok {
		t.Fatal("pk_dup not found")
	}
	count := 0
	for _, nc := range tbl.NotNullConstraints {
		if nc.ColName == "a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("NOT NULL constraint rows for pk_dup.a = %d, want exactly 1 (no duplicate)", count)
	}
}
