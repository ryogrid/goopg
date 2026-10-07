package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/storage"
)

// M0146-0063a / M0146-0063: when the visibility-map forks may be trusted.
//
// Before M0146-0063 goopg's map reached disk only from the clean-shutdown
// SaveVM, and no WAL record cleared its bits. After a crash the _vm forks were
// those of the last clean shutdown. Measured: VACUUM, clean restart, a DELETE,
// kill -9, restart; `count(*) … WHERE a < 1000` by Index Only Scan returned 500
// where the heap held 200, because the stale fork still marked the deleted
// rows' pages all-visible. M0146-0063a made a crash start discard the forks.
//
// M0146-0063 WAL-logs every map change and saves the forks at each checkpoint.
// A cluster carrying storage.VMWALLoggedFeature therefore loads its forks after
// a crash, once replay has brought them up to date. One without it (last run by
// an older binary) still discards them, and Open records the capability for the
// runs that follow.

// plantVMFork writes an all-visible _vm fork for a relation in database 5 and
// returns its path and relfilenode.
func plantVMFork(t *testing.T, dir string) (string, storage.RelFileNode) {
	t.Helper()
	rel := storage.RelFileNode{DBOid: 5, RelOid: 99901}
	path := storage.RelForkPath(dir, storage.RelFileNode{DBOid: rel.DBOid, RelOid: rel.RelOid, Fork: storage.VisibilityMapFork})
	masks := []uint8{storage.VMAllVisible, storage.VMAllVisible, storage.VMAllVisible}
	if err := storage.WriteVMFork(path, masks); err != nil {
		t.Fatalf("WriteVMFork: %v", err)
	}
	return path, rel
}

// markCrashed leaves pg_control as a killed server leaves it.
func markCrashed(t *testing.T, dir string) {
	t.Helper()
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.State = control.DBStateInProduction
	}); err != nil {
		t.Fatal(err)
	}
}

// dropVMCapability rewrites the features file without VMWALLoggedFeature: a
// cluster last run by a binary that did not WAL-log the map.
func dropVMCapability(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, goopgFeaturesFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line != storage.VMWALLoggedFeature {
			keep = append(keep, line)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCleanStartLoadsVMForks(t *testing.T) {
	dir := freshDataDir(t)
	if got := readControl(t, dir).State; got != control.DBStateShutdowned {
		t.Fatalf("precondition: fresh initdb must be DB_SHUTDOWNED, got %d", got)
	}
	dropVMCapability(t, dir) // a clean start loads with or without it
	path, rel := plantVMFork(t, dir)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := rt.VM.CountAllVisible(rel); got != 3 {
		t.Errorf("clean start: %d all-visible blocks loaded, want 3", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("clean start removed the _vm fork: %v", err)
	}
	// The capability is recorded by the first checkpoint that saves the
	// forks with logging on — Close's shutdown checkpoint here — never by
	// Open itself: until then the forks on disk are the old binary's.
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !readGoopgFeatures(dir)[storage.VMWALLoggedFeature] {
		t.Errorf("the shutdown checkpoint did not record %s; the next crash start would discard a logged map", storage.VMWALLoggedFeature)
	}
}

func TestCrashStartWithoutCapabilityDiscardsVMForks(t *testing.T) {
	dir := freshDataDir(t)
	dropVMCapability(t, dir)
	path, rel := plantVMFork(t, dir)
	markCrashed(t, dir)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if got := rt.VM.CountAllVisible(rel); got != 0 {
		t.Errorf("crash start: %d all-visible blocks trusted from a fork no WAL "+
			"record kept current; an index-only scan would skip the heap check "+
			"for pages modified since the last clean shutdown", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("crash start left the stale _vm fork on disk (err=%v); a later clean "+
			"shutdown rewrites only relations with bits set again, so this fork "+
			"would be loaded on the following clean start", err)
	}
}

func TestCrashStartWithCapabilityLoadsVMForks(t *testing.T) {
	dir := freshDataDir(t)
	if !readGoopgFeatures(dir)[storage.VMWALLoggedFeature] {
		t.Fatalf("precondition: initdb must write %s", storage.VMWALLoggedFeature)
	}
	path, rel := plantVMFork(t, dir)
	markCrashed(t, dir)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if got := rt.VM.CountAllVisible(rel); got != 3 {
		t.Errorf("crash start of a WAL-logged map: %d all-visible blocks loaded, want 3", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("crash start of a WAL-logged map removed its fork: %v", err)
	}
}
