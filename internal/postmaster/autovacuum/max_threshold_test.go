package autovacuum

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestAutovacuumVacuumMaxThresholdFollowsPG pins M0122-0008 slice 6 against
// PG 18.3 (guc_tables.c; autovacuum.c relation_needs_vacanalyze):
// autovacuum_vacuum_max_threshold defaults to 100000000. Any value >= 0 caps
// the vacuum threshold, so 0 makes every dead tuple count; -1 disables the
// cap. goopg defaulted to 200000000 and read the setting only when it was
// positive, so 0 and -1 both silently fell back to the default.
func TestAutovacuumVacuumMaxThresholdFollowsPG(t *testing.T) {
	for _, tc := range []struct {
		set  string // "" = default
		want int64
	}{
		{"", 100_000_000},
		{"0", 0},
		{"-1", -1},
		{"5000", 5000},
	} {
		r := misc.BuildDefaultRegistry()
		if tc.set != "" {
			if err := r.ApplyConfigEntries([]misc.ConfigEntry{{Name: "autovacuum_vacuum_max_threshold", Value: tc.set}}); err != nil {
				t.Fatal(err)
			}
		}
		l := &Launcher{GUCs: r}
		if got := l.params().maxThreshold; got != tc.want {
			t.Errorf("autovacuum_vacuum_max_threshold=%q: launcher cap %d, want %d", tc.set, got, tc.want)
		}
	}
	// Without a registry the launcher uses PG's boot value too.
	if got := (&Launcher{}).params().maxThreshold; got != 100_000_000 {
		t.Errorf("no registry: launcher cap %d, want 100000000", got)
	}
}
