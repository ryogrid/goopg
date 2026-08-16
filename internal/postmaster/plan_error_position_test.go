package postmaster

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// TestPlanErrorPosition covers the M0134-0001 P3b simple-path fix:
// planErrorHintFields must carry the wire-protocol FieldPosition ('P') field,
// 1-based, when PlanError.Pos is set (> 0). Sites that leave Pos unset (=0)
// must emit no 'P' field at all — matching the ExecError convention — rather
// than spuriously pointing at character 1.
func TestPlanErrorPosition(t *testing.T) {
	fields := planErrorHintFields(&optimizer.PlanError{Code: "42803", Message: "x", Pos: 5})
	pos := ""
	for _, f := range fields {
		if f.Code == libpq.FieldPosition {
			pos = f.Value
		}
	}
	if pos != "6" {
		t.Fatalf("FieldPosition = %q, want %q (PlanError.Pos 5 is 0-based -> wire 6)", pos, "6")
	}

	// Pos unset (0) must emit no 'P' field.
	fields = planErrorHintFields(&optimizer.PlanError{Code: "42803", Message: "x"})
	for _, f := range fields {
		if f.Code == libpq.FieldPosition {
			t.Fatalf("Pos=0 emitted a FieldPosition field %q; want none", f.Value)
		}
	}
}

// TestNewPlannerExtendedQueryError covers the M0134-0001 P3b extended-path fix:
// dispatch_extended.go now builds the extendedQueryError from the whole
// *planner.PlanError (Code/Message/Detail/Hint/Position) instead of dropping
// everything but code+message via planErrorFields.
func TestNewPlannerExtendedQueryError(t *testing.T) {
	eq := newPlannerExtendedQueryError(&optimizer.PlanError{
		Code:    "42803",
		Message: "aggregate function calls cannot be nested",
		Detail:  "d",
		Hint:    "h",
		Pos:     5,
	})
	if eq.Position != 6 {
		t.Errorf("Position = %d, want 6 (PlanError.Pos 5 is 0-based -> wire 6)", eq.Position)
	}
	if eq.Detail != "d" {
		t.Errorf("Detail = %q, want %q", eq.Detail, "d")
	}
	if eq.Hint != "h" {
		t.Errorf("Hint = %q, want %q", eq.Hint, "h")
	}
	if eq.Code != errcodes.Code("42803") {
		t.Errorf("Code = %q, want %q", eq.Code, "42803")
	}
	if eq.Message != "aggregate function calls cannot be nested" {
		t.Errorf("Message = %q, want the PlanError message", eq.Message)
	}

	// Non-PlanError falls back to FeatureNotSupported, mirroring planErrorFields.
	eq = newPlannerExtendedQueryError(errors.New("boom"))
	if eq.Code != errcodes.FeatureNotSupported {
		t.Errorf("fallback Code = %q, want %q", eq.Code, errcodes.FeatureNotSupported)
	}
	if eq.Message != "boom" {
		t.Errorf("fallback Message = %q, want %q", eq.Message, "boom")
	}
	if eq.Position != 0 {
		t.Errorf("fallback Position = %d, want 0", eq.Position)
	}
}
