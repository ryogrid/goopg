package executor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-A — the descriptor PLUMBING, behaviour-preserving.
//
// 3b-2c-i put one comparer (`keyComparer`) behind every ordering decision in
// `internal/access/btree`, and 3b-2b built `buildPGIndexKeyDesc`, the
// catalog → `btree.PGIndexKeyDesc` mapper. What was missing between them is a
// route: the engine opened index btrees from nineteen scattered
// `btree.Open(ctx.Pool, idxRel)` / `btree.CreateWithXID` / `BulkCreateWithXID`
// calls, none of which had ever seen an index descriptor, so
// `Options.KeyDesc` was nil everywhere by construction.
//
// This file is that route, and nothing more. Every index btree the executor
// opens or builds now goes through one of the three helpers below, each of
// which resolves the descriptor once and passes it in `Options`. The FLIP —
// making the writers emit tuple-shaped, per-column-datum keys so a non-nil
// descriptor is correct — is 3b-2c-ii-B, and is REINDEX-required; until it
// lands `pgIndexTupleKeys` is false and every helper hands the btree a nil
// descriptor, i.e. exactly the bytewise `CompareKeys` ordering the current
// on-disk keys are built for.
//
// Splitting the plumbing out of the flip is not cosmetic. The flip has to be
// atomic across the writers and the comparison layer (3b-2c-i's header
// explains why a half-flipped tree mis-ORDERS rather than merely mis-reads),
// and "did every btree-opening site learn about descriptors?" is a question
// about nineteen call sites that is much better answered against a tree whose
// behaviour has not moved.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// pgIndexTupleKeys gates the per-column-datum key format.
//
// It is a var rather than a const so the plumbing is testable ahead of the
// flip: `pgindex_btree_test.go` turns it on to assert that a descriptor
// actually reaches the tree, and that an index this layer cannot describe
// still opens (with nil) instead of failing. Production code must never write
// it — 3b-2c-ii-B deletes the gate along with the last blob writer.
//
// false: `Options.KeyDesc` is always nil → `CompareKeys` (bytes.Compare) over
// goopg's opaque order-preserving key encoding, which is what is on disk.
var pgIndexTupleKeys = true

// pgIndexKeyDesc returns the key descriptor for idx, or nil when this layer
// cannot order the index exactly the way PostgreSQL does.
//
// A nil result is NOT an error: `buildPGIndexKeyDesc` deliberately refuses
// expression keys, explicit operator classes, non-bytewise collations and
// every type without a 3b-2a comparator, and those indexes must keep working.
// They keep the blob key path — which is why the flip cannot simply delete it
// (a dual-format decision 3b-2c-ii-B has to make explicit; see the ledger).
//
// The result is memoised per statement because the hot callers are per-ROW:
// maintainUniqueIndexesForInsert and friends open every index on the table for
// every tuple written, and buildPGIndexKeyDesc walks the key columns doing
// string type-name resolution. The cache is keyed by index OID and lives on
// the per-statement Context, so the only staleness window is DDL that alters a
// key column's type inside the very statement that is also writing through the
// index — which cannot happen, since ALTER TABLE takes AccessExclusiveLock.
func (ctx *Context) pgIndexKeyDesc(idx *catalog.Index) *btree.PGIndexKeyDesc {
	if !pgIndexTupleKeys || idx == nil {
		return nil
	}
	if desc, ok := ctx.pgKeyDescCache[idx.OID]; ok {
		// Present-but-nil is the memoised "cannot describe" answer; the
		// comma-ok is what keeps that from re-deriving the failure per row.
		return desc
	}
	desc, err := buildPGIndexKeyDesc(idx)
	if err != nil {
		desc = nil
	}
	if ctx.pgKeyDescCache == nil {
		ctx.pgKeyDescCache = make(map[uint32]*btree.PGIndexKeyDesc)
	}
	ctx.pgKeyDescCache[idx.OID] = desc
	return desc
}

