package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/control"
)

// TestOpenStampsDBInProduction is the M0131-S17 guard.
//
// The bug it locks out is silent, permanent data loss on a hosted PG.
// `initdb` writes DB_SHUTDOWNED into pg_control, and before S17 the ONLY
// runtime writer of that byte was the checkpointer — whose first tick is one
// full checkpoint_timeout (PG default 300 s) after start, with no leading
// immediate checkpoint (internal/wal/checkpointer.go:440). A cluster killed
// inside that window therefore advertised "cleanly shut down" over a WAL tail
// full of committed work. A real PG 18.3 opening that directory takes none of
// the three InRecovery arms at
// postgres/src/backend/access/transam/xlogrecovery.c:924-936, skips
// PerformWalRecovery() entirely (xlog.c:5886-5890), and resumes inserting WAL
// at the end of the stale checkpoint — over goopg's tail, with no PANIC and
// nothing alarming in the log.
//
// The test asserts the byte on disk at the instant a SIGKILL would be most
// damaging: after Open has returned (so clients could be admitted) but before
// any checkpoint has run. LastCheckpointLSN == 0 is asserted alongside so a
// pass cannot be a checkpoint in disguise — that is what distinguishes the
// stamp from the pre-existing checkpointer write.
//
// Design: docs/design/0131-0014-pgcontrol-runtime-state-and-durability.md §S17.
func TestOpenStampsDBInProduction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Precondition: a freshly initdb'd directory claims a clean shutdown.
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile after Init: %v", err)
	}
	if cd == nil {
		t.Fatal("ReadControlFile after Init: no pg_control")
	}
	if cd.State != control.DBStateShutdowned {
		t.Fatalf("state after Init: got %d, want %d (DB_SHUTDOWNED)",
			cd.State, control.DBStateShutdowned)
	}

	// BgwriterDelay/WalWriterDelay left at zero so no background worker can
	// race the assertion; the checkpointer is not started by Open at all.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = rt.Close()
		}
	}()

	if rt.Checkpointer != nil && rt.Checkpointer.LastCheckpointLSN() != 0 {
		t.Fatalf("a checkpoint already ran (LastCheckpointLSN=%d) — the "+
			"assertion below would not prove the startup stamp",
			rt.Checkpointer.LastCheckpointLSN())
	}

	cd, err = control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile after Open: %v", err)
	}
	if cd.State != control.DBStateInProduction {
		t.Fatalf("state after Open: got %d, want %d (DB_IN_PRODUCTION) — a "+
			"SIGKILL here would leave the directory claiming a clean shutdown",
			cd.State, control.DBStateInProduction)
	}
	if cd.Time == 0 {
		t.Error("pg_control time not stamped alongside the state")
	}

	// The inverse direction: a clean Close still lands on DB_SHUTDOWNED, so
	// the stamp did not break the clean-handover path that
	// TestE2E_PGColdStartOnGoopgDataDir depends on.
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true

	cd, err = control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile after Close: %v", err)
	}
	if cd.State != control.DBStateShutdowned {
		t.Fatalf("state after clean Close: got %d, want %d (DB_SHUTDOWNED)",
			cd.State, control.DBStateShutdowned)
	}
}
