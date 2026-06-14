// verify_nbtree.go implements the page-structural core of upstream amcheck's
// bt_index_check() — the B-tree index integrity checker
// (postgres/contrib/amcheck/verify_nbtree.c). It is the index-side companion
// to the heap-side engine in verify_heapam.go and follows the same
// engine-first/wire-later pattern: the SQL surface (CREATE EXTENSION amcheck +
// the bt_index_check / bt_index_parent_check functions) is wired in a later
// loop once the tree is clean. See docs/design/0110-0005-verify-heapam-engine.md.
//
// This first tier ports the per-page sanity checks that upstream applies to
// every page it reads, in verify_nbtree.c:palloc_btree_page (the metapage
// magic/version validation and the leaf/internal page-level consistency
// checks). These are deterministic functions of a single page's raw bytes —
// they need neither clog, sibling traversal, nor the index TupleDesc — so they
// map faithfully onto goopg's B-tree page format
// (internal/access/btree/btree.go).
//
// goopg / upstream PG divergences handled here:
//
//   - High key placement. Upstream stores a page's high key as line-pointer
//     item P_HIKEY (offset 1) and derives P_FIRSTDATAKEY from it; goopg keeps
//     the high key in the per-page opaque special area (BTPageOpaque.HighKey),
//     so the item-count checks that upstream phrases in terms of P_HIKEY /
//     P_FIRSTDATAKEY ("internal block lacks high key and/or at least one
//     downlink", "non-rightmost leaf block lacks high key item") do not
//     translate and are NOT ported. The metapage and page-level checks, which
//     do not depend on high-key placement, port cleanly.
//
//   - MaxIndexTuplesPerPage ceiling. Upstream's item-count upper bound is a
//     constant derived from PG's IndexTupleData size; goopg's index-tuple
//     on-disk layout differs (keys are stored inline with a 2-byte length, see
//     btree.go), so the ceiling is computed from goopg's own per-item footprint
//     and exported as btree.MaxItemsPerPage. VerifyBtreePage flags a page whose
//     line-pointer count exceeds it (upstream-verbatim message, goopg constant).
//
//   - Single on-disk version. Upstream accepts a range
//     [BTREE_MIN_VERSION, BTREE_VERSION]; goopg writes exactly one metapage
//     version (btree.BTreeVersion), so the version check is an equality test
//     and the "minimum supported version" reported in the message equals the
//     current version.
//
// The page-local key-comparison tier of bt_target_page_check — the item-order
// and high-key invariants that need only the page's own bytes plus its high key
// — is ported in VerifyBtreeItemOrder below. The cross-page sibling-link tier —
// the left-link/right-link agreement, per-level uniformity, and circular-chain
// checks that a horizontal walk of one level performs (verify_nbtree.c's
// bt_check_level_from_leftmost loop) — is ported in VerifyBtreeLevelSiblingLinks.
// The cross-level descent tier — the per-downlink child checks that
// bt_index_parent_check performs (verify_nbtree.c's bt_child_check: child not
// deleted, child level one down, and the parent's separator key as a lower bound
// on every child key) — is ported in VerifyBtreeParentDownlinks. The remaining
// clog-dependent tier (heapallindexed / bloom-filter fingerprinting, which needs
// a heap scan and the index TupleDesc) is deferred; see the design doc.
package amcheck

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// BtreeReport is one B-tree index corruption finding. Block is the 0-based
// block number the corruption was found on; Msg is the upstream-matching
// corruption message (verbatim from verify_nbtree.c, including the
// in index "<name>" clause, so the later SQL surface and the
// 003_check/005_opclass_damage ports can reuse it).
type BtreeReport struct {
	Block storage.BlockNumber
	Msg   string
}