// compositeUpperBound returns the inclusive upper bound to hand
// `RangeScan`/`RangeScanPos` for a probe on a COMPOSITE index whose search key
// may name only the leading key attributes.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-ii — the upper-bound funnel. The two key
// formats express "everything under this prefix" in opposite ways, and this is
// the one place that knows which:
//
//   - blob (desc == nil): a page key is the concatenation of every column's
//     encoding, and `CompareKeys` is `bytes.Compare`, so a leading-column key
//     compares BELOW every member of its own group. The bound has to be faked
//     upwards with bytes — `appendCompositeUpperPadding`'s 64 trailing 0xFF
//     (M0053-0001). Nothing about that is a valid key; it is a byte string
//     chosen to sort above any plausible suffix encoding.
//   - tuple (desc != nil): the key IS the prefix, unchanged. 0xFF padding is
//     not available (an 0xFF run is a malformed attribute image, not a large
//     one, and upstream never invents a maximal key either — `_bt_check_compare`
//     stops when the compared ATTRIBUTES exceed the bound). Instead slice
//     3b-2c-ii-B2-c-i taught the scan's high-end test to read a truncated bound
//     as PLUS infinity beyond the attributes it names (`indexFormat.compareHigh`),
//     which is exactly what a prefix pivot means. A bound that happens to name
//     every key attribute is unaffected: `compareHigh` then agrees with
//     `compare` attribute for attribute, heap-TID tiebreak included.
//
// So under the tuple format this is a no-op, and the callers' `len(Columns) > 1`
// guard becomes merely a cheap skip rather than a correctness condition —
// widening is a blob-format repair, not a scan requirement.
//
// The returned slice is caller-owned in the blob case and ALIASES key in the
// tuple case; callers only read it (they hand it straight to a scan).
func (ctx *Context) compositeUpperBound(idx *catalog.Index, key []byte) []byte {
	if ctx.pgIndexKeyDesc(idx) != nil {
		return key
	}
	return appendCompositeUpperPadding(key)
}

// indexProbeKeyPart is one attribute of a scan's SEARCH key: the datum, the
// table column backing the index key attribute it fills, and the position of
// the expression it came from (error reporting only — the two formats report
// an unencodable datum at the same place the open-coded sites did).
type indexProbeKeyPart struct {
	col *catalog.Column
	val Datum
	pos int
}

