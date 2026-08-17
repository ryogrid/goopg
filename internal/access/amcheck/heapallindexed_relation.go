package amcheck

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// This file ports the index-side relation walk of the heapallindexed tier — the
// "fingerprint phase" driver of upstream amcheck's bt_check_every_level /
// bt_target_page_check, which adds every non-dead leaf IndexTuple to the Bloom
// filter while it walks the index (postgres/contrib/amcheck/verify_nbtree.c:1500-1511).
// The pure fingerprint+probe core is already ported (VerifyBtreeHeapAllIndexed in
// heapallindexed.go); what this adds is the leaf-level enumeration that produces
// the index entry set from a live index, behind the same PageSource seam the
// cross-page / cross-level B-tree tiers already take.
//
// It is the index-side counterpart to the heap engine's relation-walking driver
// (VerifyHeapRelation in verify_heapam_relation.go). Keeping the leaf walk here —
// rather than in the eventual bt_index_check SQL surface — means the SQL slice
// (docs/design/0110-0007) reduces to a thin adapter: fill a PageSource from the
// index's smgr/buffer manager, form the heap-side entries from each live heap
// tuple (table_index_build_scan, the catalog-coupled half that stays at the wire
// layer exactly as upstream draws the line), and compose.
//
// Scope boundary, mirroring where loop #63's heap driver and upstream draw the
// line: the heap scan + index_form_tuple that produce the heapEntries argument
// are catalog- and TupleDesc-coupled (they need the index's key columns,
// expressions, and collations to re-form each heap tuple's would-be index
// tuple), so they remain a wire-layer concern and are passed in here as a slice —
// the same shape VerifyBtreeHeapAllIndexed already consumes. What this file ports
// is exactly the part that is a deterministic function of the index's page bytes:
// descending to the leftmost leaf and walking the leaf level to collect entries.

// CollectBtreeLeafEntries walks a B-tree index's leaf level and returns every
// (key, heap TID) entry, in leaf-level left-to-right then slot order, expanding
// each posting-list item to one entry per heap TID (btree.PageLeafEntries does
// the expansion). These are the entries the heapallindexed fingerprint phase adds
// to its Bloom filter — the "plain tuples" upstream's bt_posting_plain_tuple
// produces (verify_nbtree.c).
//
// It descends from the metapage's Root to the leftmost leaf (following the
// leftmost downlink at each internal level), then follows btpo_next across the
// leaf level. src yields each page's raw bytes by block number — the SQL surface
// fills it from the index's smgr, tests from a map; keyFmt is the index's
// on-page key format (the zero value is the blob format). Fully deleted leaf pages
// carry no live items and are skipped (their fields may be type-punned), matching
// the per-page tiers' deleted-page exemption.
//
// A structurally impossible index (Root is the metapage or InvalidBlockNumber —
// a real goopg tree always has its root+leaf at block 1) yields no entries rather
// than an error: an empty/absent leaf level simply contributes nothing to the
// filter. A read error, an unparseable page, or a sibling/downlink cycle is
// returned as an error so the SQL surface surfaces it rather than silently
// fingerprinting a truncated entry set (which would manufacture spurious "lacks
// matching index tuple" reports — the heapallindexed soundness invariant).
func CollectBtreeLeafEntries(src PageSource, keyFmt nbtree.IndexFormat) ([]nbtree.LeafEntry, error) {
	if src == nil {
		return nil, fmt.Errorf("amcheck: nil page source")
	}

	metaPage, err := src(nbtree.MetaBlock)
	if err != nil {
		return nil, fmt.Errorf("amcheck: reading metapage: %w", err)
	}
	meta := nbtree.ParseMeta(metaPage)
	if meta.Root == nbtree.MetaBlock || meta.Root == storage.InvalidBlockNumber {
		// No key level (defensive: a real goopg index always roots at block 1).
		return nil, nil
	}

	leftmost, err := leftmostLeafBlock(src, meta.Root, keyFmt)
	if err != nil {
		return nil, err
	}

	var entries []nbtree.LeafEntry
	visited := make(map[storage.BlockNumber]bool)
	for current := leftmost; current != storage.InvalidBlockNumber; {
		if visited[current] {
			return nil, fmt.Errorf("amcheck: circular leaf sibling chain at block %d", current)
		}
		visited[current] = true

		p, err := src(current)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading leaf block %d: %w", current, err)
		}
		opaque := nbtree.ParseOpaque(p)
		if !opaque.IsLeaf() {
			return nil, fmt.Errorf("amcheck: block %d on leaf walk is not a leaf page", current)
		}
		// A fully deleted page is unlinked and holds no live items; skip it
		// rather than decoding type-punned fields.
		if !opaque.IsDeleted() {
			leafEntries, err := keyFmt.PageLeafEntries(p)
			if err != nil {
				return nil, fmt.Errorf("amcheck: decoding leaf block %d: %w", current, err)
			}
			entries = append(entries, leafEntries...)
		}
		current = opaque.Next
	}
	return entries, nil
}

