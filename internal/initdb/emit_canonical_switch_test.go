package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// countCanonicalRecords scans every WAL record under dir/pg_wal and returns
// (canonical records excluding XLOG_PARAMETER_CHANGE, parameter-change
// records). XLOG_PARAMETER_CHANGE is carried in the same RecordKindCanonical
// envelope but is deliberately retained in native-only mode
// (perf-optimize3-dash/01 §3.3 D4), so the switch assertions must not count
// it.
func countCanonicalRecords(t *testing.T, dataDir string) (other, paramChange int) {
	t.Helper()
	records, err := wal.ReadAll(filepath.Join(dataDir, "pg_wal"), 0)
	if err != nil {
		t.Fatalf("wal.ReadAll: %v", err)
	}
	for _, r := range records {
		// ReadAll (PGCompat mode) unwraps the 0xFE canonical envelope during
		// XLogRecord decode: canonical records surface with a decoded header
		// whose Rmid is a REAL rmgr (Heap/Xact/Btree/...), while native
		// records and the checkpoint record classify as RmgrXLog.
		if r.XLog == nil {
			continue
		}
		rm := r.XLog.Header.Rmid
		info := r.XLog.Header.Info
		if rm == wal.RmgrXLog {
			if info == 0x60 { // XLOG_PARAMETER_CHANGE (retained in off mode)
				paramChange++
			}
			continue // native (0xF0), checkpoint, or other control records
		}
		other++ // canonical heap/xact/btree/... content record
	}
	return other, paramChange
}

// runCanonicalSwitchWorkload opens a fresh cluster at dir and runs a small
// DML workload that exercises the canonical heap emitters (insert + HOT
// update + delete) plus a commit, then closes cleanly.
func runCanonicalSwitchWorkload(t *testing.T, dir string) {
	t.Helper()
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDLRealDataDir(t, rt, "CREATE TABLE canon_sw (id int4, val text)")
	runDDLRealDataDir(t, rt, "INSERT INTO canon_sw VALUES (1, 'a'), (2, 'b')")
	runDDLRealDataDir(t, rt, "UPDATE canon_sw SET val = 'c' WHERE id = 1")
	runDDLRealDataDir(t, rt, "DELETE FROM canon_sw WHERE id = 2")
	rt.Close()
}

// TestEmitCanonicalSwitch pins the perf-optimize3-dash S1 switch semantics:
// default (and =on) emits canonical records; =off yields a native-only
// stream in which the ONLY RecordKindCanonical envelope left is the retained
// XLOG_PARAMETER_CHANGE.
func TestEmitCanonicalSwitch(t *testing.T) {
	t.Run("default_on", func(t *testing.T) {
		// Pin the mode explicitly so an ambient GOOPG_WAL_CANONICAL=off in
		// the developer's shell cannot flip this subtest (review nit 2).
		t.Setenv("GOOPG_WAL_CANONICAL", "on")
		dir := filepath.Join(t.TempDir(), "data")
		runCanonicalSwitchWorkload(t, dir)
		other, _ := countCanonicalRecords(t, dir)
		if other == 0 {
			t.Fatalf("default mode: expected canonical records in the stream, found none")
		}
	})

	t.Run("env_off_native_only", func(t *testing.T) {
		t.Setenv("GOOPG_WAL_CANONICAL", "off")
		dir := filepath.Join(t.TempDir(), "data")
		runCanonicalSwitchWorkload(t, dir)
		other, param := countCanonicalRecords(t, dir)
		if other != 0 {
			t.Fatalf("native-only mode: found %d canonical records beyond XLOG_PARAMETER_CHANGE; want 0", other)
		}
		// XLOG_PARAMETER_CHANGE is retained (not gated) in off mode, but a
		// fresh cluster emits it only when a GUC echo differs from
		// pg_control (ReportParameters mirrors PG's diff-only behavior), so
		// its absence here is normal. Log for visibility; the retention
		// property is pinned structurally (choke point 3 keeps only the
		// PageHeadersEnabled gate).
		t.Logf("native-only mode: %d XLOG_PARAMETER_CHANGE records (diff-only emission)", param)
	})

	t.Run("env_off_crash_recovers", func(t *testing.T) {
		// Native-only stream must recover: commit, reopen, data visible.
		t.Setenv("GOOPG_WAL_CANONICAL", "off")
		dir := filepath.Join(t.TempDir(), "data")
		if err := Init(Options{DataDir: dir}); err != nil {
			t.Fatal(err)
		}
		rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
		if err != nil {
			t.Fatal(err)
		}
		runDDLRealDataDir(t, rt1, "CREATE TABLE canon_crash (id int4, val text)")
		runDDLRealDataDir(t, rt1, "INSERT INTO canon_crash VALUES (42, 'x'), (43, 'y')")
		rt1.Close()

		rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
		if err != nil {
			t.Fatalf("re-open native-only cluster: %v", err)
		}
		defer rt2.Close()
		// The committed table must survive (catalog recovery rides the
		// native heap replay in native-only mode) and its rows must be
		// visible. (Full crash matrices land in slice S3a; S1 smoke.)
		if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "canon_crash"}); !ok {
			t.Fatal("canon_crash missing after native-only close/reopen — catalog heap replay broken")
		}
		rows := runSelectRealDataDir(t, rt2, "SELECT id FROM canon_crash")
		if len(rows) != 2 {
			t.Fatalf("native-only recovery: want 2 rows, got %d", len(rows))
		}
	})
}
