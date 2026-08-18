package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCheckConstraintNotEnforcedSkipsRuntimeEvaluation verifies M0134-0005
// Bucket 2: a CHECK constraint declared NOT ENFORCED must never be evaluated
// at INSERT/UPDATE time, matching PG's ExecRelCheck
// (postgres/src/backend/executor/execMain.c:1813-1815 — "Skip not enforced
// constraint" / `if (!check[i].ccenforced) continue;`). Prior to this fix,
// checkConstraints (internal/executor/operators_fk.go) looped
// tbl.CheckConstraints unconditionally and never consulted
// tbl.NamedChecks[i].NotEnforced, so goopg raised 23514 on rows PG accepts.
func TestCheckConstraintNotEnforcedSkipsRuntimeEvaluation(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// (1) A NOT ENFORCED CHECK must accept violating rows on INSERT and UPDATE.
	if err := runDDL(t, ctx, `CREATE TABLE ne_check_tbl (x integer, CONSTRAINT check_con CHECK (x > 3) NOT ENFORCED)`); err != nil {
		t.Fatalf("CREATE TABLE ne_check_tbl: %v", err)
	}
	for _, v := range []string{"5", "4", "3", "2", "6", "1"} {
		if err := runDDL(t, ctx, `INSERT INTO ne_check_tbl VALUES (`+v+`)`); err != nil {
			t.Fatalf("INSERT INTO ne_check_tbl VALUES (%s): unexpected error %v (NOT ENFORCED check must not fire)", v, err)
		}
	}
	rows := runQuery(t, ctx, `SELECT x FROM ne_check_tbl ORDER BY x`)
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows in ne_check_tbl, got %d: %v", len(rows), rows)
	}

	// UPDATE path must also skip the NOT ENFORCED check.
	if err := runDDL(t, ctx, `UPDATE ne_check_tbl SET x = 1 WHERE x = 6`); err != nil {
		t.Fatalf("UPDATE ne_check_tbl SET x=1: unexpected error %v (NOT ENFORCED check must not fire)", err)
	}

	// (2) A normal (enforced) CHECK on another table must still raise 23514 —
	// the skip must not over-fire onto enforced constraints.
	if err := runDDL(t, ctx, `CREATE TABLE e_check_tbl (x integer, CONSTRAINT check_con CHECK (x > 3))`); err != nil {
		t.Fatalf("CREATE TABLE e_check_tbl: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO e_check_tbl VALUES (5)`); err != nil {
		t.Fatalf("INSERT INTO e_check_tbl VALUES (5): unexpected error %v", err)
	}
	err := runDDL(t, ctx, `INSERT INTO e_check_tbl VALUES (2)`)
	if err == nil {
		t.Fatal("INSERT INTO e_check_tbl VALUES (2) should violate the enforced CHECK constraint")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23514" {
		t.Errorf("Code = %q, want 23514", ee.Code)
	}

	// (3) A table with BOTH an enforced and a NOT ENFORCED check evaluates
	// only the enforced one — the index-alignment regression guard: if
	// checkConstraints ever indexed tbl.NamedChecks by the wrong offset, this
	// would either wrongly skip the enforced check or wrongly fire the
	// NOT ENFORCED one.
	if err := runDDL(t, ctx, `CREATE TABLE mixed_check_tbl (x integer, y integer,
		CONSTRAINT enforced_chk CHECK (x > 3),
		CONSTRAINT unenforced_chk CHECK (y > 100) NOT ENFORCED)`); err != nil {
		t.Fatalf("CREATE TABLE mixed_check_tbl: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "mixed_check_tbl"})
	if !ok {
		t.Fatal("mixed_check_tbl table not found")
	}
	if len(tbl.NamedChecks) != 2 {
		t.Fatalf("expected 2 named checks, got %+v", tbl.NamedChecks)
	}

	// x > 3 satisfied, y > 100 violated but NOT ENFORCED -> must succeed.
	if err := runDDL(t, ctx, `INSERT INTO mixed_check_tbl VALUES (5, 1)`); err != nil {
		t.Fatalf("INSERT INTO mixed_check_tbl VALUES (5, 1): unexpected error %v (unenforced_chk must not fire)", err)
	}
	// x > 3 violated, y > 100 satisfied -> must fail on the enforced check.
	err = runDDL(t, ctx, `INSERT INTO mixed_check_tbl VALUES (1, 200)`)
	if err == nil {
		t.Fatal("INSERT INTO mixed_check_tbl VALUES (1, 200) should violate enforced_chk")
	}
	ee, ok = err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23514" {
		t.Errorf("Code = %q, want 23514", ee.Code)
	}
	if got := ee.Message; got == "" || !strings.Contains(got, "enforced_chk") {
		t.Errorf("Message = %q, want it to name enforced_chk", got)
	}
}
