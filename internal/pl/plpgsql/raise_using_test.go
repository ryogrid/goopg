package plpgsql

import (
	"strings"
	"testing"
)

func raiseOf(t *testing.T, body string) *RaiseStmt {
	t.Helper()
	fn, err := Parse("BEGIN " + body + " END")
	if err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	for _, s := range fn.Statements {
		if r, ok := s.(*RaiseStmt); ok {
			return r
		}
	}
	t.Fatalf("no RAISE in %q", body)
	return nil
}

// TestRaiseUsingOptions pins pl_gram.y stmt_raise: the format string and its
// arguments stop at a top-level USING, each `name = expr` option keeps its
// expression text, SQLSTATE 'xxxxx' is a condition, and bare RAISE re-raises.
func TestRaiseUsingOptions(t *testing.T) {
	r := raiseOf(t, "RAISE NOTICE USING message = sqlstate;")
	if r.Level != "notice" || r.Msg != "" || len(r.Options) != 1 || r.Options[0] != (RaiseOption{"MESSAGE", "sqlstate"}) {
		t.Fatalf("got %+v", r)
	}
	r = raiseOf(t, "RAISE EXCEPTION 'a %, b', f(1, 2) USING DETAIL = 'x, y', HINT := h || 'z';")
	if r.Msg != "'a %, b', f(1, 2)" || len(r.Options) != 2 ||
		r.Options[0] != (RaiseOption{"DETAIL", "'x, y'"}) || r.Options[1] != (RaiseOption{"HINT", "h || 'z'"}) {
		t.Fatalf("got %+v", r)
	}
	r = raiseOf(t, "RAISE SQLSTATE '22012' USING message = 'm';")
	if r.SQLState != "22012" || r.ConditionName != "" || r.Level != "exception" {
		t.Fatalf("got %+v", r)
	}
	r = raiseOf(t, "RAISE division_by_zero;")
	if r.ConditionName != "division_by_zero" || r.Msg != "" {
		t.Fatalf("got %+v", r)
	}
	if r = raiseOf(t, "RAISE;"); !r.ReRaise {
		t.Fatalf("bare RAISE not a re-raise: %+v", r)
	}
	if _, err := Parse("BEGIN RAISE EXCEPTION USING foo = 1; END"); err == nil ||
		!strings.Contains(err.Error(), `unrecognized RAISE statement option "foo"`) {
		t.Fatalf("unknown option: %v", err)
	}
}
