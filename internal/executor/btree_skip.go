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

	// The leaf walk's state (walkLeaf, tuple format only): the cursor, and
	// the current prefix group's probe (prefix || bound columns) and pivot
	// (prefix alone), with whether the walk has reached and passed the
	// probe's range inside that group.
	walkCur  *nbtree.ScanCursor
	gProbe   []byte
	gPivot   []byte
	gReached bool
	gPassed  bool
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
			probe, succ, gerr := e.groupBounds(ctx, idx, s, first, pos, op)
			if gerr != nil {
				return nil, nil, false, gerr
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

// groupBounds builds, from one entry of a prefix group (tuple format), the
// group's probe — the entry's skipPrefix attributes joined with the bound
// columns, one pivot whose range [probe, probe] is the group's matches — and
// its pivot, the prefix alone: an entry is still in the group exactly while
// CompareHighBound(entry, pivot) <= 0, and the next group starts strictly past it.
func (e *btreeSkipEnum) groupBounds(ctx *Context, idx *catalog.Index, s int, key []byte, pos int, op string) (probe, pivot []byte, err error) {
	datums, derr := pgIndexTupleKeyDatums(e.desc, e.keyCols, key)
	if derr != nil {
		return nil, nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"%s: skip scan on index %q could not decode a prefix entry: %v", op, idx.Name, derr)}
	}
	parts := make([]indexProbeKeyPart, 0, s+len(e.rest))
	for i := 0; i < s; i++ {
		parts = append(parts, indexProbeKeyPart{col: e.keyCols[i], val: datums[i], pos: pos})
	}
	probe, encErr := ctx.indexProbeKey(idx, append(parts, e.rest...))
	if encErr != nil {
		return nil, nil, encErr
	}
	pivot, encErr = ctx.indexProbeKey(idx, parts)
	if encErr != nil {
		return nil, nil, encErr
	}
	return probe, pivot, nil
}

// canWalk reports whether the leaf walk applies: the tuple format, whose
// entries the walk can compare against a group probe. The blob format keeps
// the per-group descents of next.
func (e *btreeSkipEnum) canWalk() bool { return e.desc != nil }

// walkLeaf is the skip scan as PG 18 runs it (M0145-0008af): one leaf page
// per call, every entry checked against the current prefix group's probe,
// as `_bt_checkkeys` checks a skip array advanced to the entry's prefix
// (`_bt_advance_array_keys`, nbtutils.c). Matching entries go to emit in
// index order; emit returning false ends the walk.
//
// The per-group descent of next pays two root-to-leaf descents for every
// distinct prefix value — for web_returns_pkey (wr_item_sk, wr_order_number)
// probed on wr_order_number, 10619 prefixes over 17920 entries, that is
// 226 ms where PG's scan reports one Index Search and takes 1 ms. PG starts a
// new primitive index scan only when the next match lies beyond the current
// page; the walk re-descends under the same condition, read off what one leaf
// showed: when the whole leaf belonged to one group (no new group started on
// it), the group may run on for many pages, so the walk descends straight to
// the group's probe (not reached yet) or past the group (already passed)
// instead of reading the group's remaining leaves. Otherwise it steps to the
// sibling leaf. Dense prefixes therefore cost one sequential pass, sparse
// ones a descent per group — the two regimes PG's runtime choice spans.
//
// more is false once the walk is exhausted (or emit stopped it).
func (e *btreeSkipEnum) walkLeaf(ctx *Context, tree *nbtree.BTree, idx *catalog.Index, s, pos int, op string,
	leafFilter func(storage.BlockNumber) bool, emit func(key []byte, ptr storage.ItemPointer, p nbtree.ScanPos) (bool, error)) (more bool, err error) {
	if e.done {
		return false, nil
	}
	if e.walkCur == nil {
		cur, cerr := tree.NewScanCursor(e.seek, nil, e.seekExcl, false, leafFilter)
		if cerr != nil {
			return false, &ExecError{Code: "XX000", Pos: pos, Message: cerr.Error()}
		}
		e.walkCur = cur
	}
	entries, newGroups := 0, 0
	stopped := false
	ok, nerr := e.walkCur.Next(func(key []byte, ptr storage.ItemPointer, p nbtree.ScanPos) (bool, error) {
		entries++
		if e.gPivot == nil || tree.CompareHighBound(key, e.gPivot) > 0 {
			probe, pivot, gerr := e.groupBounds(ctx, idx, s, key, pos, op)
			if gerr != nil {
				return false, gerr
			}
			e.gProbe, e.gPivot = probe, pivot
			e.gReached, e.gPassed = false, false
			newGroups++
		}
		if e.gPassed || tree.CompareLowBound(key, e.gProbe) < 0 {
			return true, nil
		}
		if tree.CompareHighBound(key, e.gProbe) > 0 {
			e.gPassed = true
			return true, nil
		}
		e.gReached = true
		cont, eerr := emit(key, ptr, p)
		if eerr != nil {
			return false, eerr
		}
		if !cont {
			stopped = true
			return false, nil
		}
		return true, nil
	})
	if nerr != nil {
		if ee, isExec := nerr.(*ExecError); isExec {
			return false, ee
		}
		return false, &ExecError{Code: "XX000", Pos: pos, Message: nerr.Error()}
	}
	if stopped || !ok {
		e.done = true
		e.walkCur = nil
		return false, nil
	}
	if entries > 0 && newGroups == 0 && e.gPivot != nil {
		// The whole leaf was one group: jump instead of walking it.
		var target []byte
		excl := false
		if e.gPassed {
			target, excl = e.gPivot, true
		} else if !e.gReached {
			target = e.gProbe
		}
		if target != nil {
			cur, cerr := tree.NewScanCursor(target, nil, excl, false, leafFilter)
			if cerr != nil {
				return false, &ExecError{Code: "XX000", Pos: pos, Message: cerr.Error()}
			}
			e.walkCur = cur
		}
	}
	return true, nil
}
