package main

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/utils/misc"
)

// TestRecordClusterStateGUCs pins M0122-0008 slice 7: data_checksums reports
// pg_control's data_checksum_version, and huge_pages_status reports "off"
// (goopg never maps huge pages) once the server has started. PG 18.3 stores
// both at startup as dynamic defaults; boot values stay off / unknown.
func TestRecordClusterStateGUCs(t *testing.T) {
	for _, checksums := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "data")
		if err := initdb.Init(initdb.Options{DataDir: dir, NoSync: true, DataChecksums: checksums}); err != nil {
			t.Fatal(err)
		}
		r := misc.BuildDefaultRegistry()
		recordClusterStateGUCs(r, dir)
		want := map[bool]string{false: "off", true: "on"}[checksums]
		if v, _ := r.Get("data_checksums"); v.Value != want || v.BootVal != "off" {
			t.Errorf("checksums=%v: data_checksums = %q (boot %q), want %q (boot off)", checksums, v.Value, v.BootVal, want)
		}
		if v, _ := r.Get("huge_pages_status"); v.Value != "off" || v.BootVal != "unknown" {
			t.Errorf("huge_pages_status = %q (boot %q), want off (boot unknown)", v.Value, v.BootVal)
		}
	}
}
