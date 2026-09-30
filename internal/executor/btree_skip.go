package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// btreeSkipEnum is the btree skip-scan enumeration (M0146-0005v, PG18's
// `_bt_skiparray`, nbtpreprocesskeys.c) shared by the heap-fetching index
// scan and — since M0146-0005bt — the index-only scan: an equality probe
// whose keys bind columns [skipPrefix, skipPrefix+len(keys)) while the
// leading skipPrefix columns carry no qual, run as an outer loop over the
// distinct values the index stores in the skipped prefix. One enumeration
// descent lands on each prefix group's first entry; the group's own probe
// is that prefix joined with the bound columns. The two operators drive it
// differently (the index scan lazily per cursor, the index-only scan
// eagerly per Rescan) but must enumerate the same groups, so the
// enumeration lives here once.
type btreeSkipEnum struct {
	rest      []indexProbeKeyPart // evaluated bound-column parts (columns skipPrefix..), once per Rescan
	keyCols   []*catalog.Column   // resolved index key columns (pgIndexKeyColumns), tuple AND blob prefix decode
	desc      *nbtree.PGIndexKeyDesc
	restBytes []byte // blob format only: encoded suffix bound, appended behind each group's prefix
	seek      []byte // lo bound for the next group's first entry (nil = index start)
	seekExcl  bool   // seek is exclusive (tuple pivots; blob successors are inclusive)
	done      bool   // enumeration exhausted (or impossible — e.g. a NULL bound)
}

// init evaluates the bound columns ONCE per Rescan — the same eval timing
// lookupKeys applies — and prepares the enumeration. empty reports a NULL
// bound: `col = NULL` matches nothing in any prefix group, so the caller's
// whole scan is empty.
func (e *btreeSkipEnum) init(ctx *Context, idx *catalog.Index, skipPrefix int, keys []optimizer.Expr,
	outerSlot SlotView, pos int, op string) (empty bool, err error) {
	*e = btreeSkipEnum{}
	ncols := len(idx.Columns)
	e.keyCols = pgIndexKeyColumns(idx)
	if len(e.keyCols) != ncols {
		return false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"%s: skip scan on index %q cannot resolve its %d key columns (expression index?)",
			op, idx.Name, ncols)}
	}
	e.rest = make([]indexProbeKeyPart, 0, len(keys))
	for i, ke := range keys {
		v, err := evalExprSlot(ke, outerSlot, ctx)
		if err != nil {
			return false, err
		}
		if v.IsNull() {
			e.done = true
			return true, nil
		}
		e.rest = append(e.rest, indexProbeKeyPart{col: e.keyCols[skipPrefix+i], val: v, pos: ke.Pos()})
	}
	e.desc = ctx.pgIndexKeyDesc(idx)
	if e.desc == nil {
		// Blob format: pre-encode the suffix bound once; each group's lo is
		// (prefix bytes || suffix bytes). The skipped columns' decode is
		// decodeIndexKeyColumn walking the concatenated encoding — the same
		// walk the index-only scan's multi-column decode performs.
		var encErr *ExecError
		e.restBytes, encErr = ctx.indexProbeKey(idx, e.rest)
		if encErr != nil {
			return false, encErr
		}
	}
	return false, nil
}

// next advances the enumeration to the next distinct value of the skipped
// prefix and returns the bounded probe for that group as (lo, hi) — both
// inclusive — or ok=false when no further group exists.
//
// The enumeration is a pair of strictly-increasing descent positions:
//
//   - tuple format: the group's prefix is decoded back to datums
//     (pgIndexTupleKeyDatums), re-encoded as a pivot of skipPrefix
//     attributes, and the NEXT group's first entry is the first entry
//     strictly past that pivot — `compareHigh` treats a truncated bound
//     as plus infinity beyond its named attributes, so an exclusive lo
//     pivot skips every entry sharing the prefix (pgkeycmp.go).
//   - blob format: the prefix is the concatenated column encodings,
//     split by decodeIndexKeyColumn's consumed byte counts; the successor
//     is the byte-string increment of the prefix (order-preserving column
//     encodings are prefix-free, so no other group's prefix can extend it
//     — byteInc lands on or before the next group's first entry).
//
// The group's own probe is (prefix || bound columns) as one pivot of
// skipPrefix+len(keys) attributes for the tuple format — lo == hi, with
// the truncated-hi rule covering the unbound tail — or the byte
// concatenation plus the usual upper padding for the blob format.
func (e *btreeSkipEnum) next(ctx *Context, tree *nbtree.BTree, idx *catalog.Index, skipPrefix, pos int, op string) (lo, hi []byte, ok bool, err error) {
	s := skipPrefix
	for !e.done {
		var first []byte
		enum, cerr := tree.NewScanCursor(e.seek, nil, e.seekExcl, false, nil)
		if cerr != nil {
			return nil, nil, false, &ExecError{Code: "XX000", Pos: pos, Message: cerr.Error()}
		}
		for first == nil {
			more, nerr := enum.Next(func(key []byte, _ storage.ItemPointer, _ nbtree.ScanPos) (bool, error) {
				// The key aliases the pinned leaf; the probe bounds it
				// produces outlive the callback, so copy.
				first = append([]byte(nil), key...)
				return false, nil
			})
			if nerr != nil {
				return nil, nil, false, &ExecError{Code: "XX000", Pos: pos, Message: nerr.Error()}
			}
			if !more {
				break
			}
		}
		if first == nil {
			e.done = true
			return nil, nil, false, nil
		}
		if e.desc != nil {
			datums, derr := pgIndexTupleKeyDatums(e.desc, e.keyCols, first)
			if derr != nil {
				return nil, nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
					"%s: skip scan on index %q could not decode a prefix entry: %v", op, idx.Name, derr)}
			}
			parts := make([]indexProbeKeyPart, 0, s+len(e.rest))
			for i := 0; i < s; i++ {
				parts = append(parts, indexProbeKeyPart{col: e.keyCols[i], val: datums[i], pos: pos})
			}
			probe, encErr := ctx.indexProbeKey(idx, append(parts, e.rest...))
			if encErr != nil {
				return nil, nil, false, encErr
			}
			succ, encErr := ctx.indexProbeKey(idx, parts)
			if encErr != nil {
				return nil, nil, false, encErr
			}
			e.seek = succ
			e.seekExcl = true
			return probe, probe, true, nil
		}
		// Blob format: split the entry's leading skipPrefix column
		// encodings off the composite key.
		off := 0
		for i := 0; i < s; i++ {
			_, n, derr := decodeIndexKeyColumn(first[off:], *e.keyCols[i])
			if derr != nil {
				return nil, nil, false, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
					"%s: skip scan on index %q could not split a key prefix: %v", op, idx.Name, derr)}
			}
			off += n
		}
		prefix := first[:off]
		loKey := append(append([]byte(nil), prefix...), e.restBytes...)
		hiKey := loKey
		if s+len(e.rest) < len(idx.Columns) {
			hiKey = ctx.compositeUpperBound(idx, loKey)
		}
		// The next group's prefix is the byte-string increment of this
		// one. An all-0xFF prefix has no successor — and cannot be
		// extended by another group's prefix either (prefix-free
		// encodings), so the enumeration ends after this group.
		if succ := byteKeySuccessor(prefix); succ != nil {
			e.seek = succ
			e.seekExcl = false
		} else {
			e.done = true
		}
		return loKey, hiKey, true, nil
	}
	return nil, nil, false, nil
}