// leftmostLeafBlock descends from root to the leftmost leaf page, following the
// leftmost downlink (the negative-infinity entry, slot 1) at each internal level.
// The descent is bounded by a visited set so a corrupt downlink cycle terminates
// with an error rather than looping forever — a bytes-only checker cannot
// otherwise guarantee termination.
func leftmostLeafBlock(src PageSource, root storage.BlockNumber, keyFmt nbtree.IndexFormat) (storage.BlockNumber, error) {
	visited := make(map[storage.BlockNumber]bool)
	current := root
	for {
		if current == nbtree.MetaBlock || current == storage.InvalidBlockNumber {
			return 0, fmt.Errorf("amcheck: invalid child block %d during leftmost descent", current)
		}
		if visited[current] {
			return 0, fmt.Errorf("amcheck: circular downlink chain at block %d during leftmost descent", current)
		}
		visited[current] = true

		p, err := src(current)
		if err != nil {
			return 0, fmt.Errorf("amcheck: reading block %d during leftmost descent: %w", current, err)
		}
		opaque := nbtree.ParseOpaque(p)
		if opaque.IsLeaf() {
			return current, nil
		}
		downlinks, err := keyFmt.PageDownlinks(p)
		if err != nil {
			return 0, fmt.Errorf("amcheck: decoding internal block %d during leftmost descent: %w", current, err)
		}
		if len(downlinks) == 0 {
			return 0, fmt.Errorf("amcheck: internal block %d has no downlinks during leftmost descent", current)
		}
		// Slot 1 is the leftmost (negative-infinity) downlink.
		current = downlinks[0].Child
	}
}

// VerifyBtreeHeapAllIndexedRelation drives the heapallindexed tier end to end
// over a live index: it collects the index's leaf entries via CollectBtreeLeafEntries
// (the fingerprint set) and composes them with the caller-supplied heapEntries
// (the index tuple each live heap tuple would form — the probe set) through the
// pure VerifyBtreeHeapAllIndexed core. It returns one BtreeReport per heap tuple
// whose formed index tuple is absent from the index, with the upstream-verbatim
// "heap tuple (b,o) from table \"T\" lacks matching index tuple within index \"I\""
// message.
//
// idxSrc yields the index's pages and keyFmt is their key format; heapEntries are formed by the wire layer's
// heap scan (see the scope note above); seed is the Bloom hash seed (the SQL
// surface randomizes it per run so a different false-positive-masked subset is
// caught on re-check, tests pin it). An index-walk error (unreadable page,
// structural cycle) is returned rather than yielding a truncated fingerprint set
// that would manufacture spurious reports.
func VerifyBtreeHeapAllIndexedRelation(idxSrc PageSource, keyFmt nbtree.IndexFormat, heapEntries []nbtree.LeafEntry, indexName, tableName string, seed uint64) ([]BtreeReport, error) {
	leafEntries, err := CollectBtreeLeafEntries(idxSrc, keyFmt)
	if err != nil {
		return nil, err
	}
	return VerifyBtreeHeapAllIndexed(leafEntries, heapEntries, indexName, tableName, seed), nil
}
