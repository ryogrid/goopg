package executor

import "testing"

// TestAlterDomainAddConstraintRejectsTransitiveColumnUse verifies that
// `ALTER DOMAIN ... ADD CONSTRAINT` is rejected with PG's 0A000
// (ERRCODE_FEATURE_NOT_SUPPORTED) "cannot alter type ... uses it" error
// whenever a table column's type transitively contains the domain being
// altered — not just when a column is declared directly as that domain.
// Mirrors postgres/src/test/regress/sql/domain.sql's "Check that ALTER
// DOMAIN tests columns of derived types" block (expected/domain.out:1092 and
// the 4 further occurrences at lines 1098/1105/1113/1121). PG oracle:
// get_rels_with_domain (typecmds.c:3316) / find_composite_type_dependencies
// (tablecmds.c:6936, error at tablecmds.c:7039-7044). M0134-0067 Bucket 3.
func TestAlterDomainAddConstraintRejectsTransitiveColumnUse(t *testing.T) {
	cases := []struct {
		name  string
		setup []string
	}{
		{
			name: "plain domain column",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TABLE ddtest2(f1 posint)`,
			},
		},
		{
			name: "composite field",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TYPE ddtest1 AS (f1 posint)`,
				`CREATE TABLE ddtest2(f1 ddtest1)`,
			},
		},
		{
			name: "composite array",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TYPE ddtest1 AS (f1 posint)`,
				`CREATE TABLE ddtest2(f1 ddtest1[])`,
			},
		},
		{
			name: "domain over composite",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TYPE ddtest1 AS (f1 posint)`,
				`CREATE DOMAIN ddtest1d AS ddtest1`,
				`CREATE TABLE ddtest2(f1 ddtest1d)`,
			},
		},
		{
			name: "domain over array of composite",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TYPE ddtest1 AS (f1 posint)`,
				`CREATE DOMAIN ddtest1d AS ddtest1[]`,
				`CREATE TABLE ddtest2(f1 ddtest1d)`,
			},
		},
		{
			name: "range over domain",
			setup: []string{
				`CREATE DOMAIN posint AS int4`,
				`CREATE TYPE rposint AS RANGE (subtype = posint)`,
				`CREATE TABLE ddtest2(f1 rposint)`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()

			for _, stmt := range tc.setup {
				if err := runDDL(t, ctx, stmt); err != nil {
					t.Fatalf("setup %q: %v", stmt, err)
				}
			}

			err := runDDL(t, ctx, `ALTER DOMAIN posint ADD CONSTRAINT c1 CHECK (VALUE >= 0)`)
			if err == nil {
				t.Fatal("expected 0A000 error, got nil")
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("expected *ExecError, got %T: %v", err, err)
			}
			if ee.Code != "0A000" {
				t.Fatalf("expected ExecError 0A000, got %s: %s", ee.Code, ee.Message)
			}
			wantMsg := `cannot alter type "posint" because column "ddtest2.f1" uses it`
			if ee.Message != wantMsg {
				t.Fatalf("message mismatch:\n got:  %s\n want: %s", ee.Message, wantMsg)
			}
		})
	}
}

// TestAlterDomainAddConstraintAllowsNonDependentTable is a regression guard
// against false positives: a table with no column using the domain (directly
// or transitively) must NOT be rejected. M0134-0067 Bucket 3.
func TestAlterDomainAddConstraintAllowsNonDependentTable(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE DOMAIN posint AS int4`,
		`CREATE TABLE t2(f1 int4)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN posint ADD CONSTRAINT c1 CHECK (VALUE >= 0)`); err != nil {
		t.Fatalf("unexpected error for non-dependent table: %v", err)
	}
}