// VerifyBtreePage runs the page-structural bt_index_check tier on a single
// B-tree page. indexName is the relation name woven into the upstream messages
// (the SQL surface supplies it from the regclass; page-bytes-only callers may
// pass any label). It returns 0 or 1 findings — like upstream's
// palloc_btree_page, the first structural problem on a page is conclusive — and
// never an error: a structurally unreadable page surfaces as a finding, not a
// Go error, mirroring the report-and-continue model of VerifyHeapPage.
func VerifyBtreePage(p storage.Page, blkno storage.BlockNumber, indexName string) []BtreeReport {
	// The metapage (block 0) carries no opaque leaf/level semantics; it is
	// validated solely by its magic and version, mirroring upstream's
	// dedicated BTREE_METAPAGE branch in palloc_btree_page.
	if blkno == btree.MetaBlock {
		meta := btree.ParseMeta(p)
		if meta.Magic != btree.BTreeMagic {
			return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
				"index \"%s\" meta page is corrupt", indexName)}}
		}
		if meta.Version != btree.BTreeVersion {
			return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
				"version mismatch in index \"%s\": file version %d, current version %d, minimum supported version %d",
				indexName, meta.Version, btree.BTreeVersion, btree.BTreeVersion)}}
		}
		return nil
	}

	opaque := btree.ParseOpaque(p)

	// Deleted pages type-pun their level field and hold no items, so upstream
	// skips the level checks for them (the !P_ISDELETED guard in
	// palloc_btree_page). goopg's deleted pages are similarly off-limits.
	if opaque.IsDeleted() {
		return nil
	}

	// A leaf page sits at level 0; an internal page never does. A mismatch
	// means the page's role and its recorded level disagree — structural
	// corruption (verify_nbtree.c:palloc_btree_page).
	if opaque.IsLeaf() && opaque.Level != 0 {
		return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
			"invalid leaf page level %d for block %d in index \"%s\"",
			opaque.Level, blkno, indexName)}}
	}
	if !opaque.IsLeaf() && opaque.Level == 0 {
		return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
			"invalid internal page level 0 for block %d in index \"%s\"",
			blkno, indexName)}}
	}

	// Item-count ceiling: a page cannot hold more line-pointer items than
	// physically fit (palloc_btree_page, verify_nbtree.c:3396-3402). goopg's
	// per-item footprint differs from PG's, so the bound is
	// btree.MaxItemsPerPage rather than PG's MaxIndexTuplesPerPage, but the
	// message text is upstream-verbatim. A line-pointer count above the bound
	// can only mean a corrupt pd_lower (the item bodies could never have fit).
	//
	// Divergence: upstream applies this check to deleted pages too (it sits
	// outside palloc_btree_page's !P_ISDELETED guard); goopg returns above for
	// deleted pages, but a goopg deleted page holds no live items so the check
	// is moot there. Reading pd_lower only after the deleted guard also avoids
	// a deleted page's type-punned fields. The metapage returned earlier (it
	// carries no line pointers).
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
			"index \"%s\" has a damaged page at block %d: %v", indexName, blkno, err)}}
	}
	if count > btree.MaxItemsPerPage {
		return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
			"Number of items on block %d of index \"%s\" exceeds MaxIndexTuplesPerPage (%d)",
			blkno, indexName, btree.MaxItemsPerPage)}}
	}

	return nil
}

// VerifyBtreeItemOrder ports the two per-item key invariants from upstream
// amcheck's bt_target_page_check (verify_nbtree.c:1565-1642) — the page-local
// checks that need only the page's own bytes plus its high key, not sibling
// traversal or cross-level descent:
//
//   - High-key invariant. On a non-rightmost page every item key must respect
//     the page's high key: <= the high key on a leaf page, but strictly < the
//     high key on an internal page (upstream weakens the leaf check to <=
//     because suffix truncation can leave a leaf high key that is an untruncated
//     copy of the last data item; an internal high key is "just another
//     separator" and is unique on its level). Violation message:
//     `high key invariant violated for index "%s"`.
//
//   - Item-order invariant. Items must be stored in strictly ascending key
//     order: each item's key must be strictly less than the next item's key.
//     Violation message: `item order invariant violated for index "%s"`.
//
// goopg specifics that make this port faithful:
//
//   - The high key lives in the opaque special area (BTPageOpaque.HighKey), not
//     as line-pointer item P_HIKEY, so it is never one of the page's items and
//     needs no P_HIKEY-skip; rightmost / has-high-key gating matches the engine's
//     own keyExceedsHighKey (Next == InvalidBlockNumber means rightmost).
//   - Internal pages carry a leftmost negative-infinity downlink whose key is
//     empty (see findChildBlock); an empty key compares strictly less than any
//     real separator, so it satisfies both invariants without a special case,
//     exactly as upstream's zero-attribute negative-infinity tuple does.
//   - Keys compare with btree.CompareKeys (order-preserving encoded bytes), the
//     same comparator the live index uses, and are decoded through
//     btree.PageItemKeys so posting-list items contribute their shared separator
//     key once — the comparison is over stored separators, not expanded TIDs.
//
// Like VerifyBtreePage it returns 0 or 1 findings: upstream ereport(ERROR)s on
// the first violation, so the first per-page violation is conclusive. The
// metapage and deleted pages hold no orderable items and yield nil. A page whose
// items cannot be decoded surfaces as a finding, never a Go error, matching the
// report-and-continue model of the heap engine.
func VerifyBtreeItemOrder(p storage.Page, blkno storage.BlockNumber, indexName string) []BtreeReport {
	// The metapage has no data items; deleted pages hold none either (their
	// level field is type-punned and the page carries no live tuples).
	if blkno == btree.MetaBlock {
		return nil
	}
	opaque := btree.ParseOpaque(p)
	if opaque.IsDeleted() {
		return nil
	}

	keys, err := btree.PageItemKeys(p)
	if err != nil {
		return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
			"index \"%s\" has a damaged page at block %d: %v", indexName, blkno, err)}}
	}

	leaf := opaque.IsLeaf()
	// A non-rightmost page that carries a high key bounds every item from above.
	// This mirrors the engine's keyExceedsHighKey gating (rightmost pages have no
	// high key to honour).
	checkHighKey := opaque.HasHighKey() && opaque.Next != storage.InvalidBlockNumber

	for i, key := range keys {
		if checkHighKey {
			// Leaf: key <= high key. Internal: key < high key.
			cmp := btree.CompareKeys(key, opaque.HighKey)
			if (leaf && cmp > 0) || (!leaf && cmp >= 0) {
				return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
					"high key invariant violated for index \"%s\"", indexName)}}
			}
		}
		// Item order: current key strictly less than the next item's key.
		if i+1 < len(keys) && btree.CompareKeys(key, keys[i+1]) >= 0 {
			return []BtreeReport{{Block: blkno, Msg: fmt.Sprintf(
				"item order invariant violated for index \"%s\"", indexName)}}
		}
	}
	return nil
}

