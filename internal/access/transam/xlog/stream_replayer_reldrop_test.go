package xlog

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestDropReplayedRelBuffers pins the standby's DropRelationFiles buffer drop
// (M0149-0032c): for a replayed commit that dropped a relation, the rel-drop
// hook runs for every fork of it, with pg_default mapped to goopg's TblOid 0,
// so no buffer of the removed relfilenode is written back later. A record of
// another rmgr runs nothing.
func TestDropReplayedRelBuffers(t *testing.T) {
	payload, err := EncodeXactCommitPGWithRels(700, nil, []storage.RelFileNode{{DBOid: 5, RelOid: 16384}}, false)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	rec := Record{XLog: &XLogDecodedRecord{Header: XLogRecord{Rmid: RmgrXact, Info: info}, MainData: mainData}}
	sr := NewStreamReplayer(&storage.Manager{}, 0)
	var got []storage.RelFileNode
	sr.SetRelDropReplayHook(func(rel storage.RelFileNode) { got = append(got, rel) })
	sr.dropReplayedRelBuffers(rec)
	var want []storage.RelFileNode
	for fork := storage.ForkNumber(0); fork <= storage.InitFork; fork++ {
		want = append(want, storage.RelFileNode{DBOid: 5, RelOid: 16384, Fork: fork})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hook saw %v, want %v", got, want)
	}
	got = nil
	rec.XLog.Header.Rmid = RmgrHeap
	sr.dropReplayedRelBuffers(rec)
	if len(got) != 0 {
		t.Fatalf("non-xact record ran the hook: %v", got)
	}
}
