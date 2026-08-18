package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/libpq"
)

// TestExecErrorPositionAlterConstraintEnforceability covers M0134-0005d
// slice 1 Part B: the "cannot alter enforceability of constraint" ExecError
// (execAlterTableAlterConstraint, internal/executor/operators_ddl.go, mirroring
// PG's ATExecAlterConstraint, tablecmds.c:12254-12258) must carry NO wire
// FieldPosition — PG's ereport for this case has no parser_errposition call,
// so psql prints no LINE/caret. Mirrors TestPlanErrorPosition
// (plan_error_position_test.go): Pos unset (0) must emit no 'P' field,
// through both the simple-query (execErrDetailFields) and extended-query
// (newExtendedQueryError) wire-conversion paths.
func TestExecErrorPositionAlterConstraintEnforceability(t *testing.T) {
	// The real error as constructed by execAlterTableAlterConstraint post-fix:
	// Code 42809, message text unchanged, Pos left unset.
	ee := &executor.ExecError{
		Code:    "42809",
		Message: `cannot alter enforceability of constraint "unique_tbl_i_key" of relation "unique_tbl"`,
	}

	fields := execErrDetailFields(ee)
	for _, f := range fields {
		if f.Code == libpq.FieldPosition {
			t.Fatalf("execErrDetailFields: Pos=0 emitted a FieldPosition field %q; want none", f.Value)
		}
	}

	eq := newExtendedQueryError(ee)
	if eq.Position != 0 {
		t.Errorf("newExtendedQueryError: Position = %d, want 0 (no parser_errposition in PG's ereport)", eq.Position)
	}
	if eq.Code != "42809" {
		t.Errorf("newExtendedQueryError: Code = %q, want 42809", eq.Code)
	}

	// Regression guard: an ExecError that DOES carry a position (e.g. a
	// syntax-adjacent semantic error) must still surface it — this fix must
	// not silently blind the wire-position machinery for other ExecErrors.
	withPos := &executor.ExecError{Code: "42601", Message: "y", Pos: 5}
	fields = execErrDetailFields(withPos)
	found := false
	for _, f := range fields {
		if f.Code == libpq.FieldPosition {
			found = true
			if f.Value != "6" {
				t.Errorf("FieldPosition = %q, want %q (ExecError.Pos 5 is 0-based -> wire 6)", f.Value, "6")
			}
		}
	}
	if !found {
		t.Errorf("execErrDetailFields: Pos=5 emitted no FieldPosition field; want one")
	}
}
