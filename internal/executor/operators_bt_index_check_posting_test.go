package executor

// End-to-end coverage for the checkunique tier's POSTING-LIST arm over a posting
// list goopg's own deduplication produced, rather than one a test encoder wrote.
//
// internal/amcheck/verify_nbtree_unique_posting_test.go covers the arm's logic
// against pages built through btree.IndexFormat.PGBTPostingRaw. That proves the
// arm agrees with the ENCODER; it cannot prove the arm ever meets a posting list
// the engine actually wrote, nor that the tier's heap-visibility filter works
// against a real heap. This test closes both gaps: the index is built by the
// normal CREATE INDEX bulk-build path (deduplicateToRawItems, bulkload.go), the
// heap is a real heap, and liveness is judged by the executor's own snapshot.
//
// The one thing injected is the index's UNIQUE-ness, and that is forced by a
// goopg limitation worth naming: no goopg unique index can hold a posting list
// today. The bulk build is the only producer of posting bytes (the pre-split
// dedupConsolidate merely drops exact (key,tid) duplicates, btree.go), and it
// indexes only tuples live to the building snapshot — so it never sees the dead
// row versions that give a real PG unique index its duplicates. Upstream has no
// such gap: _bt_delete_or_dedup_one_page runs _bt_dedup_pass on unique indexes
// too (nbtinsert.c:2778), which is precisely why bt_entry_unique_check has to
// walk posting lists at all. Flipping catalog Index.Unique on an index whose
// pages the real producer wrote is therefore the closest reachable stand-in, and
// it is itself a faithful corruption: `pg_index.indisunique` claiming a
// uniqueness the content does not have is what a broken build or a changed
// collation leaves behind. The gap is a deferral-ledger row (2026-08-12).
// M0119-0006.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// leafPostingStats reads every leaf page of idxName through the same reader the
// checkunique tier uses (btree.IndexFormat.PageLeafItems) and reports how many
// (key, heap TID) entries it yields and how many of those came out of a posting
// list. It is deliberately not a byte-level inspection: what the test needs to
// know is what the TIER sees.
func leafPostingStats(t *testing.T, ctx *Context, idxName string) (entries, postingEntries int) {
	t.Helper()
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected in-memory catalog")
	}
	idx, ok := im.LookupIndex(parser.ObjectName{Name: idxName})
	if !ok {
		t.Fatalf("index %q not found", idxName)
	}
	rel := ctx.Catalog.IndexRelFileNode(idx)
	fm := btree.IndexFormatFor(ctx.pgIndexKeyDesc(idx))
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	for blk := btree.MetaBlock + 1; blk < nblocks; blk++ {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			t.Fatalf("pin block %d: %v", blk, perr)
		}
		page := make(storage.Page, len(s.Page()))
		copy(page, s.Page())
		ctx.Pool.Unpin(s)
		if !btree.ParseOpaque(page).IsLeaf() {
			continue
		}
		its, err := fm.PageLeafItems(page)
		if err != nil {
			t.Fatalf("PageLeafItems(block %d): %v", blk, err)
		}
		entries += len(its)
		for _, it := range its {
			if it.PostingIndex >= 0 {
				postingEntries++
			}
		}
	}
	return entries, postingEntries
}

