package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// TestPgInputIsValidEnum verifies that pg_input_is_valid correctly returns
// false for values not in the enum and true for valid members. M0097-0071.
func TestPgInputIsValidEnum(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.RegisterEnum("rainbow", []string{"red", "orange", "yellow", "green", "blue", "purple"}); err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}

	ctx := &Context{Catalog: cat}

	cases := []struct {
		value string
		valid bool
	}{
		{"red", true},
		{"green", true},
		{"purple", true},
		{"mauve", false},
		{"", false},
		{"RED", true},   // case-insensitive
		{"MAUVE", false},
	}

	for _, tc := range cases {
		fc := &planner.FuncCall{
			Name: "pg_input_is_valid",
			Args: []planner.Expr{
				&planner.StringConst{Value: tc.value},
				&planner.StringConst{Value: "rainbow"},
			},
		}
		got, err := evalFuncCall(fc, nil, ctx)
		if err != nil {
			t.Errorf("pg_input_is_valid(%q, 'rainbow') error: %v", tc.value, err)
			continue
		}
		want := tc.valid
		if got.Kind != KindBool || (got.Int != 0) != want {
			t.Errorf("pg_input_is_valid(%q, 'rainbow') = %v, want %v", tc.value, got, want)
		}
	}
}

// TestPgInputErrorInfoEnum verifies that pg_input_error_info returns one row
// with the correct message for an invalid enum value. M0097-0071.
func TestPgInputErrorInfoEnum(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.RegisterEnum("rainbow", []string{"red", "orange", "yellow", "green", "blue", "purple"}); err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}

	ctx := &Context{Catalog: cat}

	// Build a fake PgInputErrorInfo plan.
	pgPlan := &planner.PgInputErrorInfo{
		Value: &planner.StringConst{Value: "mauve"},
		Type:  &planner.StringConst{Value: "rainbow"},
	}
	op := newPgInputErrorInfoOp(pgPlan)
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}

	slot, err := op.Next()
	if err != nil {
		t.Fatalf("Next error for invalid enum: %v", err)
	}
	if slot == nil {
		t.Fatal("Next returned nil slot for invalid enum value — expected 1 row")
	}
	row := slot.Row()
	if len(row) < 1 {
		t.Fatal("Expected at least 1 column in result row")
	}
	msg := row[0].StringValue()
	wantMsg := `invalid input value for enum rainbow: "mauve"`
	if msg != wantMsg {
		t.Errorf("message = %q, want %q", msg, wantMsg)
	}
	// Code should be 22P02.
	if len(row) >= 4 {
		code := row[3].StringValue()
		if code != "22P02" {
			t.Errorf("sql_error_code = %q, want 22P02", code)
		}
	}

	// Second call should return EOF.
	_, err2 := op.Next()
	if err2 != EOF {
		t.Errorf("second Next should be EOF, got err=%v", err2)
	}

	// Valid enum value → 0 rows.
	pgPlan2 := &planner.PgInputErrorInfo{
		Value: &planner.StringConst{Value: "red"},
		Type:  &planner.StringConst{Value: "rainbow"},
	}
	op2 := newPgInputErrorInfoOp(pgPlan2)
	if err := op2.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err3 := op2.Next()
	if err3 != EOF {
		t.Errorf("Next for valid enum should be EOF immediately, got %v", err3)
	}
}
