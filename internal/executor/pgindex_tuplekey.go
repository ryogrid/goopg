package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-B2-a — the key ENCODER, still unwired.
//
// B1 made `indexFormat` own both halves of the on-page contract: the ordering
// (`ComparePGIndexTuples` under a `PGIndexKeyDesc`) and the layout (what
// `item.key` physically IS — an opaque payload, or the whole
// `FormPGIndexTuple` image). What no layer can produce yet is that image from
// a ROW: `buildPGIndexKeyDesc` says how the key attributes are laid out and
// ordered, but the three writers (`encodeCompositeBTreeKey`,
// `encodeIndexKeyFromCols`, `encodeArbiterKey`) still concatenate goopg's
// order-preserving per-column blobs. This file is the missing converter, and
// only the converter — nothing calls it on a production path yet, so nothing
// on disk moves and no REINDEX is required. Wiring the three writers to it
// (and turning `pgIndexTupleKeys` on) is B2-c.
//
// WHY IT IS SPLIT OUT. The flip has to be atomic — a tree whose writer and
// comparer disagree mis-ORDERS rather than failing to parse — so the flip
// commit is the one that cannot be verified incrementally. Everything that can
// be proven ahead of it should be: "does goopg's datum image, laid out by
// FormPGIndexTuple under the descriptor buildPGIndexKeyDesc derives, sort in
// the order the 3b-2a comparators promise, for every type the descriptor
// accepts?" is exactly that question, and it is answerable today against a
// tree whose behaviour has not moved (pgindex_tuplekey_test.go answers it as a
// per-type ordering table).
//
// THE IMAGE IS THE HEAP'S IMAGE. The per-attribute bytes come from
// `encodeValuePG` — the same function that fills a heap tuple's data area —
// rather than from a new index-only encoder. That is not convenience: an index
// datum and a heap datum of the same type are the SAME physical image in
// PostgreSQL (index_form_tuple and heap_form_tuple share heap_fill_tuple), and
// the 3b-2a comparators were written against that image (see
// pgcompare_types.go's ENDIANNESS note, which cites encodeValuePG by name).
// Giving the index its own encoder would create precisely the sibling-pair
// divergence the ledger keeps recording.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// pgIndexTupleKey builds the nbtree IndexTupleData image for a key whose
// attributes are the FIRST len(vals) key attributes of desc.
//
// A caller supplying every attribute (len(vals) == desc.NKeyAtts()) gets a
// plain data tuple: t_tid is tid, and BTreeTupleGetNAtts reports the index's
// full key count implicitly.
//
// A caller supplying FEWER — a scan positioning on a prefix of a composite
// index — gets a tuple stamped as a pivot carrying its own attribute count, via
// BTreeTupleSetNAtts. That stamp is not optional bookkeeping: without
// INDEX_ALT_TID_MASK, BTreeTupleGetNAtts would report the index's full key
// count for a tuple that physically holds fewer attributes, and
// DeformPGIndexTuple would then walk off the end of the data area reading
// attributes that are not there. With it, `ComparePGIndexTuples` applies the
// truncation rule — equal on the attributes both sides store, and the shorter
// side is MINUS INFINITY beyond them — which is exactly the "position at the
// first entry matching this prefix" semantics the blob path got for free from a
// bytewise prefix comparison.
//
// tid is the heap TID for a data tuple. A search key passes an invalid/zero TID
// so that it sorts before every real entry with the same key attributes
// (heapkeyspace's final tiebreaker; block 0 / offset 0 is below any real TID) —
// again matching what the blob path did by having no TID at all.
//
// hasNull reports that at least one key attribute was NULL. The tuple format
// can represent NULLs (there is a null bitmap, and PGKeyAttr.compareAttr
// implements NULLS FIRST/LAST per attribute), which the blob format could not;
// but whether a NULL-keyed row becomes an index entry is a SEMANTIC change
// (unique-index NULLS DISTINCT, index-only scans, the planner's null
// handling), not a format change, so this reports the fact and leaves today's
// "a NULL key column means the row is not indexed" policy to the callers. See
// the deferral ledger row for M0130-S11.4 B2-a.
func pgIndexTupleKey(desc *btree.PGIndexKeyDesc, cols []*catalog.Column, vals []Datum, tid storage.ItemPointer) (key []byte, hasNull bool, err error) {
	nkey := desc.NKeyAtts()
	if nkey == 0 {
		return nil, false, fmt.Errorf("pgIndexTupleKey: nil or empty key descriptor")
	}
	if len(vals) == 0 || len(vals) > nkey {
		return nil, false, fmt.Errorf("pgIndexTupleKey: %d values for a %d-attribute key", len(vals), nkey)
	}
	if len(cols) != len(vals) {
		return nil, false, fmt.Errorf("pgIndexTupleKey: %d columns for %d values", len(cols), len(vals))
	}

	phys := desc.Physical()[:len(vals)]
	values := make([][]byte, len(vals))
	isnull := make([]bool, len(vals))
	for i, d := range vals {
		if d.IsNull() {
			isnull[i] = true
			hasNull = true
			continue
		}
		if cols[i] == nil {
			return nil, false, fmt.Errorf("pgIndexTupleKey: key attribute %d has no column", i+1)
		}
		if d.Kind == KindToastPointer {
			// PostgreSQL detoasts before index_form_tuple (the TOAST_INDEX_HACK
			// in indextuple.c); storing the pointer would index the reference
			// rather than the value, and the comparator would order 18-byte
			// pointers. Refusing keeps the caller on the blob path instead of
			// building a wrong tree.
			return nil, false, fmt.Errorf("pgIndexTupleKey: key attribute %d is an unresolved TOAST pointer", i+1)
		}
		img, encErr := encodeValuePG(cols[i].Type, d)
		if encErr != nil {
			return nil, false, fmt.Errorf("pgIndexTupleKey: key attribute %d (%s): %w", i+1, cols[i].Type.Name, encErr)
		}
		values[i] = img
	}

	raw, err := btree.FormPGIndexTuple(phys, values, isnull, tid)
	if err != nil {
		return nil, false, err
	}
	if len(vals) < nkey {
		// A prefix key is a pivot: it carries its own attribute count, and it
		// keeps no tiebreaker heap TID (heapTID=false), so it is minus infinity
		// in the TID position as well as in every attribute it dropped.
		if err := btree.BTreeTupleSetNAtts(raw, uint16(len(vals)), false); err != nil {
			return nil, false, err
		}
	}
	// BTMaxItemSize is the leaf-page limit upstream enforces in _bt_check_
	// natts' caller (nbtinsert.c's _bt_check_third_page). Reporting it here,
	// where the offending column is still nameable, beats a page-split failure.
	if err := btree.CheckPGBTItemSize(len(raw), true); err != nil {
		return nil, false, err
	}
	return raw, hasNull, nil
}

