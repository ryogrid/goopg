package pgcluster

import (
	"os"
	"path/filepath"
	"testing"
)

// WaitReady must not connect before postmaster.pid's PM_STATUS line says
// the postmaster finished starting: a connection during crash recovery is
// refused with a logged FATAL that the E2E log-cleanliness checks fail on
// (AI-20261103-004134-001). The line layout is PG 18's pidfile.h; the
// accepted values are pg_ctl's (wait_for_postmaster_start).
func TestPostmasterDoneStartingReadsPMStatusLine(t *testing.T) {
	dir := t.TempDir()
	c := &Cluster{dataDir: dir}
	head := "4242\n" + dir + "\n1762000000\n5432\n/tmp\n127.0.0.1\n  1234567    98304\n"
	for _, tc := range []struct {
		name    string
		content *string
		want    bool
	}{
		{"missing file", nil, false},
		{"status line not written yet", ptr(head), false},
		{"starting", ptr(head + "starting\n"), false},
		{"stopping", ptr(head + "stopping\n"), false},
		{"ready", ptr(head + "ready   \n"), true},
		{"standby", ptr(head + "standby \n"), true},
	} {
		path := filepath.Join(dir, "postmaster.pid")
		_ = os.Remove(path)
		if tc.content != nil {
			if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.postmasterDoneStarting(); got != tc.want {
			t.Errorf("%s: postmasterDoneStarting() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func ptr(s string) *string { return &s }
