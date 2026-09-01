package autovacuum

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestFreezeCutoffYoungClusterFreezesNothing pins review/260831-2 CP-3: with
// the subtraction done in uint32, a cluster younger than freeze_min_age wrapped
// `nextXID - eff` to a value near 2^32, the `fb > oldestXmin` clamp rewrote it
// to oldestXmin, and every dead-to-all tuple was frozen on every pass. Upstream
// keeps the wrapped value and compares it with TransactionIdPrecedes
// (vacuum.c:1204-1209), which puts it freeze_min_age transactions in the past,
// so nothing qualifies; goopg's consumer compares plainly, so the equivalent is
// FreezeBelow == 0 (freezing off for this pass).
func TestFreezeCutoffYoungClusterFreezesNothing(t *testing.T) {
	p := avParams{freezeMinAge: 50_000_000, freezeMaxAge: 200_000_000}
	cases := []struct {
		name       string
		nextXID    storage.TransactionID
		oldestXmin storage.TransactionID
		reloption  int
		want       storage.TransactionID
	}{
		{"fresh cluster", 100, 90, 0, 0},
		{"just below the min age", 50_000_000, 40_000_000, 0, 0},
		{"exactly at the min age", 50_000_003, 40_000_000, 0, 3},
		{"mature cluster, no clamp", 60_000_000, 59_000_000, 0, 10_000_000},
		{"mature cluster, clamped to oldestXmin", 60_000_000, 5_000_000, 0, 5_000_000},
		{"reloption shortens the age", 1_000, 900, 100, 900},
	}
	for _, tc := range cases {
		got := freezeCutoff(tc.nextXID, tc.oldestXmin, p, tc.reloption)
		if got != tc.want {
			t.Errorf("%s: freezeCutoff(%d, %d, reloption=%d) = %d, want %d",
				tc.name, tc.nextXID, tc.oldestXmin, tc.reloption, got, tc.want)
		}
	}
}
