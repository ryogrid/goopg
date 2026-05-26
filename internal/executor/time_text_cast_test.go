package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func runSQL(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	op, err := Build(planOne(t, sql, ctx.Catalog))
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("Run(%q): %v", sql, err)
	}
	return rows
}

func TestTimeTextCastPreservesTimeOfDay(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE TABLE time_cast_t (f1 time(2))`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO time_cast_t VALUES ('00:00')`,
		`INSERT INTO time_cast_t VALUES ('02:03 PST')`,
		`INSERT INTO time_cast_t VALUES ('11:59:59.99 PM')`,
		`INSERT INTO time_cast_t VALUES ('2003-03-07 15:36:39 America/New_York')`,
	} {
		runSQL(t, ctx, q)
	}
	rows := runSQL(t, ctx, `SELECT f1::text FROM time_cast_t ORDER BY f1`)
	got := []string{
		rows[0][0].StringValue(),
		rows[1][0].StringValue(),
		rows[2][0].StringValue(),
		rows[3][0].StringValue(),
	}
	want := []string{"00:00:00", "02:03:00", "15:36:39", "23:59:59.99"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows=%v, want %v", got, want)
		}
	}
}

func TestTimeLiteralTextCastPreservesTimeOfDay(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	rows := runSQL(t, ctx, `SELECT '02:03 PST'::time::text, '2003-03-07 15:36:39 America/New_York'::time::text`)
	got := []string{rows[0][0].StringValue(), rows[0][1].StringValue()}
	want := []string{"02:03:00", "15:36:39"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows=%v, want %v", got, want)
		}
	}
}
