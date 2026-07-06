package executor

import "testing"

// TestDomainColumnNotNullEnforced verifies that a domain declared with NOT NULL
// rejects a NULL value assigned to a table column of that domain type, not just
// on an explicit `::domain` cast. M0122-0005 (deferral ledger 2026-07-06
// "domain constraint enforcement gap").
func TestDomainColumnNotNullEnforced(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE DOMAIN nn_int AS int NOT NULL`,
		`CREATE TABLE t_nn (p nn_int)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `INSERT INTO t_nn VALUES (5)`); err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}

	err := runDDL(t, ctx, `INSERT INTO t_nn VALUES (NULL)`)
	if err == nil {
		t.Fatal("expected 23502 error inserting NULL into a NOT NULL domain column, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23502" {
		t.Fatalf("expected ExecError 23502, got: %v", err)
	}
}

// TestDomainColumnGenericCheckEnforced verifies that a domain's generic
// (non `VALUE IN (...)`) CHECK predicate is enforced when a plain value is
// assigned to a table column of that domain type. M0122-0005.
func TestDomainColumnGenericCheckEnforced(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE DOMAIN pos_int AS int CHECK (VALUE > 0)`,
		`CREATE TABLE t_pos (p pos_int)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `INSERT INTO t_pos VALUES (5)`); err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}

	err := runDDL(t, ctx, `INSERT INTO t_pos VALUES (-5)`)
	if err == nil {
		t.Fatal("expected 23514 error inserting -5 into a CHECK (VALUE > 0) domain column, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected ExecError 23514, got: %v", err)
	}
}

// TestDomainColumnInCheckEnforced verifies the VALUE IN (...) fast-path domain
// CHECK form is also enforced on a plain (non-cast) column assignment. M0122-0005.
func TestDomainColumnInCheckEnforced(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE DOMAIN small_num AS int CHECK (VALUE IN (1, 2, 3))`,
		`CREATE TABLE t_small (p small_num)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `INSERT INTO t_small VALUES (2)`); err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}

	err := runDDL(t, ctx, `INSERT INTO t_small VALUES (9)`)
	if err == nil {
		t.Fatal("expected 23514 error inserting 9 into a VALUE IN (1,2,3) domain column, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected ExecError 23514, got: %v", err)
	}
}