// PageSource yields the raw bytes of a B-tree page by block number. It is the
// minimal relation-walking dependency the cross-page tier needs: the SQL surface
// will satisfy it from the index's smgr (read block N), while page-bytes-only
// callers and tests can back it with an in-memory map. An error (block out of
// range, short read) is surfaced by the driver as a damaged-page finding, never
// a Go panic — matching the report-and-continue model of the per-page tiers.
type PageSource func(storage.BlockNumber) (storage.Page, error)

// VerifyBtreeLevelSiblingLinks ports the cross-page sibling-link checks that
// upstream amcheck performs while walking one B-tree level left-to-right, in
// bt_check_level_from_leftmost (verify_nbtree.c:650-790). Starting from leftmost
// it follows each page's btpo_next (BTPageOpaque.Next) to the rightmost page of
// the level, applying the three invariants that need only sibling-link state —
// no clog, no index TupleDesc, no parent/downlink descent:
//
//   - Sibling-link agreement. Each page's btpo_prev (BTPageOpaque.Prev) must
//     equal the block we arrived from. Upstream gates this on leftcurrent != P_NONE
//     (`opaque->btpo_prev != leftcurrent && leftcurrent != P_NONE`), so the
//     leftmost page — reached with leftcurrent == P_NONE — is exempt (its left
//     link may legitimately differ when its left sibling is half-dead). Violation
//     message: `left link/right link pair in index "%s" not in agreement`
//     (verify_nbtree.c:1193, the bt_recheck_sibling_links readonly path).
//
//   - Per-level uniformity. Every page reached on this horizontal walk must
//     report the same btpo_level as the leftmost page of the level (upstream's
//     `level.level != opaque->btpo_level`). Violation message: `leftmost down
//     link for level points to block in index "%s" whose level is not one level
//     down` (verify_nbtree.c:774).
//
//   - Circular link chain. A corrupt index can form a sibling cycle; upstream
//     detects the immediate case (`current == leftcurrent || current ==
//     opaque->btpo_prev`). goopg tracks every block visited on the walk and flags
//     a revisit — this subsumes the immediate case (a self-loop or back-link
//     revisits within one step) and also bounds the walk against longer cycles a
//     bytes-only checker cannot otherwise terminate. Message: `circular link
//     chain found in block %u of index "%s"` (verify_nbtree.c:787).
//
// goopg / upstream divergences:
//
//   - P_NONE is storage.InvalidBlockNumber; a rightmost page (btpo_next ==
//     P_NONE) ends the walk, exactly as upstream's P_RIGHTMOST.
//   - Reaching a fully deleted page through a sibling link is itself corruption
//     in readonly mode: upstream ereports `downlink or sibling link points to
//     deleted block in index "%s"` (verify_nbtree.c:676). goopg mirrors that —
//     a deleted page is unlinked and must not be reachable.
//   - The metapage is never part of a level walk (it carries no sibling links),
//     so a leftmost of MetaBlock is treated as a damaged starting point.
//
// Like the per-page tiers it returns 0 or 1 findings: upstream ereport(ERROR)s on
// the first cross-page violation, so the first one the walk reaches is conclusive.
// It performs only the cross-page checks; per-page structure (VerifyBtreePage) and
// per-page key order (VerifyBtreeItemOrder) are run separately and composed by the
// SQL surface.
func VerifyBtreeLevelSiblingLinks(src PageSource, leftmost storage.BlockNumber, indexName string) []BtreeReport {
	if leftmost == btree.MetaBlock {
		return []BtreeReport{{Block: leftmost, Msg: fmt.Sprintf(
			"index \"%s\" has a damaged page at block %d: metapage is not part of a level",
			indexName, leftmost)}}
	}

	// leftcurrent is the block we arrived from; P_NONE for the leftmost page.
	leftcurrent := storage.InvalidBlockNumber
	current := leftmost
	var expectLevel uint32
	first := true
	visited := make(map[storage.BlockNumber]bool)

	for current != storage.InvalidBlockNumber {
		// Circular detection: a block reached twice on one horizontal walk is a
		// sibling cycle (subsumes upstream's immediate current==leftcurrent /
		// current==btpo_prev check and bounds longer cycles).
		if visited[current] {
			return []BtreeReport{{Block: current, Msg: fmt.Sprintf(
				"circular link chain found in block %d of index \"%s\"", current, indexName)}}
		}
		visited[current] = true

		p, err := src(current)
		if err != nil {
			return []BtreeReport{{Block: current, Msg: fmt.Sprintf(
				"index \"%s\" has a damaged page at block %d: %v", indexName, current, err)}}
		}
		opaque := btree.ParseOpaque(p)

		// A deleted page must not be reachable through a sibling link.
		if opaque.IsDeleted() {
			return []BtreeReport{{Block: current, Msg: fmt.Sprintf(
				"downlink or sibling link points to deleted block in index \"%s\"", indexName)}}
		}

		// Sibling-link agreement (exempt only the leftmost page, leftcurrent == P_NONE).
		if leftcurrent != storage.InvalidBlockNumber && opaque.Prev != leftcurrent {
			return []BtreeReport{{Block: current, Msg: fmt.Sprintf(
				"left link/right link pair in index \"%s\" not in agreement", indexName)}}
		}

		// Per-level uniformity: pin the expected level from the leftmost page.
		if first {
			expectLevel = opaque.Level
			first = false
		} else if opaque.Level != expectLevel {
			return []BtreeReport{{Block: current, Msg: fmt.Sprintf(
				"leftmost down link for level points to block in index \"%s\" whose level is not one level down",
				indexName)}}
		}

		leftcurrent = current
		current = opaque.Next
	}
	return nil
}

