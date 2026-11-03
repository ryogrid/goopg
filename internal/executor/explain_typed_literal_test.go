package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestExplainTextLiteralArgLabel pins M0148-0001f3's literal slice (PG 18.3
// output): a string literal passed to a text parameter of a built-in
// function or aggregate prints with get_const_expr's `::text` label; a
// literal whose overload resolves to another type (string_agg's bytea
// variant) is left unlabelled.
func TestExplainTextLiteralArgLabel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE lt (t text, b bytea)")
	out := func(q string) string {
		lines := renderRows(runSQLWith(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) "+q, optimizer.DefaultPlannerSettings()))
		for _, l := range lines {
			if s := strings.TrimSpace(l); strings.HasPrefix(s, "Output: ") {
				return strings.TrimPrefix(s, "Output: ")
			}
		}
		return ""
	}
	for _, tc := range []struct{ q, want string }{
		{"SELECT string_agg(t, ',') FROM lt", "string_agg(t, ','::text)"},
		{"SELECT regexp_split_to_array(t, ',') FROM lt", "regexp_split_to_array(t, ','::text)"},
	} {
		if got := out(tc.q); got != tc.want {
			t.Errorf("%s: Output %q, want (PG 18.3) %q", tc.q, got, tc.want)
		}
	}
	if got := out("SELECT string_agg(b, 'x') FROM lt"); strings.Contains(got, "::text") {
		t.Errorf("bytea string_agg: literal labelled text: %q", got)
	}
}
