package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestInitWALSegSize pins initdb --wal-segsize: pg_control's xlog_seg_size,
// a bootstrap segment of that size whose checkpoint sits after segment 1's
// long page header, min/max_wal_size scaled by pretty_wal_size, and the
// power-of-two validation. M0149-0022a.
func TestInitWALSegSize(t *testing.T) {
	t.Cleanup(func() { xlog.SetWALSegmentSize(xlog.DefaultSegmentSize) })
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true, WALSegSizeMB: 1}); err != nil {
		t.Fatal(err)
	}
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cd.XLogSegSize != 1<<20 {
		t.Errorf("xlog_seg_size = %d, want %d", cd.XLogSegSize, 1<<20)
	}
	if cd.CheckPoint != 1<<20+40 || cd.CheckPointCopyRedo != 1<<20+40 {
		t.Errorf("checkpoint = %X redo = %X, want %X", cd.CheckPoint, cd.CheckPointCopyRedo, 1<<20+40)
	}
	st, err := os.Stat(filepath.Join(dir, "pg_wal", "000000010000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 1<<20 {
		t.Errorf("bootstrap segment size = %d, want %d", st.Size(), 1<<20)
	}
	conf, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nmin_wal_size = 5MB", "\nmax_wal_size = 64MB"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("postgresql.conf lacks %q", strings.TrimSpace(want))
		}
	}
	err = Init(Options{DataDir: filepath.Join(t.TempDir(), "bad"), NoSync: true, WALSegSizeMB: 3})
	if err == nil || err.Error() != "argument of --wal-segsize must be a power of two between 1 and 1024" {
		t.Errorf("--wal-segsize 3: err = %v", err)
	}
}

// TestPrettyWALSize pins initdb's pretty_wal_size.
func TestPrettyWALSize(t *testing.T) {
	for _, c := range []struct {
		mb, segs int
		want     string
	}{{16, 5, "80MB"}, {16, 64, "1GB"}, {1, 5, "5MB"}, {1, 64, "64MB"}, {64, 64, "4GB"}} {
		if got := prettyWALSize(c.mb, c.segs); got != c.want {
			t.Errorf("prettyWALSize(%d, %d) = %q, want %q", c.mb, c.segs, got, c.want)
		}
	}
}
