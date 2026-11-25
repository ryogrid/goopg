package xlog

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// TestXactCommitDroppedStatsItems pins the XACT_XINFO_HAS_DROPPED_STATS chunk
// goopg's commit and abort records carry (M0149-0032b): the xinfo bit is set,
// the xl_xact_stats_items chunk sits after the relfilelocators and before
// the invals (xact.c XactLogCommitRecord), each item is {kind, dboid,
// objid_lo, objid_hi}, and ParseXactRecord reads the items back. A PG 18.3
// standby of a goopg primary drops the stats these items name.
func TestXactCommitDroppedStatsItems(t *testing.T) {
	stats := []transam.XactStatsItem{
		{Kind: transam.PgStatKindRelation, DBOid: 5, ObjID: 16384},
		{Kind: transam.PgStatKindFunction, DBOid: 5, ObjID: 1<<32 | 16390},
		{Kind: transam.PgStatKindDatabase, DBOid: 16400},
	}
	rels := []storage.RelFileNode{{DBOid: 5, RelOid: 16384}}
	payload, err := EncodeXactCommitPGWithStatsAt(0, 900, []storage.TransactionID{901}, rels, stats, true)
	if err != nil {
		t.Fatal(err)
	}
	info, mainData := decodeMainData(t, payload)
	parsed, err := ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord: %v", err)
	}
	if parsed.Xinfo&xactXinfoHasDroppedStats == 0 {
		t.Fatalf("xinfo %#x lacks HAS_DROPPED_STATS", parsed.Xinfo)
	}
	if !reflect.DeepEqual(parsed.DroppedStats, stats) {
		t.Errorf("DroppedStats = %+v, want %+v", parsed.DroppedStats, stats)
	}
	if len(parsed.DroppedRels) != 1 || len(parsed.Subxacts) != 1 {
		t.Errorf("rels %v subxacts %v, want one each", parsed.DroppedRels, parsed.Subxacts)
	}
	// xact_time 8, xinfo 4, subxacts 4+4, rels 4+12, stats 4+3*16, invals 4.
	if want := 8 + 4 + 8 + 16 + 4 + 48 + 4; len(mainData) != want {
		t.Errorf("main data %d bytes, want %d", len(mainData), want)
	}
	if n := binary.LittleEndian.Uint32(mainData[len(mainData)-4:]); n != 0 {
		t.Errorf("trailing invals nmsgs = %d, want 0 (invals chunk last)", n)
	}
	item := mainData[8+4+8+16+4+16:]
	if k, lo, hi := binary.LittleEndian.Uint32(item[0:4]), binary.LittleEndian.Uint32(item[8:12]), binary.LittleEndian.Uint32(item[12:16]); k != 3 || lo != 16390 || hi != 1 {
		t.Errorf("second item kind %d objid_lo %d objid_hi %d, want 3 16390 1", k, lo, hi)
	}

	// The abort record carries created objects' items the same way.
	payload, err = EncodeXactAbortPGWithStatsAt(0, 910, nil, nil, stats[:1])
	if err != nil {
		t.Fatal(err)
	}
	info, mainData = decodeMainData(t, payload)
	parsed, err = ParseXactRecord(info, mainData)
	if err != nil {
		t.Fatalf("ParseXactRecord(abort): %v", err)
	}
	if !reflect.DeepEqual(parsed.DroppedStats, stats[:1]) {
		t.Errorf("abort DroppedStats = %+v, want %+v", parsed.DroppedStats, stats[:1])
	}

	// No items: no bit, no chunk (unchanged record shape).
	payload, err = EncodeXactCommitPGWithStatsAt(0, 920, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if info, mainData = decodeMainData(t, payload); info&xlogXactHasInfo != 0 || len(mainData) != 8 {
		t.Errorf("empty commit: info %#x, %d bytes; want no xinfo and 8 bytes", info, len(mainData))
	}
}