// TestBtIndexCheck_CheckUniquePostingListRealTree drives the arm over an index
// whose duplicate entries live INSIDE line pointers because goopg's bulk build
// put them there.
//
// Phases, in the order that makes the gate non-vacuous:
//  1. the build really produced posting lists (asserted through the tier's own
//     reader — without this the rest would pass on a plain-item index and prove
//     nothing about the arm),
//  2. with the index declared unique, checkunique OFF is clean and ON reports the
//     upstream message with the ` posting N` errdetail that only a duplicate
//     sharing one line pointer produces,
//  3. after deleting every duplicate row the posting lists are still physically
//     present, yet the tier goes clean — the visibility filter judging a real
//     heap, which is exactly how a deduplicated PG unique index accumulates dead
//     versions without being corrupt.
func TestBtIndexCheck_CheckUniquePostingListRealTree(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// Ten rows per key: enough duplicates per run that the build has something to
	// merge, few enough that the whole index is one leaf page.
	if err := runDDL(t, ctx, "CREATE TABLE bicp (a int, b int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := range 40 {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO bicp VALUES (%d, %d)", i%4, i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Built NON-unique: the build refuses duplicate keys for a unique index
	// (23505), which is the limitation the file header describes.
	if err := runDDL(t, ctx, "CREATE INDEX bicp_a_idx ON bicp (a)"); err != nil {
		t.Fatalf("create index: %v", err)
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	// (1) The bulk build deduplicated: the entries the tier will walk come out of
	// posting lists, not out of one line pointer each.
	entries, postings := leafPostingStats(t, ctx, "bicp_a_idx")
	if entries != 40 {
		t.Fatalf("leaf holds %d entries, want the 40 indexed rows", entries)
	}
	if postings == 0 {
		t.Fatalf("bulk build produced no posting list (%d entries, all plain); "+
			"this test would exercise the plain-item path only", entries)
	}

	im := ctx.Catalog.(*catalog.InMemory)
	idx, ok := im.LookupIndex(parser.ObjectName{Name: "bicp_a_idx"})
	if !ok {
		t.Fatal("index bicp_a_idx not found")
	}
	idx.Unique = true // the injected corruption; see the file header

	// (2a) Every other tier is silent: the page is structurally perfect and its
	// key run is non-decreasing, so only checkunique can have an opinion.
	for _, sql := range []string{
		"SELECT bt_index_check('bicp_a_idx')",
		"SELECT bt_index_check('bicp_a_idx', false, false)",
		"SELECT bt_index_parent_check('bicp_a_idx', false, false, false)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Fatalf("%s: posting-list duplicates leaked into a non-checkunique tier: %v", sql, err)
		}
	}

	// (2b) checkunique reports, and the errdetail names two POSTING POSITIONS of
	// one index tid — bt_report_duplicate's spelling when both conflicting
	// entries share a line pointer (verify_nbtree.c). A per-line-pointer walk
	// would have found nothing here at all.
	for _, sql := range []string{
		"SELECT bt_index_check('bicp_a_idx', false, true)",
		"SELECT bt_index_parent_check('bicp_a_idx', false, false, true)",
	} {
		_, err := runQueryWithErr(ctx, sql)
		if err == nil {
			t.Errorf("%s: duplicate inside a posting list not detected", sql)
			continue
		}
		const want = `index uniqueness is violated for index "bicp_a_idx"`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %q, want substring %q", sql, err.Error(), want)
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%s: error is %T, want *ExecError", sql, err)
			continue
		}
		if !strings.Contains(ee.Detail, "posting 0 and posting 1") {
			t.Errorf("%s: DETAIL %q does not report the two posting positions; "+
				"the duplicate sits inside one line pointer", sql, ee.Detail)
		}
	}

	// (3) Delete every duplicate, leaving one live row per key. Nothing touches
	// the index — the posting lists survive verbatim — but only one TID per list
	// is now visible, which is the shape a deduplicated unique index has in PG
	// whenever a row has been updated. The tier must go quiet.
	if err := runDDL(t, ctx, "DELETE FROM bicp WHERE b > 3"); err != nil {
		t.Fatalf("delete duplicates: %v", err)
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	entriesAfter, postingsAfter := leafPostingStats(t, ctx, "bicp_a_idx")
	if entriesAfter != entries || postingsAfter != postings {
		t.Fatalf("index changed under DELETE (entries %d→%d, posting entries %d→%d); "+
			"the clean assertion below would no longer be about a posting list",
			entries, entriesAfter, postings, postingsAfter)
	}
	for _, sql := range []string{
		"SELECT bt_index_check('bicp_a_idx', false, true)",
		"SELECT bt_index_parent_check('bicp_a_idx', false, false, true)",
	} {
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Errorf("%s: posting list of dead row versions reported as a duplicate: %v", sql, err)
		}
	}
}
