package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestInfinityCellText: date, timestamp and timestamptz print their infinity
// values by name. The sentinel is not a real instant; formatting it as one gave
// dates in 2262 and 1677.
func TestInfinityCellText(t *testing.T) {
	srv := &Server{}
	for _, tc := range []struct {
		typ  string
		d    executor.Datum
		want string
	}{
		{"date", executor.NewDateInfinity(true), "infinity"},
		{"date", executor.NewDateInfinity(false), "-infinity"},
		{"timestamp", executor.NewTimestampInfinity(true), "infinity"},
		{"timestamp", executor.NewTimestampInfinity(false), "-infinity"},
		{"timestamptz", executor.NewTimestampInfinity(true), "infinity"},
		{"timestamptz", executor.NewTimestampInfinity(false), "-infinity"},
	} {
		got := string(srv.appendTypedCellText(nil, tc.d, catalog.Type{Name: tc.typ}, nil, nil))
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.typ, got, tc.want)
		}
	}
}