// VerifyBtreeParentDownlinks ports the cross-level (parent->child) checks that
// upstream amcheck's bt_index_parent_check applies to every downlink of an
// internal page, in bt_child_check (verify_nbtree.c:2393-2543). parentBlk must
// be an internal page; for each of its downlink entries (separator key K_i,
// child block C_i) the child page is read through src and three invariants —
// none of which need clog, the index TupleDesc, or sibling traversal — are
// checked:
//
//   - Downlink-to-deleted. A fully deleted page is unlinked and cannot be a
//     legitimate downlink target in readonly mode. Violation message:
//     `downlink to deleted page found in index "%s"` (verify_nbtree.c:2494).
//
//   - Child level one down. An internal parent at btpo_level L must only point
//     to children at level L-1 (upstream checks this while combining the
//     downlink visit with bt_child_highkey_check's connectivity test). Violation
//     message: `downlink points to block in index "%s" whose level is not one
//     level down` (verify_nbtree.c:2655, errmsg_internal).
//
//   - Down-link lower bound invariant. The parent's separator key K_i must be a
//     lower bound on every key in child C_i (bt_child_check's
//     invariant_l_nontarget_offset loop, verify_nbtree.c:2500-2540). Violation
//     message: `down-link lower bound invariant violated for index "%s"`
//     (verify_nbtree.c:2535).
//
// goopg / upstream divergences that make this port faithful:
//
//   - Inclusive vs strict lower bound. Upstream (heapkeyspace) requires the
//     downlink key strictly less than every non-negative-infinity child key,
//     because nbtree suffix-truncates separators and tie-breaks on heap TID.
//     goopg routes a search to the rightmost internal item whose key <= the
//     search key (findChildBlock), so child C_i covers the half-open range
//     [K_i, K_{i+1}); K_i is therefore an INCLUSIVE lower bound and the faithful
//     goopg test is CompareKeys(childKey, K_i) >= 0. Using upstream's strict
//     test here would raise false positives on every separator that equals its
//     child's first key.
//
//   - Negative-infinity skip. Upstream skips the child's negative-infinity item
//     (offset_is_negative_infinity: the first data item of an internal page).
//     goopg stores that item with the empty key (findChildBlock's leftmost
//     convention); an empty key sorts below any real K_i and would falsely trip
//     the lower-bound test, so the first item of an internal child is skipped,
//     exactly as upstream. A leaf child has no negative-infinity item and all of
//     its keys are checked.
//
//   - P_NONE / block reads. A child read error (out of range, short read)
//     surfaces as a damaged-page finding, never a Go panic, matching the
//     report-and-continue model of the other tiers.
//
// Like the other tiers it returns 0 or 1 findings: upstream ereport(ERROR)s on
// the first violation, so the first downlink problem the scan reaches is
// conclusive. A leaf or deleted parentBlk (no downlinks to descend) and the
// metapage yield nil. It performs only the cross-level checks; per-page
// structure and key order are run separately and composed by the SQL surface.
func VerifyBtreeParentDownlinks(src PageSource, parentBlk storage.BlockNumber, indexName string) []BtreeReport {
	if parentBlk == btree.MetaBlock {
		return nil
	}

	parent, err := src(parentBlk)
	if err != nil {
		return []BtreeReport{{Block: parentBlk, Msg: fmt.Sprintf(
			"index \"%s\" has a damaged page at block %d: %v", indexName, parentBlk, err)}}
	}
	popaque := btree.ParseOpaque(parent)

	// Only internal, live pages have downlinks to descend. A leaf or deleted
	// page yields nil (upstream descends from internal target pages only).
	if popaque.IsLeaf() || popaque.IsDeleted() {
		return nil
	}

	downlinks, err := btree.PageDownlinks(parent)
	if err != nil {
		return []BtreeReport{{Block: parentBlk, Msg: fmt.Sprintf(
			"index \"%s\" has a damaged page at block %d: %v", indexName, parentBlk, err)}}
	}

	for _, dl := range downlinks {
		child, err := src(dl.Child)
		if err != nil {
			return []BtreeReport{{Block: dl.Child, Msg: fmt.Sprintf(
				"index \"%s\" has a damaged page at block %d: %v", indexName, dl.Child, err)}}
		}
		copaque := btree.ParseOpaque(child)

		// Downlink-to-deleted: a deleted page is unlinked and must not be a
		// downlink target.
		if copaque.IsDeleted() {
			return []BtreeReport{{Block: dl.Child, Msg: fmt.Sprintf(
				"downlink to deleted page found in index \"%s\"", indexName)}}
		}

		// Child level must be exactly one below the parent's.
		if copaque.Level != popaque.Level-1 {
			return []BtreeReport{{Block: dl.Child, Msg: fmt.Sprintf(
				"downlink points to block in index \"%s\" whose level is not one level down", indexName)}}
		}

		// Down-link lower bound: every real child key must be >= the parent's
		// separator key for this child. Skip the child's negative-infinity item
		// (the first item of an internal child, stored with the empty key).
		keys, err := btree.PageItemKeys(child)
		if err != nil {
			return []BtreeReport{{Block: dl.Child, Msg: fmt.Sprintf(
				"index \"%s\" has a damaged page at block %d: %v", indexName, dl.Child, err)}}
		}
		childLeaf := copaque.IsLeaf()
		for i, k := range keys {
			if !childLeaf && i == 0 {
				// Negative-infinity downlink on an internal child — no lower
				// bound applies (offset_is_negative_infinity).
				continue
			}
			if btree.CompareKeys(k, dl.Key) < 0 {
				return []BtreeReport{{Block: dl.Child, Msg: fmt.Sprintf(
					"down-link lower bound invariant violated for index \"%s\"", indexName)}}
			}
		}
	}
	return nil
}
