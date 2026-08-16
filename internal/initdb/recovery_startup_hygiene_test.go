package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/control"
)

// TestClearRelcacheInitFilesSweepsWholeCluster pins M0131-S20.3's sweep against
// RelationCacheInitFileRemove (relcache.c) — specifically the three places
// goopg's pre-existing reactive unlink (catalog.RelcacheInitFileUnlink, which
// takes ONE database OID) could never reach: a database directory the caller
// did not name, a non-default tablespace, and the shared file when no
// invalidation happened to fire.
//
// The non-numeric entries are the other half of the contract. Upstream guards
// every descent with `strspn(d_name, "0123456789") == strlen(d_name)`; without
// that test the sweep would try `base/pgsql_tmp/pg_internal.init` and, worse,
// would treat any future non-database directory as a database.
func TestClearRelcacheInitFilesSweepsWholeCluster(t *testing.T) {
	dir := t.TempDir()

	verDir := misc.TablespaceVersionDirectory
	swept := []string{
		filepath.Join("global", "pg_internal.init"),
		filepath.Join("base", "1", "pg_internal.init"),
		filepath.Join("base", "5", "pg_internal.init"),
		filepath.Join("base", "16384", "pg_internal.init"),
		filepath.Join("pg_tblspc", "16500", verDir, "16384", "pg_internal.init"),
	}
	// Must survive: not a database directory (non-numeric), not an init
	// file, and a tablespace entry whose name is not all digits.
	kept := []string{
		filepath.Join("base", "pgsql_tmp", "pg_internal.init"),
		filepath.Join("base", "5", "PG_VERSION"),
		filepath.Join("pg_tblspc", "notanoid", verDir, "16384", "pg_internal.init"),
	}

	for _, p := range append(append([]string{}, swept...), kept...) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	clearRelcacheInitFiles(dir)

	for _, p := range swept {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived the pre-replay sweep (err=%v); a stale relcache "+
				"init file describes the catalog as it was BEFORE replay", p, err)
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s was removed but must not be: only all-digit directory "+
				"entries are database OIDs (relcache.c strspn test): %v", p, err)
		}
	}
}

// TestClearRelcacheInitFilesToleratesMissingDirs pins the warn-and-continue
// posture: upstream calls unlink_initfile with elevel = LOG here, so a cluster
// directory that has no base/ or pg_tblspc/ at all (hand-assembled dirs are a
// supported shape — verifyInitialized checks only PG_VERSION) must not make
// Open fail. clearRelcacheInitFiles returns nothing, so the assertion is that
// it neither panics nor blocks.
func TestClearRelcacheInitFilesToleratesMissingDirs(t *testing.T) {
	clearRelcacheInitFiles(t.TempDir())
}

