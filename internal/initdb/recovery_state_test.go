package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/control"
)

// TestBeginRecoveryReadsPgControlState is the M0131-S20.1 guard on the
// decision itself.
//
// Before this slice goopg had NO reader of pg_control.State anywhere in
// internal/ or cmd/ — the constants existed and the field was decoded, and
// that was all. Startup replayed unconditionally and never distinguished a
// crashed cluster from a clean one, so nothing on disk ever said "recovery in
// progress" and a second crash midway through replay was invisible.
//
// The three cases below are upstream's InRecovery arms at
// postgres/src/backend/access/transam/xlogrecovery.c:919-936 minus the
// archive-recovery one (goopg's recovery.signal path stamps
// DB_IN_ARCHIVE_RECOVERY of its own).
func TestBeginRecoveryReadsPgControlState(t *testing.T) {
	t.Run("clean shutdown is not recovery", func(t *testing.T) {
		dir := freshDataDir(t)
		d, err := beginRecovery(dir)
		if err != nil {
			t.Fatalf("beginRecovery: %v", err)
		}
		if d.crashRecovery {
			t.Fatalf("freshly initdb'd cluster reported crash recovery "+
				"(state=%d redo=%d checkpoint=%d) — every clean start would "+
				"pay a forced checkpoint", d.prevState, d.redoLSN, readControl(t, dir).CheckPoint)
		}
		if got := readControl(t, dir).State; got != control.DBStateShutdowned {
			t.Fatalf("state after a no-op beginRecovery: got %d, want %d", got, control.DBStateShutdowned)
		}
	})

	// The arm that closes M0131-S17's other half: S17 made a killed cluster
	// LEAVE DB_IN_PRODUCTION behind; without this, nothing read it back.
	t.Run("DB_IN_PRODUCTION means the previous run was killed", func(t *testing.T) {
		dir := freshDataDir(t)
		setState(t, dir, control.DBStateInProduction)

		d, err := beginRecovery(dir)
		if err != nil {
			t.Fatalf("beginRecovery: %v", err)
		}
		if !d.crashRecovery {
			t.Fatal("a cluster left in DB_IN_PRODUCTION was treated as a clean " +
				"shutdown — this is exactly the state a SIGKILL leaves behind")
		}
		if d.prevState != control.DBStateInProduction {
			t.Errorf("prevState: got %d, want %d", d.prevState, control.DBStateInProduction)
		}
		if got := readControl(t, dir).State; got != control.DBStateInCrashRecovery {
			t.Fatalf("state stamped before replay: got %d, want %d "+
				"(DB_IN_CRASH_RECOVERY) — a crash during replay would again "+
				"look like whatever the previous run left", got, control.DBStateInCrashRecovery)
		}
	})

	// Upstream's DB_SHUTDOWNED_IN_RECOVERY subtlety: it is NOT clean. A
	// standby shut down tidily still has to replay back to consistency, and
	// upstream's test is literally `state != DB_SHUTDOWNED`.
	t.Run("shut down in recovery still needs recovery", func(t *testing.T) {
		dir := freshDataDir(t)
		setState(t, dir, control.DBStateShutdownedInRecovery)

		d, err := beginRecovery(dir)
		if err != nil {
			t.Fatalf("beginRecovery: %v", err)
		}
		if !d.crashRecovery {
			t.Fatal("DB_SHUTDOWNED_IN_RECOVERY treated as a clean shutdown; " +
				"upstream's test is state != DB_SHUTDOWNED, not a two-value set")
		}
	})

	// The second arm: an ONLINE checkpoint (redo strictly before the
	// checkpoint record) proves the cluster did not shut down through a
	// shutdown checkpoint, whatever the state byte claims.
	t.Run("an online last checkpoint forces recovery", func(t *testing.T) {
		dir := freshDataDir(t)
		if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
			cd.State = control.DBStateShutdowned
			cd.CheckPointCopyRedo = 4096
			cd.CheckPoint = 8192
		}); err != nil {
			t.Fatal(err)
		}
		d, err := beginRecovery(dir)
		if err != nil {
			t.Fatalf("beginRecovery: %v", err)
		}
		if !d.crashRecovery {
			t.Fatal("redo < checkpoint-record LSN with state DB_SHUTDOWNED was " +
				"accepted as clean; that pair can only come from an online " +
				"checkpoint, i.e. a cluster that never ran its shutdown one")
		}
		if d.redoLSN != 4096 {
			t.Errorf("redoLSN: got %d, want 4096 (the control file's checkPointCopy.redo)", d.redoLSN)
		}
	})

	// A hand-assembled directory has no pg_control at all (verifyInitialized
	// checks only PG_VERSION). It must start, not fail, and must fall back to
	// the stream scan by reporting redo 0.
	t.Run("absent pg_control degrades to the scan", func(t *testing.T) {
		d, err := beginRecovery(t.TempDir())
		if err != nil {
			t.Fatalf("beginRecovery on a directory with no pg_control: %v", err)
		}
		if d.crashRecovery || d.redoLSN != 0 {
			t.Fatalf("got %+v, want the zero decision (replay everything the scan finds)", d)
		}
	})
}

// TestOpenAfterCrashRunsEndOfRecoveryCheckpoint is the M0131-S20.1 guard on
// what Open does with the decision.
//
// Two things must happen that did not before: the directory must not still be
// describing the pre-crash checkpoint once Open returns (an end-of-recovery
// checkpoint has run, mirroring StartupXLOG's CHECKPOINT_END_OF_RECOVERY
// request), and the state must have travelled all the way back to
// DB_IN_PRODUCTION rather than being left mid-recovery.
//
// The clean-start half matters just as much: TestOpenStampsDBInProduction
// asserts LastCheckpointLSN() == 0 after Open, so if a normal start were
// misclassified as a crash this forced checkpoint would fire on every boot.
func TestOpenAfterCrashRunsEndOfRecoveryCheckpoint(t *testing.T) {
	dir := freshDataDir(t)

	// Simulate the SIGKILL: pg_control is left exactly as M0131-S17's stamp
	// leaves it while the server is live.
	setState(t, dir, control.DBStateInProduction)
	before := readControl(t, dir)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open after a simulated crash: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if rt.Checkpointer == nil {
		t.Fatal("no checkpointer on the runtime; the end-of-recovery checkpoint could not have run")
	}
	if rt.Checkpointer.LastCheckpointLSN() == 0 {
		t.Fatal("no end-of-recovery checkpoint ran: pg_control still points at " +
			"the pre-crash checkpoint, so a second crash replays the same span " +
			"again and the recovered state is undurable until the first " +
			"scheduled checkpoint (checkpoint_timeout away)")
	}

	after := readControl(t, dir)
	if after.CheckPoint <= before.CheckPoint {
		t.Errorf("pg_control checkpoint location did not advance: before=%d after=%d",
			before.CheckPoint, after.CheckPoint)
	}
	if after.State != control.DBStateInProduction {
		t.Errorf("state after recovery: got %d, want %d (DB_IN_PRODUCTION) — "+
			"the cluster must not be left advertising DB_IN_CRASH_RECOVERY",
			after.State, control.DBStateInProduction)
	}
}

func freshDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readControl(t *testing.T, dir string) *control.ControlFileData {
	t.Helper()
	cd, err := control.ReadControlFile(dir)
	if err != nil {
		t.Fatalf("ReadControlFile: %v", err)
	}
	if cd == nil {
		t.Fatal("ReadControlFile: no pg_control")
	}
	return cd
}

func setState(t *testing.T, dir string, state uint32) {
	t.Helper()
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.State = state
	}); err != nil {
		t.Fatal(err)
	}
}
