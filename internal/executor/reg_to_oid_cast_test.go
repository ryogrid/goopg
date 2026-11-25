package executor

import "testing"

// TestRegCastBackToOid pins reg* → oid on a value an <oid>::reg* cast has
// already rendered as a name (M0149-0001h1a17): PG's reg* → oid cast is
// binary-coercible, so `0::regproc::oid` is 0 and `1299::regproc::oid` is
// 1299. goopg raised oidin's 22P02 on "-" / "now".
func TestRegCastBackToOid(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	rows := runQueryRows(t, ctx, "SELECT 0::regproc::oid, 1299::regproc::oid, 23::regtype::oid, 1259::regclass::oid, 551::regoperator::oid, 1705::regprocedure::oid, 0::regoper::oid, 99999::regclass::oid")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	want := []int64{0, 1299, 23, 1259, 551, 1705, 0, 99999}
	for i, w := range want {
		if rows[0][i].Kind != KindInt || rows[0][i].Int != w {
			t.Errorf("column %d = %+v, want %d", i, rows[0][i], w)
		}
	}
}