// TestOpenRemovesPreexistingRelcacheInitFile is the S20.3 guard that matters
// for goopg specifically: the init file on disk may have been written by real
// PostgreSQL against a catalog that WAL replay is about to move. It also pins
// the UNCONDITIONAL half — this is a CLEAN start (pg_control says
// DB_SHUTDOWNED), and upstream still sweeps ("it seems safest to just remove
// them always", xlog.c:5622-5632). Conditioning the sweep on
// decision.crashRecovery would pass every other test in the tree.
func TestOpenRemovesPreexistingRelcacheInitFile(t *testing.T) {
	dir := freshDataDir(t)

	if got := readControl(t, dir).State; got != control.DBStateShutdowned {
		t.Fatalf("precondition: a fresh initdb must be DB_SHUTDOWNED, got %d — "+
			"this test only proves the sweep is unconditional if the start is clean", got)
	}

	// WHICH database directory this plants in decides whether the test can
	// see the sweep at all. Open REGENERATES global/, base/1/ and base/5/
	// after replay (relcache_init.go:31-47, called from open.go:1690/1844),
	// so a foreign file in any of those is overwritten with or without the
	// sweep — asserting there produces a guard that passes when the sweep is
	// deleted outright (verified: both "no sweep" and "sweep only on crash
	// recovery" still passed such an assertion). base/16384 is a database
	// goopg does not regenerate for, which is exactly the gap S20.3 closes:
	// the pre-existing reactive path (catalog.RelcacheInitFileUnlink) takes
	// ONE database OID, so nothing ever reached a database the running
	// session had not itself invalidated.
	foreignDB := filepath.Join(dir, "base", "16384")
	if err := os.MkdirAll(foreignDB, 0o755); err != nil {
		t.Fatal(err)
	}
	initFile := filepath.Join(foreignDB, "pg_internal.init")
	if err := os.WriteFile(initFile, []byte("written by real PostgreSQL"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if _, err := os.Stat(initFile); !os.IsNotExist(err) {
		t.Fatalf("base/16384/pg_internal.init survived Open (err=%v): a PG-authored "+
			"relcache cache for a database this session never touched is now live in "+
			"a goopg cluster, describing the catalog as it was before replay", err)
	}
}

// TestOpenSeedsMultiXactFromPgControl pins M0131-S20.4. multixact.NewStoreAt
// has existed since M0118-0003 with no caller, so every restart rewound the
// allocator to FirstMultiXactId and began re-issuing MultiXactIds the previous
// run had already stamped into tuple xmax fields — those stamps are on disk and
// outlive the process, which is exactly why upstream seeds the counter from
// checkPointCopy.nextMulti.
//
// The assertion is on Runtime.NextMultiXact (Open's half of the wiring);
// cmd/goopg/main.go turns it into multixact.NewStoreAt. Splitting there is
// deliberate: the store is process-shared and constructed after Open.
func TestOpenSeedsMultiXactFromPgControl(t *testing.T) {
	dir := freshDataDir(t)

	const seeded = 4242
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.CheckPointCopyNextMulti = seeded
	}); err != nil {
		t.Fatal(err)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if rt.NextMultiXact != seeded {
		t.Fatalf("Runtime.NextMultiXact = %d, want %d — the restarted cluster "+
			"would hand out MultiXactIds the previous run already stamped into "+
			"tuple xmax fields", rt.NextMultiXact, seeded)
	}
}

// TestOpenWithoutControlFileLeavesMultiXactUnseeded is the other direction of
// S20.4: a hand-assembled directory with no readable pg_control must degrade to
// the pre-S20.4 behaviour (0, which multixact.NewStoreAt clamps up to
// FirstMultiXactId) rather than propagating garbage into the allocator.
func TestOpenWithoutControlFileLeavesMultiXactUnseeded(t *testing.T) {
	dir := freshDataDir(t)
	if err := os.Remove(filepath.Join(dir, "global", "pg_control")); err != nil {
		t.Fatal(err)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open without pg_control: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if rt.NextMultiXact != 0 {
		t.Fatalf("Runtime.NextMultiXact = %d with no control file, want 0", rt.NextMultiXact)
	}
}

// TestCrashRecoveryLeavesMinRecoveryPointInvalid writes down M0131-S20.5's
// policy as an executable assertion rather than a comment.
//
// Upstream's rule is one line and one comment in CreateCheckPoint
// (xlog.c:7295-7297): "crash recovery should always recover to the end of WAL",
// therefore minRecoveryPoint = InvalidXLogRecPtr and minRecoveryPointTLI = 0 —
// unconditionally, on every checkpoint a non-recovering cluster writes. The
// reader half agrees: InitWalRecovery only adopts the control file's
// minRecoveryPoint when InArchiveRecovery, and uses InvalidXLogRecPtr otherwise
// (xlog.c:5778-5794), because a stale location would make the startup process
// declare consistency early and complain about invalid page references.
//
// So goopg must NOT invent a minimum recovery point when it crash-recovers. A
// non-zero value here would be worse than useless: it is the field PG FATALs
// over at xlogrecovery.c:878-886 if its timeline disagrees, and it caps
// consistency at a point goopg made up.
//
// The one deliberate divergence is BASE_BACKUP: initdb.BackupControlImage
// sets minRecoveryPoint = 1 so a PG standby restoring the backup passes
// XLogRecPtrIsInvalid() in CheckRecoveryConsistency. Since M0131-S29 that value
// exists ONLY in the image shipped inside the tar — the live primary's control
// file is never rewritten (upstream's basebackup.c:352-360 sends the file
// through a plain sendFile), which is what keeps this test's invariant and the
// backup path from contradicting each other. Asserted by
// TestBaseBackupDoesNotMutateLiveControlFile (internal/server).
func TestCrashRecoveryLeavesMinRecoveryPointInvalid(t *testing.T) {
	dir := freshDataDir(t)

	// A crash leaves the S17 stamp behind, which is what makes beginRecovery
	// classify the next start as crash recovery.
	setState(t, dir, control.DBStateInProduction)
	// Poison both fields so a pass cannot be "nobody wrote them anyway": the
	// end-of-recovery checkpoint has to actively clear them.
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.MinRecoveryPoint = 0xDEAD
		cd.MinRecoveryPointTLI = 9
	}); err != nil {
		t.Fatal(err)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open after a simulated crash: %v", err)
	}
	defer func() { _ = rt.Close() }()

	after := readControl(t, dir)
	if after.MinRecoveryPoint != 0 || after.MinRecoveryPointTLI != 0 {
		t.Fatalf("minRecoveryPoint after crash recovery = %d/tli %d, want 0/0 — "+
			"crash recovery must replay to the end of WAL (xlog.c:7295-7297); an "+
			"invented minimum recovery point caps consistency at a made-up LSN and "+
			"FATALs PG if its timeline disagrees",
			after.MinRecoveryPoint, after.MinRecoveryPointTLI)
	}
}