// indexProbeKey builds the search key a scan positions with: an equality probe,
// or one end of a range, over the FIRST len(parts) key attributes of idx.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-iii — the probe-key funnel, the low-end twin
// of B2-c-ii's `compositeUpperBound`. Six scan sites (index scan equality +
// range, index-only scan equality + range, bitmap index scan, the storage
// UPDATE-by-index path) each built this key by calling
// `encodeBTreeKeyForColumn` per attribute and CONCATENATING the results. That
// concatenation is not a detail of the encoder, it is the blob format's whole
// key layout asserted at ten call sites:
//
//   - blob (desc == nil): a page key is exactly that concatenation compared
//     with `bytes.Compare`, so a probe key is built the same way and a
//     leading-attribute probe is a byte prefix. Unchanged here, byte for byte.
//   - tuple (desc != nil): a page key is one `FormPGIndexTuple` image with a
//     null bitmap, per-attribute alignment padding and a heap TID; the
//     concatenation of per-column blobs is not a smaller version of it, it is
//     not one at all. `pgIndexTupleKey` builds the image, and a probe naming
//     fewer than every key attribute becomes a PIVOT stamped with its own
//     attribute count — minus infinity beyond what it names, which is the
//     "position at the first entry matching this prefix" semantics the blob
//     path got for free from a bytewise prefix. The zero ItemPointer is the
//     heapkeyspace tiebreaker's minus infinity, so an equality probe still
//     lands before every real entry sharing its key attributes.
//
// Under the tuple format there is no fallback: the tree IS tuple-shaped, so a
// refusal by `pgIndexTupleKey` (an unresolved TOAST pointer, an over-size key)
// has to surface as an error rather than quietly emit a blob key that would
// scan the wrong range. Indexes `buildPGIndexKeyDesc` refuses never get here —
// they resolve to desc == nil and keep the blob path whole, which is the same
// per-index dual-format property B2-c-ii asserted for the upper bound.
func (ctx *Context) indexProbeKey(idx *catalog.Index, parts []indexProbeKeyPart) ([]byte, *ExecError) {
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		var probe []byte
		for _, p := range parts {
			segment, encErr := encodeBTreeKeyForColumn(p.val, p.col, p.pos)
			if encErr != nil {
				return nil, encErr
			}
			probe = append(probe, segment...)
		}
		return probe, nil
	}

	pos := 0
	if len(parts) > 0 {
		pos = parts[0].pos
	}
	// pgIndexKeyColumns and buildPGIndexKeyDesc refuse the same indexes, so a
	// non-nil descriptor guarantees resolvable key columns; taking them from
	// here rather than from the caller keeps the datums aligned with the
	// descriptor attributes even if a site looked its column up differently.
	keyCols := pgIndexKeyColumns(idx)
	if len(parts) > len(keyCols) {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexProbeKey: %d search-key attributes for index %q with %d key columns", len(parts), idx.Name, len(keyCols))}
	}
	cols := make([]*catalog.Column, len(parts))
	vals := make([]Datum, len(parts))
	for i, p := range parts {
		// A probe MUST name a leading prefix of the key. The blob path
		// tolerated a site that skipped or reordered attributes (it just
		// produced a key that matched nothing); a pivot silently means
		// something else — "the first i attributes" — so name the mismatch.
		if p.col == nil || keyCols[i] == nil || p.col.Name != keyCols[i].Name {
			return nil, &ExecError{Code: "XX000", Pos: p.pos, Message: fmt.Sprintf(
				"indexProbeKey: search-key attribute %d is column %q, index %q key attribute %d is %q",
				i+1, columnNameOrEmpty(p.col), idx.Name, i+1, columnNameOrEmpty(keyCols[i]))}
		}
		cols[i] = keyCols[i]
		vals[i] = p.val
	}
	key, _, err := pgIndexTupleKey(desc, cols, vals, storage.ItemPointer{})
	if err != nil {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexProbeKey: index %q: %v", idx.Name, err)}
	}
	return key, nil
}

// indexEntryKey builds the key an index ENTRY is stored under: every key
// attribute of idx, taken from a heap row that lives at tid.
//
// indexRowProbeKey builds the key a probe POSITIONS with for the same row and
// the same index: every key attribute, no heap TID.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-iv — the row-key funnels, the writer-side
// counterpart of B2-c-iii's `indexProbeKey` (which funnels a scan's search key,
// built from EXPRESSIONS rather than from a row). Both delegate to the same
// `indexRowKey`, and under the blob format they are byte-identical — which is
// why one function, `encodeIndexKeyFromCols`, has served both roles since
// M0100-0005. That is the thing this slice exists to end:
//
//   - blob (desc == nil): a key is the concatenation of each key column's
//     `encodeBTreeKeyForColumn` image and carries no heap TID at all. The
//     entry's TID travels beside the key, in the btree item. Stored key and
//     probe key are therefore the same bytes, and asking for one when you meant
//     the other is undetectable.
//   - tuple (desc != nil): the heap TID is INSIDE the image (`t_tid`) and is the
//     final tiebreaker of the heapkeyspace ordering. A stored entry carries its
//     row's real TID, so duplicates of one key sort by physical position exactly
//     as `_bt_compare` orders them; a search key carries the zero TID, which is
//     minus infinity in that position, so a probe lands BEFORE every real entry
//     sharing its key attributes rather than in the middle of them. Handing a
//     probe an entry key would start a duplicate scan after some of its own
//     matches — a silent under-read, not a failure.
//
// Neither is a total replacement for `encodeIndexKeyFromCols`: two callers use
// that encoder for something that is not a tree key at all
// (`indexKeyColumnsChanged` compares two rows' key VALUES with bytes.Equal, and
// `ssiRecordHashIndexInsert` hashes the key into a bucket page tag). Both would
// break if a heap TID entered the bytes — equal keys from different rows would
// stop comparing equal, and a bucket tag would stop matching the reader's. They
// are value fingerprints, they now go through `indexKeyFingerprint`
// (pgindex_fingerprint.go, slice B2-c-viii, which also names the per-column
// `indexColumnFingerprint` the NULLS NOT DISTINCT heap scan needs), and the flip
// must leave all of them on a TID-free encoding; see the ledger rows for
// B2-c-iv and B2-c-viii.
//
// A nil key with a nil error keeps `encodeIndexKeyFromCols`'s meaning: this row
// has no entry in this index (NULL key column, or an expression key this
// projection cannot make). Under the tuple format a refusal by `pgIndexTupleKey`
// is an ERROR rather than a fallback, for the reason B2-c-iii gives: the tree is
// tuple-shaped, so emitting a blob key would address the wrong place.
func (ctx *Context) indexEntryKey(idx *catalog.Index, cols []catalog.Column, row Row, tid storage.ItemPointer) ([]byte, error) {
	return ctx.indexRowKey(idx, cols, row, tid)
}

