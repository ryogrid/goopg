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
//     btree.go), so a faithful ceiling needs goopg-specific tuple-size
//     accounting. That check is deferred to its own tier rather than ported
//     with a wrong constant.
//
//   - Single on-disk version. Upstream accepts a range
//     [BTREE_MIN_VERSION, BTREE_VERSION]; goopg writes exactly one metapage
//     version (btree.BTreeVersion), so the version check is an equality test
//     and the "minimum supported version" reported in the message equals the
//     current version.
//
// The page-local key-comparison tier of bt_target_page_check — the item-order
// and high-key invariants that need only the page's own bytes plus its high key
// — is ported in VerifyBtreeItemOrder below. The clog/sibling-dependent tiers
// (downlink/sibling-link agreement, cross-page item order, cross-level descent
// via bt_index_parent_check) are deferred; see the design doc.
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
