package plpgsql

import "testing"

// TestExceptionWhenSQLStateCondition: `WHEN SQLSTATE 'xxxxx'` keeps its code
// (pl_gram.y proc_condition) instead of dropping the string literal, and a
// malformed code is rejected with PG's message.
func TestExceptionWhenSQLStateCondition(t *testing.T) {
	fn, err := Parse(`BEGIN NULL; EXCEPTION WHEN SQLSTATE '58P01' OR division_by_zero THEN NULL; END`)
	if err != nil {
		t.Fatal(err)
	}
	var eb *ExceptionBlock
	for _, s := range fn.Statements {
		if b, ok := s.(*ExceptionBlock); ok {
			eb = b
		}
	}
	if eb == nil || len(eb.Handlers) != 1 {
		t.Fatalf("no exception block parsed: %#v", fn.Statements)
	}
	got := eb.Handlers[0].Conditions
	if len(got) != 2 || got[0] != SQLStateConditionPrefix+"58P01" || got[1] != "division_by_zero" {
		t.Fatalf("conditions = %q", got)
	}
	if _, err := Parse(`BEGIN NULL; EXCEPTION WHEN SQLSTATE '58p01' THEN NULL; END`); err == nil {
		t.Fatal("lower-case SQLSTATE code accepted; PG says invalid SQLSTATE code")
	}
}
