package nbtree

// Guards for M0130-S11.5b-2 — the split record's INCREMENTAL left half.
//
// Two kinds of coverage, deliberately separated:
//
//   - unit tests over hand-built pages, which pin the three offsets and the
//     block-data framing exactly (a real tree's items are whatever the sort
//     order produced, so they cannot pin "the new item spliced at index 1");
//   - a REAL tree driven through hundreds of splits, which answers the question
//     the unit tests cannot: is a split goopg actually performs describable by
//     upstream's record at all? That is the whole premise of the slice, and it
//     is a property of `splitPage`, not of these functions.

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// splitLeftTestPages builds the trio the encoder reconciles: the page as it
// stood before the split, and the two halves as `splitPage` writes them. The
// caller says where the new item landed among the pre-split items (index into
// the merged list) and how many merged items stay on the left.
func splitLeftTestPages(t *testing.T, pre [][]byte, newItem []byte, spliceAt, leftCount int, leftBlk, rightBlk, sibBlk storage.BlockNumber) (prePage, leftPage, rightPage storage.Page) {
	t.Helper()
	merged := make([][]byte, 0, len(pre)+1)
	merged = append(merged, pre[:spliceAt]...)
	merged = append(merged, newItem)
	merged = append(merged, pre[spliceAt:]...)

	inheritedHK := PGBTPivotRaw([]byte("inherited-hikey"), PNone)
	newHK := PGBTPivotRaw([]byte("new-separator"), PNone)

	build := func(op BTPageOpaque, hk []byte, items [][]byte) storage.Page {
		p := make(storage.Page, storage.BlockSize)
		if err := InitPGBTPage(p); err != nil {
			t.Fatal(err)
		}
		writeOpaque(p, op)
		if hk != nil {
			if err := pgSetHighKeyRaw(p, hk); err != nil {
				t.Fatal(err)
			}
		}
		for _, raw := range items {
			if _, err := storage.PageAddItemRaw(p, raw); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	prePage = build(BTPageOpaque{Prev: PNone, Next: sibBlk, Flags: BTLeaf}, inheritedHK, pre)
	leftPage = build(BTPageOpaque{Prev: PNone, Next: rightBlk, Flags: BTLeaf | BTIncompleteSplit}, newHK, merged[:leftCount])
	rightPage = build(SplitRightPageOpaque(0, leftBlk, sibBlk), inheritedHK, merged[leftCount:])
	return prePage, leftPage, rightPage
}

func splitLeftTestItems(n int) [][]byte {
	items := make([][]byte, n)
	for i := range items {
		items[i] = PGBTPivotRaw([]byte{'p', byte('0' + i)}, storage.BlockNumber(50+i))
	}
	return items
}

// TestDescribeSplitLeftOffsets pins the two offsets against upstream's
// coordinate system: they are PHYSICAL offset numbers on the PRE-SPLIT page, so
// they start at that page's P_FIRSTDATAKEY (2 here — it is non-rightmost and
// carries a high key), and `firstrightoff` counts only PRE-SPLIT items, never
// the new one.
func TestDescribeSplitLeftOffsets(t *testing.T) {
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(1), storage.BlockNumber(9), storage.BlockNumber(2)
	newItem := PGBTPivotRaw([]byte("new"), 77)

	for _, tc := range []struct {
		name                  string
		spliceAt, leftCount   int
		wantFirstRight, wantN uint16
		wantOnLeft            bool
	}{
		// Pre-split items p0..p3 at offsets 2,3,4,5.
		{"new item mid-left", 1, 3, 4, 3, true},
		// The new item is the LAST item on the left half: upstream's
		// "cope with possibility that newitem goes at the end" arm, where
		// newitemoff == firstrightoff.
		{"new item at the left edge", 2, 3, 4, 4, true},
		// SPLIT_R: the new item opens the right half, so no pre-split item is
		// displaced on the left and the record carries no new item at all.
		{"new item on the right", 2, 2, 4, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pre, left, right := splitLeftTestPages(t, splitLeftTestItems(4), newItem, tc.spliceAt, tc.leftCount, leftBlk, rightBlk, sibBlk)
			d, err := DescribeSplitLeft(pre, left, right, newItem)
			if err != nil {
				t.Fatalf("DescribeSplitLeft: %v", err)
			}
			if d.NewItemOnLeft != tc.wantOnLeft {
				t.Errorf("NewItemOnLeft = %v, want %v", d.NewItemOnLeft, tc.wantOnLeft)
			}
			if d.FirstRightOff != tc.wantFirstRight {
				t.Errorf("FirstRightOff = %d, want %d", d.FirstRightOff, tc.wantFirstRight)
			}
			if d.NewItemOff != tc.wantN {
				t.Errorf("NewItemOff = %d, want %d", d.NewItemOff, tc.wantN)
			}
			if got := d.NewItem != nil; got != tc.wantOnLeft {
				t.Errorf("carried new item = %v, want %v (upstream registers it only under newitemonleft)", got, tc.wantOnLeft)
			}
			if err := CheckSplitLeft(pre, left, 0, rightBlk, d); err != nil {
				t.Errorf("CheckSplitLeft: %v", err)
			}
			// Round trip through the on-record framing.
			ni, hk, err := ParseSplitLeftBlockData(SplitLeftBlockData(d), d.NewItemOnLeft)
			if err != nil {
				t.Fatalf("ParseSplitLeftBlockData: %v", err)
			}
			if !bytes.Equal(ni, d.NewItem) || !bytes.Equal(hk, d.HighKey) {
				t.Error("block data did not round-trip")
			}
		})
	}
}

// TestDescribeSplitLeftRefusesUndescribableSplits is the mutation guard on the
// premise: each of these is a rewrite goopg's split path can really produce and
// upstream's three offsets cannot express, so each must be REPORTED (the
// encoder then logs a full-page image) rather than mis-described.
func TestDescribeSplitLeftRefusesUndescribableSplits(t *testing.T) {
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(1), storage.BlockNumber(9), storage.BlockNumber(2)
	newItem := PGBTPivotRaw([]byte("new"), 77)
	items := splitLeftTestItems(4)

	t.Run("an item the rewrite dropped", func(t *testing.T) {
		// goopg's rewrite reads the page with pageItems, which SKIPS
		// LP_DEAD-marked items; upstream's redo copies them like any other.
		pre, _, _ := splitLeftTestPages(t, items, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		// The halves are rebuilt from one FEWER pre-split item while the
		// pre-split page still holds all four.
		_, left, right := splitLeftTestPages(t, items[1:], newItem, 0, 2, leftBlk, rightBlk, sibBlk)
		if _, err := DescribeSplitLeft(pre, left, right, newItem); err == nil {
			t.Error("a split that dropped a pre-split item was accepted")
		}
	})

	t.Run("an item the dedup pass rewrote", func(t *testing.T) {
		pre, left, right := splitLeftTestPages(t, items, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		merged := append([][]byte{items[0], newItem}, PGBTPivotRaw([]byte("merged"), 99))
		_, left, _ = splitLeftTestPages(t, merged, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		if _, err := DescribeSplitLeft(pre, left, right, newItem); err == nil {
			t.Error("a left half holding an item that was never on the pre-split page was accepted")
		}
	})

	t.Run("a root split", func(t *testing.T) {
		// Upstream's _bt_split clears BTP_ROOT on the left half; goopg clears it
		// in a later step, so at this LSN the two engines disagree and only the
		// image is faithful. Describe cannot see it (it compares items) — Check
		// must.
		pre, left, right := splitLeftTestPages(t, items, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		op := readOpaque(pre)
		op.Flags |= BTRoot
		writeOpaque(pre, op)
		op = readOpaque(left)
		op.Flags |= BTRoot
		writeOpaque(left, op)
		d, err := DescribeSplitLeft(pre, left, right, newItem)
		if err != nil {
			t.Fatalf("DescribeSplitLeft: %v", err)
		}
		if err := CheckSplitLeft(pre, left, 0, rightBlk, d); err == nil {
			t.Error("a root split reproduced the primary's page — upstream's redo drops BTP_ROOT")
		}
	})

	t.Run("no pre-split page", func(t *testing.T) {
		_, left, right := splitLeftTestPages(t, items, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		if _, err := DescribeSplitLeft(nil, left, right, newItem); err == nil {
			t.Error("a describe with no pre-split page was accepted")
		}
		pre, left, right := splitLeftTestPages(t, items, newItem, 1, 3, leftBlk, rightBlk, sibBlk)
		if _, err := DescribeSplitLeft(pre, left, right, nil); err == nil {
			t.Error("a describe with no new item was accepted")
		}
	})
}

// TestParseSplitLeftBlockDataRejectsMalformed pins the framing: the run is
// untagged, so a size that runs past the payload or a trailing remainder means
// producer and consumer disagree — which must fail loudly, not build a short
// page.
func TestParseSplitLeftBlockDataRejectsMalformed(t *testing.T) {
	hk := PGBTPivotRaw([]byte("sep"), PNone)
	good := SplitLeftBlockData(SplitLeftDescription{HighKey: hk})
	if _, _, err := ParseSplitLeftBlockData(good, false); err != nil {
		t.Fatalf("well-formed payload rejected: %v", err)
	}
	if _, _, err := ParseSplitLeftBlockData(good, true); err == nil {
		t.Error("a one-tuple payload parsed as two")
	}
	if _, _, err := ParseSplitLeftBlockData(append(append([]byte(nil), good...), 0, 0, 0, 0, 0, 0, 0, 0), false); err == nil {
		t.Error("trailing bytes accepted")
	}
	if _, _, err := ParseSplitLeftBlockData(good[:len(good)-2], false); err == nil {
		t.Error("a truncated tuple accepted")
	}
}

// TestRealTreeSplitsAreDescribable is the premise test. S11.5b deferred the
// incremental form because goopg's split "is not upstream's"; this drives a real
// tree through hundreds of splits and requires that every one of them EXCEPT the
// root splits (where upstream's redo drops BTP_ROOT and goopg's runtime does not)
// both describes and reproduces. Without it the encoder could silently fall back
// to an image on every split and every other test here would still pass.
func TestRealTreeSplitsAreDescribable(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9211, Fork: storage.MainFork}

	var splits, described, rootSplits int
	logSplit := func(_ storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, prePage, leftPage, rightPage storage.Page, newItem []byte, sibBlk storage.BlockNumber, sibPage storage.Page, childBlk storage.BlockNumber) (storage.LSN, error) {
		splits++
		if len(prePage) != storage.BlockSize || len(newItem) == 0 {
			t.Fatalf("split %d reached the hook with no pre-split page / new item", splits)
		}
		isRoot := readOpaque(prePage).IsRoot()
		level := readOpaque(rightPage).Level
		d, derr := DescribeSplitLeft(prePage, leftPage, rightPage, newItem)
		if derr != nil {
			t.Fatalf("split %d (root=%v level=%d) is not describable: %v", splits, isRoot, level, derr)
		}
		cerr := CheckSplitLeft(prePage, leftPage, level, rightBlk, d)
		switch {
		case isRoot:
			rootSplits++
			if cerr == nil {
				t.Errorf("split %d of a ROOT page reproduced the primary's page; upstream's redo drops BTP_ROOT", splits)
			}
		case cerr != nil:
			t.Fatalf("split %d (level %d, blk %d) does not reproduce the left half: %v", splits, level, leftBlk, cerr)
		default:
			described++
		}
		return storage.LSN(2000 + splits), nil
	}

	bt, err := CreateWithOptions(pool, rel, Options{LogSplit: logSplit})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	key := make([]byte, 120)
	for i := range 3000 {
		key[0], key[1], key[2] = byte(i>>16), byte(i>>8), byte(i)
		if err := bt.Insert(append([]byte(nil), key...), storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if splits < 20 {
		t.Fatalf("only %d splits — the tree did not exercise the path", splits)
	}
	if described < splits-rootSplits {
		t.Fatalf("%d/%d non-root splits reproduced the left half", described, splits-rootSplits)
	}
	if rootSplits == 0 {
		t.Log("no root split observed (the fallback arm was not exercised here)")
	}
}
