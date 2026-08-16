package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// Guards for M0130-S11.3 — block 0 is now upstream's metapage: a PG-format
// page (16-byte BTPageOpaqueData special area, BTP_META set) carrying the
// 48-byte BTMetaPageData at PageGetContents, with pd_lower advanced past it.
// Before the flip block 0 was a zero-special-area page with a private 24-byte
// payload, which is the page a real PG's _bt_checkpage rejected first
// ("contains corrupted page at block 0").

// readMetaPageBytes returns a private copy of block 0's bytes.
func readMetaPageBytes(t *testing.T, pool *storage.Pool, rel storage.RelFileNode) storage.Page {
	t.Helper()
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: MetaBlock})
	if err != nil {
		t.Fatalf("pin metapage: %v", err)
	}
	slot.RLock()
	p := make(storage.Page, storage.BlockSize)
	copy(p, slot.Page())
	slot.RUnlock()
	pool.Unpin(slot)
	return p
}

// assertUpstreamMetaShape is the shared shape assertion: the page must pass the
// oracle's own _bt_checkpage, carry BTP_META, and reserve pd_lower for the full
// (padding included) BTMetaPageData.
func assertUpstreamMetaShape(t *testing.T, p storage.Page, wantRoot storage.BlockNumber, wantLevel uint32) {
	t.Helper()
	if err := CheckPGBTPage(p, MetaBlock); err != nil {
		t.Fatalf("metapage fails _bt_checkpage: %v", err)
	}
	if op := ReadPGOpaque(p); !op.IsMeta() {
		t.Errorf("metapage opaque flags = %#x, want BTP_META (%#x) set", op.Flags, BTPMeta)
	}
	if lower := storage.MustHeader(p).Lower(); int(lower) != storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG {
		t.Errorf("pd_lower = %d, want %d", lower, storage.SizeOfPageHeaderData+SizeOfBTMetaPageDataPG)
	}
	m := ReadPGMetaPage(p)
	if m.Magic != BTreeMagicPG || m.Version != BTreeVersionPG {
		t.Errorf("magic/version = %#x/%d, want %#x/%d", m.Magic, m.Version, BTreeMagicPG, BTreeVersionPG)
	}
	if m.Root != wantRoot || m.FastRoot != wantRoot {
		t.Errorf("root/fastroot = %d/%d, want %d", m.Root, m.FastRoot, wantRoot)
	}
	if m.Level != wantLevel || m.FastLevel != wantLevel {
		t.Errorf("level/fastlevel = %d/%d, want %d", m.Level, m.FastLevel, wantLevel)
	}
	// -1.0 is upstream's "cleanup stats unknown" sentinel; 0 would tell a real
	// PG's _bt_vacuum_needs_cleanup that the index holds no tuples.
	if m.LastCleanupNumHeapTuples != -1.0 {
		t.Errorf("btm_last_cleanup_num_heap_tuples = %v, want -1", m.LastCleanupNumHeapTuples)
	}
	if !m.AllEqualImage {
		t.Error("btm_allequalimage = false; goopg writes posting lists unconditionally, so amcheck would reject the index")
	}
}

func TestCreateWritesUpstreamMetapage(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()
	assertUpstreamMetaShape(t, readMetaPageBytes(t, pool, bt.rel), rootStart, 0)
}

func TestBulkCreateWritesUpstreamMetapage(t *testing.T) {
	// Enough entries to build at least one internal level, so the metapage is
	// written by the "final root" limb with a non-zero level rather than the
	// empty-index limb.
	entries := make([]BulkEntry, 4000)
	for i := range entries {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1},
		}
	}
	for _, tc := range []struct {
		name    string
		build   func(*storage.Pool, storage.RelFileNode, []BulkEntry) (*BTree, error)
		entries []BulkEntry
	}{
		{"dedup", BulkCreate, entries},
		{"noDedup", BulkCreateNoDedup, entries},
		{"empty", BulkCreate, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
			pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
			if err != nil {
				t.Fatalf("NewPool: %v", err)
			}
			defer func() { _ = pool.Close(); _ = mgr.Close() }()
			rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
			bt, err := tc.build(pool, rel, tc.entries)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			meta, err := bt.readMeta()
			if err != nil {
				t.Fatalf("readMeta: %v", err)
			}
			assertUpstreamMetaShape(t, readMetaPageBytes(t, pool, rel), meta.Root, meta.Level)
			if tc.entries != nil && meta.Level == 0 {
				t.Fatalf("expected a multi-level tree, got level 0 (test no longer covers the final-root limb)")
			}
		})
	}
}

