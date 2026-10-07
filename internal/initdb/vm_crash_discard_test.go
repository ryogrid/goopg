package initdb

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/storage"
)

// M0146-0063a: the visibility map is trusted only after a clean shutdown.
//
// goopg's map reaches disk only from the clean-shutdown SaveVM, and no WAL
// record clears its bits (PG redoes visibilitymap_clear from every heap record
// carrying *_ALL_VISIBLE_CLEARED). After a crash the _vm forks are those of
// the last clean shutdown. Measured before this fix: VACUUM, clean restart,
// a DELETE, kill -9, restart. `count(*) … WHERE a < 1000` by Index Only Scan
// then returned 500 where the heap held 200, because the stale fork still
// marked the deleted rows' pages all-visible. A crash start must discard the
// forks; a clean start keeps loading them.

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

func TestCleanStartLoadsVMForks(t *testing.T) {
	dir := freshDataDir(t)
	if got := readControl(t, dir).State; got != control.DBStateShutdowned {
		t.Fatalf("precondition: fresh initdb must be DB_SHUTDOWNED, got %d", got)
	}
	path, rel := plantVMFork(t, dir)

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if got := rt.VM.CountAllVisible(rel); got != 3 {
		t.Errorf("clean start: %d all-visible blocks loaded, want 3", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("clean start removed the _vm fork: %v", err)
	}
}

func TestCrashStartDiscardsVMForks(t *testing.T) {
	dir := freshDataDir(t)
	path, rel := plantVMFork(t, dir)
	// What a running server leaves in pg_control when it is killed.
	if err := control.UpdateControlFile(dir, func(cd *control.ControlFileData) {
		cd.State = control.DBStateInProduction
	}); err != nil {
		t.Fatal(err)
	}

	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rt.Close() }()

	if got := rt.VM.CountAllVisible(rel); got != 0 {
		t.Errorf("crash start: %d all-visible blocks trusted from a fork no WAL "+
			"record keeps current; an index-only scan would skip the heap check "+
			"for pages modified since the last clean shutdown", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("crash start left the stale _vm fork on disk (err=%v); a later clean "+
			"shutdown rewrites only relations with bits set again, so this fork "+
			"would be loaded on the following clean start", err)
	}
}