// pgIndexTupleKeyFromRow is pgIndexTupleKey for the writer path: it projects a
// heap row down to the index's key columns (by Ordinal, the same projection the
// blob encoders do) and stamps the row's heap TID.
//
// cols is parallel to desc.Attrs — one entry per KEY column, INCLUDE columns
// excluded, exactly as buildPGIndexKeyDesc produced them.
func pgIndexTupleKeyFromRow(desc *btree.PGIndexKeyDesc, cols []*catalog.Column, row Row, tid storage.ItemPointer) (key []byte, hasNull bool, err error) {
	vals := make([]Datum, len(cols))
	for i, col := range cols {
		if col == nil {
			return nil, false, fmt.Errorf("pgIndexTupleKeyFromRow: key attribute %d has no column", i+1)
		}
		if col.Ordinal < 0 || col.Ordinal >= len(row) {
			return nil, false, fmt.Errorf("pgIndexTupleKeyFromRow: key column %q ordinal %d out of range for a %d-column row",
				col.Name, col.Ordinal, len(row))
		}
		vals[i] = row[col.Ordinal]
	}
	return pgIndexTupleKey(desc, cols, vals, tid)
}

// pgIndexKeyColumns resolves the table columns backing idx's KEY columns, in
// key order, parallel to the descriptor buildPGIndexKeyDesc builds from the
// same index.
//
// It returns nil when any key column cannot be resolved — an expression key, or
// a name that is not on the table. Those are precisely the indexes
// buildPGIndexKeyDesc refuses, so "descriptor is non-nil" and "columns
// resolved" cannot disagree; returning nil rather than erroring keeps the two
// failure modes shaped the same for the caller (fall back to the blob path).
func pgIndexKeyColumns(idx *catalog.Index) []*catalog.Column {
	if idx == nil || idx.Table == nil || len(idx.Columns) == 0 {
		return nil
	}
	out := make([]*catalog.Column, 0, len(idx.Columns))
	for _, name := range idx.Columns {
		if name == "" {
			return nil
		}
		col := findColumnByName(idx.Table, name)
		if col == nil {
			return nil
		}
		out = append(out, col)
	}
	return out
}