// indexRowProbeKey — see indexEntryKey.
func (ctx *Context) indexRowProbeKey(idx *catalog.Index, cols []catalog.Column, row Row) ([]byte, error) {
	return ctx.indexRowKey(idx, cols, row, storage.ItemPointer{})
}

func (ctx *Context) indexRowKey(idx *catalog.Index, cols []catalog.Column, row Row, tid storage.ItemPointer) ([]byte, error) {
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		return encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
	}
	keyCols, vals, ok := indexRowKeyValues(idx, cols, row, ctx.Catalog)
	if !ok {
		return nil, nil
	}
	// buildPGIndexKeyDesc walks the same idx.Columns, so a non-nil descriptor
	// and a successful projection cannot disagree on the attribute count; the
	// check is here because a mismatch would silently make a full-key write a
	// PIVOT (minus infinity beyond what it names), which no reader could tell
	// from a legitimately truncated one.
	if len(vals) != desc.NKeyAtts() {
		return nil, fmt.Errorf("indexRowKey: index %q: %d projected key attributes for a %d-attribute descriptor",
			idx.Name, len(vals), desc.NKeyAtts())
	}
	key, _, err := pgIndexTupleKey(desc, keyCols, vals, tid)
	if err != nil {
		return nil, fmt.Errorf("indexRowKey: index %q: %w", idx.Name, err)
	}
	return key, nil
}

