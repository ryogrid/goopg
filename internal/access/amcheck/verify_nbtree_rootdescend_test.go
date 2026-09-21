package amcheck_test

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/access/amcheck"
	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// TestVerifyBtreeRootDescend covers amcheck's rootdescend tier on a REAL
// multi-level tree.
//
// The detection case is the point of the test, and of the tier: the structural
// tiers verify each page against its neighbours, so a tree can pass all of them
// and still be broken as a SEARCH STRUCTURE — an entry sits on a well-formed
// leaf, under a well-linked parent, yet a searcher starting at the root never
// arrives there. That is what upstream's bt_rootdescend catches and what a
// "healthy index still passes" test cannot distinguish from a check that does
// nothing at all. Since the defect being fixed here was exactly a check that
// reported clean without looking, a detection arm is mandatory rather than
// nice to have.
//
// The inconsistency is injected by swapping two leaf pages through the
// PageSource instead of editing item bytes. That keeps every page individually
// valid — the per-page tier still finds nothing — and breaks only the mapping
// between the search path and the page holding the entry, which isolates the
// property under test.
func TestVerifyBtreeRootDescend(t *testing.T) {
	// Enough keys to force several leaves under an internal root, so the
	// descent has real routing decisions to make.
	keys := make([][]byte, 0, 600)
	for i := 0; i < 600; i++ {
		keys = append(keys, []byte(fmt.Sprintf("k%06d", i)))
	}
	_, pool, rel, cleanup := buildRealTree(t, keys)
	defer cleanup()

	src := realPageSource(t, pool, rel)
	keyFmt := nbtree.IndexFormatFor(nil) // blob format
	metaPage, err := src(0)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	root := nbtree.ParseMeta(metaPage).Root
	if root == 0 {
		t.Fatal("tree has no root")
	}

	t.Run("healthy tree reports nothing", func(t *testing.T) {
		// This arm is also the routing check: if the descent's
		// child-selection rule disagreed with the one the tree was built
		// under, a healthy tree would produce findings here.
		reports, err := amcheck.VerifyBtreeRootDescend(src, root, "idx", keyFmt)
		if err != nil {
			t.Fatalf("VerifyBtreeRootDescend: %v", err)
		}
		if len(reports) != 0 {
			t.Errorf("healthy tree produced %d findings, want 0; first: %s",
				len(reports), reports[0].Msg)
		}
	})

	t.Run("unreachable entries are reported", func(t *testing.T) {
		leaves := leafBlocks(t, src, root)
		if len(leaves) < 2 {
			t.Fatalf("need >=2 leaves to swap, got %d", len(leaves))
		}
		a, b := leaves[0], leaves[len(leaves)-1]
		swapped := func(blk storage.BlockNumber) (storage.Page, error) {
			switch blk {
			case a:
				return src(b)
			case b:
				return src(a)
			}
			return src(blk)
		}
		reports, err := amcheck.VerifyBtreeRootDescend(swapped, root, "idx", keyFmt)
		if err != nil {
			t.Fatalf("VerifyBtreeRootDescend: %v", err)
		}
		if len(reports) == 0 {
			t.Fatal("swapping two leaf pages produced NO findings — the tier " +
				"is not actually searching from the root")
		}
		want := `could not find tuple using search from root page in index "idx"`
		if reports[0].Msg != want {
			t.Errorf("Msg = %q, want %q (upstream verbatim)", reports[0].Msg, want)
		}
	})
}

// TestRootDescendSupportedGatesOnKeyFormat pins the gate the caller uses to
// decide run-vs-refuse. Upstream asserts `key->heapkeyspace && key->scantid !=
// NULL` and raises ERRCODE_FEATURE_NOT_SUPPORTED otherwise; goopg's blob format
// is the side of that gate which cannot support the tier, because the heap TID
// is not in the key and so a search cannot identify ONE entry among duplicates.
func TestRootDescendSupportedGatesOnKeyFormat(t *testing.T) {
	if amcheck.RootDescendSupported(nbtree.IndexFormatFor(nil)) {
		t.Error("blob format reported as supported — it carries no heap TID in " +
			"the key, so the tier would report findings on a healthy index")
	}
	if !amcheck.RootDescendSupported(nbtree.IndexFormatFor(&nbtree.PGIndexKeyDesc{})) {
		t.Error("tuple format reported as unsupported — the heap TID is inside " +
			"the key image, which is what the tier needs")
	}
}

// leafBlocks returns every leaf block reachable from the root, left to right.
func leafBlocks(t *testing.T, src amcheck.PageSource, root storage.BlockNumber) []storage.BlockNumber {
	t.Helper()
	keyFmt := nbtree.IndexFormatFor(nil)
	var out []storage.BlockNumber
	var walk func(storage.BlockNumber)
	seen := make(map[storage.BlockNumber]bool)
	walk = func(blk storage.BlockNumber) {
		if seen[blk] {
			return
		}
		seen[blk] = true
		p, err := src(blk)
		if err != nil {
			t.Fatalf("read block %d: %v", blk, err)
		}
		if nbtree.ParseOpaque(p).IsLeaf() {
			out = append(out, blk)
			return
		}
		dls, err := keyFmt.PageDownlinks(p)
		if err != nil {
			t.Fatalf("downlinks of %d: %v", blk, err)
		}
		for _, d := range dls {
			walk(d.Child)
		}
	}
	walk(root)
	return out
}
