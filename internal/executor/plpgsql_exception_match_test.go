package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/pl/plpgsql"
)

// TestExceptionHandlerMatchesPG pins exception_matches_conditions (pl_exec.c)
// over the plerrcodes.h name table: names beyond the old hand-written list
// (undefined_file), SQLSTATE 'xxxxx' conditions, class conditions matching
// by category, and OTHERS not catching query_canceled / assert_failure.
func TestExceptionHandlerMatchesPG(t *testing.T) {
	sq := func(c string) string { return plpgsql.SQLStateConditionPrefix + c }
	cases := []struct {
		conds []string
		state string
		want  bool
	}{
		{[]string{"undefined_file"}, "58P01", true},
		{[]string{"UNDEFINED_FILE"}, "58P01", true},
		{[]string{sq("58P01")}, "58P01", true},
		{[]string{sq("58P01")}, "58P02", false},
		{[]string{sq("22000")}, "22012", true}, // class condition
		{[]string{"data_exception"}, "22012", true},
		{[]string{"integrity_constraint_violation"}, "23505", true},
		{[]string{"division_by_zero"}, "22000", false},
		{[]string{"others"}, "22012", true},
		{[]string{"others"}, "57014", false},
		{[]string{"others"}, "P0004", false},
		{[]string{"query_canceled"}, "57014", true},
		{[]string{"unique_violation", "undefined_file"}, "58P01", true},
		{[]string{"no_such_condition"}, "P0001", false},
	}
	for _, c := range cases {
		if got := exceptionHandlerMatches(c.conds, c.state); got != c.want {
			t.Errorf("exceptionHandlerMatches(%v, %s) = %v, want %v", c.conds, c.state, got, c.want)
		}
	}
	if got := conditionNameToSQLState("reading_sql_data_not_permitted"); got != "2F004" {
		t.Errorf("RAISE reading_sql_data_not_permitted = %s, want 2F004 (first plerrcodes entry)", got)
	}
	if got := conditionNameToSQLState("undefined_file"); got != "58P01" {
		t.Errorf("RAISE undefined_file = %s, want 58P01", got)
	}
}
