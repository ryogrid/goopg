package executor

// R84: NULL-ordering values pins for the outer-Sort skip over
// DISTINCT. distinctOp sorts ascending with NULLs last; the skip
// fires only on ASC nulls-last prefixes, so NULL placement must
// be identical with and without the top Sort. The DESC case pins
// the keep-Sort arm end to end.

import (
	"testing"
)

func distinctNullFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE dn (v text)",
		"INSERT INTO dn VALUES ('b'), (NULL), ('a'), ('b'), (NULL), ('c')",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

func TestDistinctAscNullsLastWithSkip(t *testing.T) {
	ctx, cleanup := distinctNullFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "SELECT DISTINCT v FROM dn ORDER BY v")
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		if r[0].IsNull() {
			got = append(got, "NULL")
		} else {
			got = append(got, r[0].Format())
		}
	}
	want := []string{"a", "b", "c", "NULL"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDistinctDescKeepsSort(t *testing.T) {
	ctx, cleanup := distinctNullFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "SELECT DISTINCT v FROM dn ORDER BY v DESC")
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		if r[0].IsNull() {
			got = append(got, "NULL")
		} else {
			got = append(got, r[0].Format())
		}
	}
	// PG DESC default: NULLS FIRST.
	want := []string{"NULL", "c", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