// indexBuildEntryKey builds the key ONE bulk-build entry is stored under: every
// key attribute of idx, taken from a heap row that lives at tid.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-v — the build path's key. It is the third and
// last writer funnel (B2-c-iii funnelled scan search keys, B2-c-iv the
// row-shaped runtime writers), and the one the flip most depends on, for a
// reason the runtime path does not have: a bulk build SORTS its entries, and
// under the tuple format the heap TID is part of what it sorts by. An entry
// built without its TID is not merely missing a field — it is placed in the
// wrong position among its own duplicates, and every later `_bt_compare`
// descent that carries a real scantid then looks for it somewhere else.
//
// The two formats:
//
//   - blob (desc == nil): `encodeCompositeBTreeKeyWithExprs` verbatim — the
//     concatenation of each key column's `encodeBTreeKeyForColumn` image, TID
//     free, with the entry's TID travelling beside it in `btree.BulkEntry.Ptr`.
//   - tuple (desc != nil): one `FormPGIndexTuple` image carrying the row's REAL
//     heap TID, which is also `BulkEntry.Ptr` — the same fact in the two places
//     the two formats each need it.
//
// hasNullKey keeps `encodeCompositeBTreeKeyWithExprs`'s meaning in both: a NULL
// value key column means the row has no entry in this index, and the caller
// skips it (NULLS DISTINCT) or dedups it against other null-bearing rows
// (NULLS NOT DISTINCT). That is a goopg divergence under the tuple format —
// PG's index tuples have a null bitmap and DO store NULL-keyed rows — but it is
// the pre-existing one, unchanged here and recorded in the deferral ledger;
// changing it is not a key-encoding question.
//
// keyExprs (an expression index's resolved key expressions) is only ever
// non-nil on the blob path: an expression key column has no
// `*catalog.Column`, so `pgIndexKeyColumns` returns nil for such an index and
// `buildPGIndexKeyDesc` refuses it. A descriptor arriving together with an
// unresolvable column is therefore a contradiction, and is reported rather than
// silently encoded as a shorter (pivot!) key.
func (ctx *Context) indexBuildEntryKey(idx *catalog.Index, cols []*catalog.Column, keyExprs []planner.Expr, row Row, tid storage.ItemPointer, pos int) (key []byte, hasNullKey bool, err *ExecError) {
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		return encodeCompositeBTreeKeyWithExprs(ctx, row, cols, keyExprs, pos)
	}
	keyCols := pgIndexKeyColumns(idx)
	if len(keyCols) != desc.NKeyAtts() || len(cols) != len(keyCols) {
		return nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexBuildEntryKey: index %q: %d build columns / %d resolved key columns / %d descriptor attributes",
			idx.Name, len(cols), len(keyCols), desc.NKeyAtts())}
	}
	vals := make([]Datum, len(keyCols))
	for i, col := range keyCols {
		// The build's own column list and the descriptor's must be the same
		// columns in the same order, or the key would describe a different
		// index than the tree it is inserted into.
		if cols[i] == nil || cols[i].Name != col.Name {
			return nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
				"indexBuildEntryKey: index %q: build key column %d is %q, index key attribute %d is %q",
				idx.Name, i+1, columnNameOrEmpty(cols[i]), i+1, col.Name)}
		}
		if col.Ordinal < 0 || col.Ordinal >= len(row) {
			return nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
				"indexBuildEntryKey: index %q: key column %q at ordinal %d is outside a %d-column row",
				idx.Name, col.Name, col.Ordinal, len(row))}
		}
		v := row[col.Ordinal]
		if v.IsNull() {
			return nil, true, nil
		}
		vals[i] = v
	}
	k, _, encErr := pgIndexTupleKey(desc, keyCols, vals, tid)
	if encErr != nil {
		// No blob fallback, for B2-c-iii's reason: the tree is tuple-shaped, so
		// a blob key would be filed in the wrong place rather than rejected.
		return nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexBuildEntryKey: index %q: %v", idx.Name, encErr)}
	}
	return k, false, nil
}

// arbiterProbeKey builds the key an ON CONFLICT arbiter probe POSITIONS with:
// the conflict-key columns of the proposed row, no heap TID.
//
// arbiterEntryKey builds the key the arbiter index ENTRY for the same row is
// stored under: the same key attributes, plus the heap TID the row was written
// at.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-vii — the arbiter-key funnel, and the last of
// the writer conflations the flip has to undo. It is B2-c-iv's split
// (`indexEntryKey` vs `indexRowProbeKey`) applied to the one index the upsert
// path does NOT maintain through those funnels: the arbiter, whose key
// `encodeArbiterKey` builds once and whose bytes are then used for BOTH roles —
// probed against the tree (`probeArbiterByKey`, `findInProgressConflictKey`) and
// inserted into it (`maintainArbiter`). Under the blob format that is sound, and
// deliberately so: a blob key carries no TID, so the probe key and the entry key
// ARE the same bytes, and reusing them is what keeps a side-effectful arbiter
// expression (`blurt_and_lock_*`) from being evaluated twice. Under the tuple
// format they differ in the heapkeyspace tiebreaker — the entry carries the
// row's real TID, the probe the zero TID that is minus infinity — and using one
// for the other files the entry among the wrong duplicates, or starts a probe
// after some of its own matches.
//
// `oc.ArbiterColumns` is built in the arbiter INDEX's key order (planner.go's
// `resolveArbiterIndex` walks `idx.Columns`), which is what makes attribute i of
// this key attribute i of the descriptor. The ordinals index the table the
// upsert is running against, which need not be the table the index was resolved
// on (the partitioned path probes with the parent's row), so the two sides are
// reconciled by NAME rather than by ordinal, and a disagreement is reported
// instead of encoded — a key built from the wrong column is not a key that
// misses, it is a key that hits something else.
//
// A nil key with a nil error keeps `encodeArbiterKey`'s meaning: a NULL
// conflict-key column never conflicts, so there is no probe and no entry.
// Expression arbiter columns (`ArbiterColumns[i] == -1`) only ever appear on
// indexes `buildPGIndexKeyDesc` refuses, which resolve to a nil descriptor and
// keep the blob path whole.
func (ctx *Context) arbiterProbeKey(oc *planner.OnConflictPlan, tbl *catalog.Table, row Row, pos int) ([]byte, error) {
	return ctx.arbiterKey(oc, tbl, row, storage.ItemPointer{}, pos)
}

