package main

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestResolveWALBuffersFollowsPG pins M0122-0008 slice 4 against PG 18.3's
// check_wal_buffers / XLOGChooseNumBuffers (xlog.c). wal_buffers is counted
// in XLOG_BLCKSZ pages with boot value -1 (auto-tune). goopg kept it in bytes
// with a fixed 16MB default, so SHOW wal_buffers printed 16777216 where PG
// prints the auto-tuned size (4MB under the 128MB shared_buffers default).
func TestResolveWALBuffersFollowsPG(t *testing.T) {
	for _, tc := range []struct {
		name      string
		conf      string // wal_buffers line, "" for none
		slots     int    // shared_buffers in 8kB slots
		wantPages int64
		wantShow  string
		wantSrc   misc.Source
	}{
		// shared_buffers/32 pages.
		{"auto, 128MB shared_buffers", "", 16384, 512, "4MB", misc.SourceDefault},
		// Capped at one 16MB WAL segment.
		{"auto, 2GB shared_buffers", "", 262144, 2048, "16MB", misc.SourceDefault},
		// Raised to at least 8 pages.
		{"auto, 1MB shared_buffers", "", 128, 8, "64kB", misc.SourceDefault},
		// An explicit -1 outranks the dynamic default: PGC_S_OVERRIDE.
		{"explicit -1", "-1", 16384, 512, "4MB", misc.SourceOverride},
		{"manual 100MB", "100MB", 16384, 12800, "100MB", misc.SourceConfigFile},
		// Manual values below 4 pages become 4.
		{"manual 16kB", "16kB", 16384, 4, "32kB", misc.SourceConfigFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := misc.BuildDefaultRegistry()
			if tc.conf != "" {
				if err := r.ApplyConfigEntries([]misc.ConfigEntry{{Name: "wal_buffers", Value: tc.conf}}); err != nil {
					t.Fatal(err)
				}
			}
			if got := resolveWALBuffers(r, tc.slots); got != tc.wantPages {
				t.Errorf("resolveWALBuffers = %d pages, want %d", got, tc.wantPages)
			}
			v, _ := r.Get("wal_buffers")
			if got := v.FormatDisplayValue(v.Value); got != tc.wantShow {
				t.Errorf("SHOW wal_buffers = %q, want %q", got, tc.wantShow)
			}
			if v.Source != tc.wantSrc {
				t.Errorf("source = %v, want %v", v.Source, tc.wantSrc)
			}
			if v.BootVal != "-1" {
				t.Errorf("boot_val = %q, want -1", v.BootVal)
			}
		})
	}
}
