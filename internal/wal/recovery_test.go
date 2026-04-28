package wal

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestReplayRecordsAppliesPageImages(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 900, Fork: storage.MainFork}

	p0 := mustPageWithByte(t, 0x22)
	p1 := mustPageWithByte(t, 0x33)

	rec0, err := EncodePageImage(rel, 0, p0)
	if err != nil {
		t.Fatal(err)
	}
	rec1, err := EncodePageImage(rel, 1, p1)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: rec0},
		{StartLSN: 101, EndLSN: 200, Payload: rec1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 2 || stats.Applied != 2 || stats.CheckpointLSN != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	got0 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got0); err != nil {
		t.Fatal(err)
	}
	if got0[100] != 0x22 {
		t.Fatalf("block0 byte = %#x, want 0x22", got0[100])
	}

	got1 := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, got1); err != nil {
		t.Fatal(err)
	}
	if got1[100] != 0x33 {
		t.Fatalf("block1 byte = %#x, want 0x33", got1[100])
	}
}

func TestReplayRecordsStopsAtLastCheckpoint(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 901, Fork: storage.MainFork}

	before := mustPageWithByte(t, 0x11)
	after := mustPageWithByte(t, 0x44)

	beforePayload, err := EncodePageImage(rel, 0, before)
	if err != nil {
		t.Fatal(err)
	}
	afterPayload, err := EncodePageImage(rel, 0, after)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := ReplayRecords(mgr, []Record{
		{StartLSN: 1, EndLSN: 100, Payload: beforePayload},
		{StartLSN: 101, EndLSN: 110, Payload: EncodeCheckpoint()},
		{StartLSN: 111, EndLSN: 210, Payload: afterPayload},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Applied != 1 {
		t.Fatalf("applied = %d, want 1", stats.Applied)
	}
	if stats.CheckpointLSN != 110 {
		t.Fatalf("checkpoint lsn = %d, want 110", stats.CheckpointLSN)
	}

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[100] != 0x11 {
		t.Fatalf("block0 byte = %#x, want 0x11", got[100])
	}
}

func TestReplayFromDirEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")

	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 256})
	if err != nil {
		t.Fatal(err)
	}

	rel := storage.RelFileNode{DBOid: 1, RelOid: 902, Fork: storage.MainFork}
	pBefore := mustPageWithByte(t, 0x55)
	pAfter := mustPageWithByte(t, 0x77)

	beforePayload, err := EncodePageImage(rel, 0, pBefore)
	if err != nil {
		t.Fatal(err)
	}
	afterPayload, err := EncodePageImage(rel, 0, pAfter)
	if err != nil {
		t.Fatal(err)
	}

	_, end1, err := w.Append(beforePayload)
	if err != nil {
		t.Fatal(err)
	}
	_, end2, err := w.Append(EncodeCheckpoint())
	if err != nil {
		t.Fatal(err)
	}
	_, end3, err := w.Append(afterPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end3); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if end2 <= end1 {
		t.Fatalf("checkpoint end lsn ordering invalid: end1=%d end2=%d", end1, end2)
	}

	stats, err := ReplayFromDir(dataDir, 256)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 3 || stats.Applied != 1 || stats.CheckpointLSN != end2 {
		t.Fatalf("unexpected stats: %+v (want records=3 applied=1 checkpoint=%d)", stats, end2)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, got); err != nil {
		t.Fatal(err)
	}
	if got[100] != 0x55 {
		t.Fatalf("replayed byte = %#x, want 0x55", got[100])
	}
}

func mustPageWithByte(t *testing.T, v byte) storage.Page {
	t.Helper()
	p := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(p); err != nil {
		t.Fatal(err)
	}
	p[100] = v
	return p
}