// arbiterEntryKey — see arbiterProbeKey.
func (ctx *Context) arbiterEntryKey(oc *planner.OnConflictPlan, tbl *catalog.Table, row Row, tid storage.ItemPointer, pos int) ([]byte, error) {
	return ctx.arbiterKey(oc, tbl, row, tid, pos)
}

func (ctx *Context) arbiterKey(oc *planner.OnConflictPlan, tbl *catalog.Table, row Row, tid storage.ItemPointer, pos int) ([]byte, error) {
	if oc == nil || oc.ArbiterIndex == nil || len(oc.ArbiterColumns) == 0 {
		return nil, nil
	}
	idx := oc.ArbiterIndex
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		return encodeArbiterKey(ctx, oc, tbl, row, pos)
	}
	if tbl == nil {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"arbiterKey: index %q: no target table", idx.Name)}
	}
	keyCols := pgIndexKeyColumns(idx)
	if len(keyCols) != desc.NKeyAtts() || len(oc.ArbiterColumns) != len(keyCols) {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"arbiterKey: index %q: %d arbiter columns / %d resolved key columns / %d descriptor attributes",
			idx.Name, len(oc.ArbiterColumns), len(keyCols), desc.NKeyAtts())}
	}
	vals := make([]Datum, len(keyCols))
	for i, ord := range oc.ArbiterColumns {
		if ord < 0 || ord >= len(tbl.Columns) || ord >= len(row) || keyCols[i] == nil {
			return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
				"arbiterKey: index %q: arbiter column %d has ordinal %d on a %d-column table / %d-column row",
				idx.Name, i+1, ord, len(tbl.Columns), len(row))}
		}
		if !strings.EqualFold(tbl.Columns[ord].Name, keyCols[i].Name) {
			return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
				"arbiterKey: index %q: arbiter column %d is %q, index key attribute %d is %q",
				idx.Name, i+1, tbl.Columns[ord].Name, i+1, keyCols[i].Name)}
		}
		v := row[ord]
		if v.IsNull() {
			return nil, nil
		}
		vals[i] = v
	}
	// keyCols, not tbl's columns: the descriptor was derived from these, and the
	// per-attribute physical layout has to come from the same catalog.Column the
	// comparator was chosen for (pgindex_keydesc.go's findColumnByName note).
	key, _, err := pgIndexTupleKey(desc, keyCols, vals, tid)
	if err != nil {
		// No blob fallback, for B2-c-iii's reason: the tree is tuple-shaped, so a
		// blob key would address the wrong place rather than be rejected.
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"arbiterKey: index %q: %v", idx.Name, err)}
	}
	return key, nil
}