// TestRootMetaUpdatePreservesCleanupFields is the read-modify-write guard: the
// root-pointer writers (updateRootMeta, the newroot WAL limb, ReplayMetaSetRoot)
// must not clobber the fields owned by VACUUM and the build. Re-initialising the
// page instead would silently reset btm_last_cleanup_num_delpages to 0 and
// allequalimage to whatever the writer happened to assume.
func TestRootMetaUpdatePreservesCleanupFields(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()

	// Stamp cleanup stats a VACUUM would have written.
	slot, err := pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	slot.Lock()
	m := ReadPGMetaPage(slot.Page())
	m.LastCleanupNumDelpages = 7
	m.LastCleanupNumHeapTuples = 12345
	WritePGMetaPage(slot.Page(), m)
	pool.MarkDirty(slot)
	slot.Unlock()
	pool.Unpin(slot)

	if err := bt.updateRootMeta(42, 3); err != nil {
		t.Fatalf("updateRootMeta: %v", err)
	}
	got, err := bt.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if got.Root != 42 || got.Level != 3 || got.FastRoot != 42 || got.FastLevel != 3 {
		t.Errorf("root/level = %d/%d (fast %d/%d), want 42/3", got.Root, got.Level, got.FastRoot, got.FastLevel)
	}
	if got.LastCleanupNumDelpages != 7 || got.LastCleanupNumHeapTuples != 12345 || !got.AllEqualImage {
		t.Errorf("cleanup fields clobbered: delpages=%d heaptuples=%v allequalimage=%v",
			got.LastCleanupNumDelpages, got.LastCleanupNumHeapTuples, got.AllEqualImage)
	}

	// The WAL-replay limb takes the same read-modify-write path.
	p := readMetaPageBytes(t, pool, bt.rel)
	if err := ReplayMetaSetRoot(p, 99, 1); err != nil {
		t.Fatalf("ReplayMetaSetRoot: %v", err)
	}
	if replayed := ReadPGMetaPage(p); replayed.Root != 99 || replayed.LastCleanupNumDelpages != 7 ||
		replayed.LastCleanupNumHeapTuples != 12345 {
		t.Errorf("after ReplayMetaSetRoot: %+v", replayed)
	}
	// Shape (not the freshly-initialised field values, which this test
	// deliberately overwrote) must survive replay too.
	if err := CheckPGBTPage(p, MetaBlock); err != nil {
		t.Fatalf("metapage fails _bt_checkpage after replay: %v", err)
	}
	if op := ReadPGOpaque(p); !op.IsMeta() {
		t.Errorf("after replay: opaque flags = %#x, want BTP_META set", op.Flags)
	}
}

// TestOpenRejectsLegacyMetapage pins the format break: a pre-S11.3 metapage
// (zero-length special area, 24-byte private payload) shares the magic/version
// offsets with BTMetaPageData, so without the _bt_checkpage gate in readMeta it
// would open "successfully" and then decode garbage. REINDEX is the only
// upgrade path.
func TestOpenRejectsLegacyMetapage(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()

	slot, err := pool.Pin(storage.BufferTag{Rel: bt.rel, Block: MetaBlock})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	slot.Lock()
	p := slot.Page()
	if err := storage.InitPage(p); err != nil { // zero-length special area
		t.Fatalf("InitPage: %v", err)
	}
	WritePGMetaPage(p, PGBTMetaPage{Magic: BTreeMagicPG, Version: BTreeVersionPG, Root: 1, FastRoot: 1})
	pool.MarkDirty(slot)
	slot.Unlock()
	pool.Unpin(slot)

	if _, err := Open(pool, bt.rel); err == nil {
		t.Fatal("Open accepted a legacy-shaped metapage; the format break is unguarded")
	}
}
