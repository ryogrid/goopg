package xlog

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestXactRelFileLocatorsRoundTrip: a commit (or abort) record listing
// relations carries XACT_XINFO_HAS_RELFILELOCATORS with PG's locators
// (pg_default 1663 for goopg's TblOid 0, pg_global 1664 for a shared
// relation, a user tablespace as is), and replaying it removes every fork of
// each relation, as xact_redo_commit / xact_redo_abort's DropRelationFiles do.
func TestXactRelFileLocatorsRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	w, err := NewWriter(Config{WALDir: filepath.Join(dataDir, "pg_wal"), PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	userRel := storage.RelFileNode{DBOid: 5, RelOid: 16400}
	sharedRel := storage.RelFileNode{DBOid: 0, RelOid: 16401}
	spcRel := storage.RelFileNode{TblOid: 16500, DBOid: 5, RelOid: 16402}
	abortRel := storage.RelFileNode{DBOid: 5, RelOid: 16403}
	commit, err := EncodeXactCommitPGWithRels(700, []storage.TransactionID{701}, []storage.RelFileNode{userRel, sharedRel, spcRel}, true)
	if err != nil {
		t.Fatal(err)
	}
	abort, err := EncodeXactAbortPGWithRels(702, nil, []storage.RelFileNode{abortRel})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]byte{commit, abort} {
		if _, _, err := w.Append(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadAll(filepath.Join(dataDir, "pg_wal"), DefaultSegmentSize)
	if err != nil || len(recs) != 2 {
		t.Fatalf("ReadAll: %d records, %v", len(recs), err)
	}
	c, err := ParseXactRecord(recs[0].XLog.Header.Info, recs[0].XLog.MainData)
	if err != nil {
		t.Fatal(err)
	}
	if want := []XactRelFileLocator{{1663, 5, 16400}, {1664, 0, 16401}, {16500, 5, 16402}}; !reflect.DeepEqual(c.DroppedRels, want) {
		t.Errorf("commit locators %v, want %v", c.DroppedRels, want)
	}
	if got := fmt.Sprint(c.Subxacts); got != "[701]" {
		t.Errorf("commit subxacts %s, want [701]", got)
	}
	if !xactCommitCarriesInvals(recs[0].XLog.Header.Info, recs[0].XLog.MainData) {
		t.Error("commit lost its invals chunk behind the relfilelocators")
	}
	a, err := ParseXactRecord(recs[1].XLog.Header.Info, recs[1].XLog.MainData)
	if err != nil {
		t.Fatal(err)
	}
	if want := []XactRelFileLocator{{1663, 5, 16403}}; !reflect.DeepEqual(a.DroppedRels, want) {
		t.Errorf("abort locators %v, want %v", a.DroppedRels, want)
	}

	// Replay removes every fork of the listed relations.
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	page := make([]byte, storage.BlockSize)
	var forks []storage.RelFileNode
	for _, r := range []storage.RelFileNode{userRel, sharedRel, abortRel} {
		for _, f := range []storage.ForkNumber{storage.MainFork, storage.VisibilityMapFork} {
			r.Fork = f
			if _, err := mgr.Extend(r, page); err != nil {
				t.Fatal(err)
			}
			forks = append(forks, r)
		}
	}
	keep := storage.RelFileNode{DBOid: 5, RelOid: 16499}
	if _, err := mgr.Extend(keep, page); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if _, err := ApplyRecord(mgr, r); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	for _, r := range forks {
		if mgr.Exists(r) {
			t.Errorf("%v still exists after replay", r)
		}
	}
	if !mgr.Exists(keep) {
		t.Error("an unlisted relation was removed")
	}
}