// sortBuildEntriesFindDuplicate sorts entries into idx's OWN key order and
// reports the first adjacent pair that duplicates a key — the unique-index
// build's 23505 test.
//
// M0130-S11.4 slice 3b-2c-ii-B2-c-v. Both halves were format assertions the
// blob key made invisible:
//
//   - the ORDER. Sorting by `string(key)` is the bytewise order, which is the
//     blob encoding's whole design and is meaningless for a tuple image (a real
//     PG datum is not order-preserving under bytes.Compare for any type but
//     bytea/text). It now goes through the same comparison the tree itself
//     uses, so "adjacent" means the same thing to the check as to the build.
//   - the EQUALITY. `bytes.Equal` on a tuple key can never report a duplicate:
//     the heap TID is inside the image and is distinct by construction, so a
//     unique build over genuinely duplicated data would succeed and produce a
//     unique index containing duplicates. The test therefore asks
//     `ComparePGIndexTupleKeyAttrs` — key attributes only, no TID tiebreak,
//     which is exactly where upstream raises the error
//     (`comparetup_index_btree`, tuplesortvariants.c:1668, before its
//     ItemPointer tiebreak).
//
// For the blob format both reduce to what M0055-0006 Phase E shipped, byte for
// byte: `CompareKeys` IS bytes.Compare, and equality under it IS bytesEqual.
func sortBuildEntriesFindDuplicate(desc *btree.PGIndexKeyDesc, entries []btree.BulkEntry) bool {
	cmp := func(a, b []byte) int { return btree.CompareKeys(a, b) }
	dup := func(a, b []byte) bool { return btree.CompareKeys(a, b) == 0 }
	if desc != nil {
		cmp = func(a, b []byte) int {
			res, err := btree.ComparePGIndexTuples(desc, a, b)
			if err != nil {
				// Same total-order fallback indexFormat.compare makes: a sort
				// predicate has nowhere to put an error, and an intransitive
				// one is worse than a wrong one.
				return btree.CompareKeys(a, b)
			}
			return res
		}
		dup = func(a, b []byte) bool {
			res, err := btree.ComparePGIndexTupleKeyAttrs(desc, a, b)
			if err != nil {
				return false
			}
			return res == 0
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return cmp(entries[i].Key, entries[j].Key) < 0 })
	for i := 1; i < len(entries); i++ {
		if dup(entries[i].Key, entries[i-1].Key) {
			return true
		}
	}
	return false
}

func columnNameOrEmpty(c *catalog.Column) string {
	if c == nil {
		return ""
	}
	return c.Name
}

// indexBTreeOptions is the Options every index btree in the engine is
// opened/created with: the pool's split-WAL hook (what plain btree.Open
// supplies) plus this index's key descriptor.
func indexBTreeOptions(ctx *Context, idx *catalog.Index) btree.Options {
	opts := btree.Options{KeyDesc: ctx.pgIndexKeyDesc(idx)}
	if ctx.Pool != nil {
		// btree.Open takes the same hook from the pool; asking a nil pool for
		// it dereferences (storage.(*Pool).LogBtreeSplit). The open itself
		// fails right after on a nil pool, so guarding here only keeps the
		// options constructor callable on its own — which is what lets the
		// descriptor wiring be tested without a buffer pool.
		opts.LogSplit = btree.PoolLogSplit(ctx.Pool)
	}
	return opts
}

// openIndexBTree opens the btree backing a catalog index. It replaces the
// engine's direct `btree.Open(ctx.Pool, idxRel)` calls: idxRel alone cannot
// name a descriptor, idx can.
//
// idxRel is passed separately rather than re-derived from idx because several
// callers open a relfilenode that is NOT ctx.Catalog.IndexRelFileNode(idx) —
// REINDEX CONCURRENTLY builds into a shadow relfile that carries the same key
// shape as the index it will replace.
func openIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode) (*btree.BTree, error) {
	return btree.OpenWithOptions(ctx.Pool, idxRel, indexBTreeOptions(ctx, idx))
}

// createIndexBTree initialises an empty btree for a catalog index, stamping
// the creating xid onto the block-0 smgr-create record (what
// btree.CreateWithXID does) and installing the key descriptor.
func createIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode) (*btree.BTree, error) {
	opts := indexBTreeOptions(ctx, idx)
	opts.CreateXID = ctx.Tx.XID
	return btree.CreateWithOptions(ctx.Pool, idxRel, opts)
}

// bulkCreateIndexBTree sort-builds a btree for a catalog index (CREATE INDEX /
// REINDEX), installing the key descriptor so the build's own sort and the
// later readers agree on the ordering — the one place where disagreeing would
// produce a tree that is wrong from birth rather than wrong on the next write.
func bulkCreateIndexBTree(ctx *Context, idx *catalog.Index, idxRel storage.RelFileNode, entries []btree.BulkEntry) (*btree.BTree, error) {
	opts := indexBTreeOptions(ctx, idx)
	opts.CreateXID = ctx.Tx.XID
	return btree.BulkCreateWithOptions(ctx.Pool, idxRel, entries, opts)
}
