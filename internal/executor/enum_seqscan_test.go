package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestEnumSeqScanFilterComparison verifies that seq scan filter predicates on enum columns
// use sort order comparison, not alphabetical comparison.
func TestEnumSeqScanFilterComparison(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// Create enum type and table - NO INDEX (force seqscan)
	for _, stmt := range []string{
		`CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`,
		`CREATE TABLE enumtest (col rainbow)`,
		`INSERT INTO enumtest VALUES ('red')`,
		`INSERT INTO enumtest VALUES ('orange')`,
		`INSERT INTO enumtest VALUES ('yellow')`,
		`INSERT INTO enumtest VALUES ('green')`,
		`INSERT INTO enumtest VALUES ('blue')`,
		`INSERT INTO enumtest VALUES ('purple')`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tests := []struct {
		sql      string
		expected []string
	}{
		{`SELECT col FROM enumtest WHERE col > 'yellow' ORDER BY col`, []string{"green", "blue", "purple"}},
		{`SELECT col FROM enumtest WHERE col >= 'yellow' ORDER BY col`, []string{"yellow", "green", "blue", "purple"}},
		{`SELECT col FROM enumtest WHERE col < 'green' ORDER BY col`, []string{"red", "orange", "yellow"}},
		{`SELECT col FROM enumtest WHERE col <= 'green' ORDER BY col`, []string{"red", "orange", "yellow", "green"}},
		{`SELECT max(col) FROM enumtest WHERE col < 'green'`, []string{"yellow"}},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, tt.sql)
			var got []string
			for _, row := range rows {
				got = append(got, row[0].StringValue())
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.expected) {
				t.Errorf("got %v want %v", got, tt.expected)
			}
		})
	}
}

// TestEnumSeqScanAfterInsenum simulates the insenum renumbering scenario
// from the enum.sql test and verifies rainbow filter comparisons still work.
func TestEnumSeqScanAfterInsenum(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// First create the rainbow enum (as in enum.sql line 5)
	if err := runDDL(t, ctx, `CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`); err != nil {
		t.Fatalf("create rainbow: %v", err)
	}

	// Then simulate insenum: create + lots of ADD VALUE operations (triggering renumbering)
	for _, stmt := range []string{
		`CREATE TYPE insenum AS ENUM ('L1', 'L2')`,
		`ALTER TYPE insenum ADD VALUE 'i1' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i2' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i3' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i4' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i5' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i6' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i7' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i8' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i9' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i10' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i11' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i12' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i13' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i14' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i15' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i16' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i17' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i18' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i19' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i20' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i21' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i22' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i23' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i24' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i25' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i26' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i27' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i28' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i29' BEFORE 'L2'`,
		`ALTER TYPE insenum ADD VALUE 'i30' BEFORE 'L2'`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("insenum setup %q: %v", stmt, err)
		}
	}

	// Now create the enumtest table and insert values (as in enum.sql lines 131-136)
	for _, stmt := range []string{
		`CREATE TABLE enumtest (col rainbow)`,
		`INSERT INTO enumtest VALUES ('red')`,
		`INSERT INTO enumtest VALUES ('orange')`,
		`INSERT INTO enumtest VALUES ('yellow')`,
		`INSERT INTO enumtest VALUES ('green')`,
		`INSERT INTO enumtest VALUES ('blue')`,
		`INSERT INTO enumtest VALUES ('purple')`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("enumtest setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tests := []struct {
		sql      string
		expected []string
	}{
		{`SELECT col FROM enumtest WHERE col > 'yellow' ORDER BY col`, []string{"green", "blue", "purple"}},
		{`SELECT col FROM enumtest WHERE col < 'green' ORDER BY col`, []string{"red", "orange", "yellow"}},
		{`SELECT max(col) FROM enumtest`, []string{"purple"}},
		{`SELECT max(col) FROM enumtest WHERE col < 'green'`, []string{"yellow"}},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, tt.sql)
			var got []string
			for _, row := range rows {
				got = append(got, row[0].StringValue())
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.expected) {
				t.Errorf("got %v want %v", got, tt.expected)
			}
		})
	}
}

// runQueryWithErr executes a SQL query and returns the rows and any execution error.
func runQueryWithErr(ctx *Context, sql string) ([]Row, error) {
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return nil, err
	}
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		return nil, err
	}
	defer op.Close()
	var rows []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, slot.Materialize().Row())
	}
	return rows, nil
}

// TestDomainCheckConstraintEnforced verifies that domain CHECK (VALUE IN (...))
// constraints are enforced during casts. M0097-domain-check.
func TestDomainCheckConstraintEnforced(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`,
		`CREATE DOMAIN rgb AS rainbow CHECK (VALUE IN ('red', 'green', 'blue'))`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Valid cast should succeed.
	rows, err := runQueryWithErr(ctx, `SELECT 'red'::rainbow::rgb`)
	if err != nil {
		t.Fatalf("valid domain cast failed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected 1 row for valid domain cast")
	}

	// Invalid cast (purple not in rgb domain) should fail.
	_, err = runQueryWithErr(ctx, `SELECT 'purple'::rainbow::rgb`)
	if err == nil {
		t.Fatal("expected error for invalid domain cast, got nil")
	}
	if !strings.Contains(err.Error(), "rgb_check") && !strings.Contains(err.Error(), "check constraint") {
		t.Fatalf("expected domain check error, got: %v", err)
	}
}

// TestEnumRenameToWorks verifies that ALTER TYPE name RENAME TO new_name works.
func TestEnumRenameToWorks(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TYPE bogus AS ENUM ('good', 'bad', 'ugly')`,
		`ALTER TYPE bogus RENAME TO bogon`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	// enum_range should work with the new name.
	rows, err := runQueryWithErr(ctx, `SELECT enum_range(null::bogon)`)
	if err != nil {
		t.Fatalf("enum_range(null::bogon) failed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected 1 row for enum_range(null::bogon)")
	}
	got := rows[0][0].StringValue()
	if got != "{good,bad,ugly}" {
		t.Errorf("enum_range(null::bogon) = %q, want {good,bad,ugly}", got)
	}

	// Old name should return null (type no longer registered as bogus).
	rows2, err2 := runQueryWithErr(ctx, `SELECT enum_range(null::bogus)`)
	if err2 != nil {
		t.Logf("enum_range(null::bogus) after rename returned error: %v (acceptable)", err2)
	} else if len(rows2) > 0 && rows2[0][0].StringValue() == "{good,bad,ugly}" {
		t.Errorf("bogus should not return same values as bogon after rename")
	}
}
