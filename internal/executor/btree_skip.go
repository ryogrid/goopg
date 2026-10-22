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

	// vals is groupBounds' reused value buffer (prefix datums, then the
	// bound columns' values).
	vals []Datum
	// restImgs are the bound columns' attribute images, encoded once per
	// Rescan for groupBoundsRaw (nil when one cannot be, e.g. a TOAST
	// pointer); rawImgs / rawNulls are its reused per-group buffers.
	restImgs     [][]byte
	restImgsDone bool
	rawImgs      [][]byte
	rawNulls     []bool
	// probeTmpl / pivotTmpl are groupBoundsFixed's per-Rescan templates for
	// a skip prefix of tmplS attributes (nil when the probe is not all
	// fixed-width), and tmplOffs the prefix attributes' offsets in them.
	tmplDone  bool
	tmplS     int
	probeTmpl []byte
	pivotTmpl []byte
	tmplOffs  []int
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
	if probe, pivot, ok, rerr := e.groupBoundsRaw(idx, s, key, pos); rerr != nil {
		return nil, nil, rerr
	} else if ok {
		return probe, pivot, nil
	}
	datums, derr := pgIndexTupleKeyDatums(e.desc, e.keyCols, key)
	if derr != nil {
		return nil, nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"%s: skip scan on index %q could not decode a prefix entry: %v", op, idx.Name, derr)}
	}
	// The key images are formed straight from the enumeration's own
	// descriptor and key columns (M0122-0012 slice 49): they are what
	// ctx.indexProbeKey resolves on every call — pgIndexKeyDesc(idx) and
	// pgIndexKeyColumns(idx), the latter a by-name column lookup that
	// allocates — and a part list built from keyCols in order always passes
	// its prefix check. The bytes and the error text are indexProbeKey's.
	n := s + len(e.rest)
	e.vals = append(e.vals[:0], datums[:s]...)
	for _, r := range e.rest {
		e.vals = append(e.vals, r.val)
	}
	if probe, err = e.probeKey(idx, e.vals[:n], pos); err != nil {
		return nil, nil, err
	}
	if pivot, err = e.probeKey(idx, e.vals[:s], pos); err != nil {
		return nil, nil, err
	}
	return probe, pivot, nil
}

// groupBoundsRaw forms the group's probe and pivot from the entry's own
// attribute images (M0122-0012 slice 52), as PG's skip array takes the
// skipped attribute's datum straight from the index tuple
// (_bt_advance_array_keys, nbtutils.c) instead of decoding and re-encoding
// it. The prefix images are deformed once and handed to FormPGIndexTuple with
// the bound columns' images (encoded once per Rescan); FormPGIndexTuple
// re-forms an inline image into the bytes it forms from the decoded value
// encoded afresh (nbtree.PGIndexImageInline), so the keys are groupBounds'.
// ok is false — the caller takes the decoding path, which also reports the
// errors — for an entry that is not a full-arity leaf tuple, a prefix value
// stored compressed or out of line, or a bound value that is a TOAST pointer.
func (e *btreeSkipEnum) groupBoundsRaw(idx *catalog.Index, s int, key []byte, pos int) (probe, pivot []byte, ok bool, err error) {
	nkey := e.desc.NKeyAtts()
	phys := e.desc.Physical()
	n := s + len(e.rest)
	if n > nkey || len(key) < nbtree.SizeOfIndexTupleData || int(nbtree.BTreeTupleGetNAtts(key, uint16(nkey))) != nkey {
		return nil, nil, false, nil
	}
	if !e.restImgsDone {
		e.restImgsDone = true
		e.restImgs = make([][]byte, 0, len(e.rest))
		for i, r := range e.rest {
			if r.val.Kind == KindToastPointer {
				e.restImgs = nil
				break
			}
			img, encErr := encodeValuePG(e.keyCols[s+i].Type, r.val)
			if encErr != nil {
				e.restImgs = nil
				break
			}
			e.restImgs = append(e.restImgs, img)
		}
	}
	if len(e.restImgs) != len(e.rest) {
		return nil, nil, false, nil
	}
	if probe, pivot, fixed := e.groupBoundsFixed(idx, s, key, nkey, phys, pos); fixed {
		return probe, pivot, true, nil
	}
	imgs, nulls, derr := nbtree.DeformPGIndexTuple(key, phys, s)
	if derr != nil {
		return nil, nil, false, nil
	}
	for i := 0; i < s; i++ {
		if !nulls[i] && !nbtree.PGIndexImageInline(phys[i], imgs[i]) {
			return nil, nil, false, nil
		}
	}
	e.rawImgs = append(append(e.rawImgs[:0], imgs...), e.restImgs...)
	e.rawNulls = append(e.rawNulls[:0], nulls...)
	for range e.rest {
		e.rawNulls = append(e.rawNulls, false)
	}
	if probe, err = e.formKey(idx, phys[:n], e.rawImgs[:n], e.rawNulls[:n], nkey, pos); err != nil {
		return nil, nil, false, err
	}
	if pivot, err = e.formKey(idx, phys[:s], e.rawImgs[:s], e.rawNulls[:s], nkey, pos); err != nil {
		return nil, nil, false, err
	}
	return probe, pivot, true, nil
}

// groupBoundsFixed is groupBoundsRaw for a probe whose attributes are all
// fixed-width (M0122-0012 slice 56). FormPGIndexTuple lays such a key out
// with no null bitmap, every attribute at its fixed offset
// (nbtree.PGIndexFixedAttrOffsets) and nothing else value-dependent, so the
// probe and pivot are formed once per Rescan as templates — the bound
// columns' images in place, the prefix attributes zero — and each group
// copies the entry's prefix attribute bytes into a fresh copy of both. An
// entry with no null bitmap stores those attributes at the same offsets.
// The bytes are therefore groupBoundsRaw's (pinned), without its per-group
// deform and two generic tuple formations; PG's skip array likewise just
// takes the datum (_bt_advance_array_keys). ok is false — groupBoundsRaw's
// general path runs — for a variable-width probe attribute, an entry with a
// null bitmap or a t_info size short of the prefix, or a template that cannot
// be formed (whose error the general path reports).
func (e *btreeSkipEnum) groupBoundsFixed(idx *catalog.Index, s int, key []byte, nkey int, phys []nbtree.PGIndexAttr, pos int) (probe, pivot []byte, ok bool) {
	if s == 0 {
		return nil, nil, false
	}
	if !e.tmplDone || e.tmplS != s {
		e.tmplDone, e.tmplS = true, s
		e.probeTmpl, e.pivotTmpl, e.tmplOffs = nil, nil, nil
		n := s + len(e.rest)
		offs, fixed := nbtree.PGIndexFixedAttrOffsets(phys[:n])
		if !fixed {
			return nil, nil, false
		}
		imgs, nulls := make([][]byte, n), make([]bool, n)
		for i := 0; i < s; i++ {
			imgs[i] = make([]byte, phys[i].Len)
		}
		copy(imgs[s:], e.restImgs)
		pr, err := e.formKey(idx, phys[:n], imgs, nulls, nkey, pos)
		if err != nil {
			return nil, nil, false
		}
		pv, err := e.formKey(idx, phys[:s], imgs[:s], nulls[:s], nkey, pos)
		if err != nil {
			return nil, nil, false
		}
		e.probeTmpl, e.pivotTmpl, e.tmplOffs = pr, pv, offs[:s]
	}
	if e.probeTmpl == nil || nbtree.PGIndexTupleHasNulls(key) {
		return nil, nil, false
	}
	size := nbtree.PGIndexTupleSize(key)
	if size > len(key) || e.tmplOffs[s-1]+int(phys[s-1].Len) > size {
		return nil, nil, false
	}
	np := len(e.probeTmpl)
	buf := make([]byte, np+len(e.pivotTmpl))
	probe, pivot = buf[:np:np], buf[np:]
	copy(probe, e.probeTmpl)
	copy(pivot, e.pivotTmpl)
	for i, o := range e.tmplOffs {
		end := o + int(phys[i].Len)
		copy(probe[o:end], key[o:end])
		copy(pivot[o:end], key[o:end])
	}
	return probe, pivot, true
}

// formKey is pgIndexTupleKey's tail over ready attribute images: one
// FormPGIndexTuple with the zero heap TID, stamped as a pivot of its own
// attribute count when it names fewer than every key attribute, and checked
// against the leaf item-size limit — with indexProbeKey's error text.
func (e *btreeSkipEnum) formKey(idx *catalog.Index, phys []nbtree.PGIndexAttr, imgs [][]byte, nulls []bool, nkey, pos int) ([]byte, error) {
	raw, err := nbtree.FormPGIndexTuple(phys, imgs, nulls, storage.ItemPointer{})
	if err == nil && len(phys) < nkey {
		err = nbtree.BTreeTupleSetNAtts(raw, uint16(len(phys)), false)
	}
	if err == nil {
		err = nbtree.CheckPGBTItemSize(len(raw), true)
	}
	if err != nil {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexProbeKey: index %q: %v", idx.Name, err)}
	}
	return raw, nil
}

// probeKey is ctx.indexProbeKey's tuple-format arm for a part list naming
// the first len(vals) key columns: one pgIndexTupleKey image with the zero
// heap TID.
func (e *btreeSkipEnum) probeKey(idx *catalog.Index, vals []Datum, pos int) ([]byte, error) {
	key, _, err := pgIndexTupleKey(e.desc, e.keyCols[:len(vals)], vals, storage.ItemPointer{})
	if err != nil {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf(
			"indexProbeKey: index %q: %v", idx.Name, err)}
	}
	return key, nil
}

// canWalk reports whether the leaf walk applies: the tuple format, whose
// entries the walk can compare against a group probe. The blob format keeps
// the per-group descents of next.
func (e *btreeSkipEnum) canWalk() bool { return e.desc != nil }

// skipPos is one leaf entry's comparison with its group's probe
// (nbtree.BTree.CompareBoundAttrs, M0122-0012 slice 57). The probe is the
// pivot's attributes followed by the bound columns, so the one pass answers
// all three of walkLeaf's per-entry questions, each exactly as the bound
// comparison it replaces would: pastGroup is CompareHighBound(key, pivot) > 0,
// beforeProbe CompareLowBound(key, probe) < 0, pastProbe
// CompareHighBound(key, probe) > 0. Without a usable pass (ok false: no probe
// yet, a comparison error, an entry storing fewer attributes than the probe)
// and in the one case the attributes leave open — an entry equal to a
// full-arity probe, which only the heap-TID tiebreak orders — the
// per-bound call answers.
type skipPos struct {
	res, at, nKey, nProbe int
	ok                    bool
}

func probePos(tree *nbtree.BTree, key, probe []byte) skipPos {
	if probe == nil {
		return skipPos{}
	}
	res, at, nKey, nProbe, ok := tree.CompareBoundAttrs(key, probe)
	return skipPos{res: res, at: at, nKey: nKey, nProbe: nProbe, ok: ok && nProbe > 0 && nKey >= nProbe}
}

// pastGroup: the entry's first s attributes order after the pivot's — the
// probe's first s. Equal on them, compareHigh calls the entry inside the
// pivot (a bound naming a prefix is plus infinity beyond it).
func (p skipPos) pastGroup(tree *nbtree.BTree, key, pivot []byte, s int) bool {
	if p.ok && s <= p.nProbe {
		return p.at < s && p.res > 0
	}
	return tree.CompareHighBound(key, pivot) > 0
}

// beforeProbe: a differing attribute decides; equal on every probe attribute,
// a longer entry orders after the truncated probe (minus infinity beyond it).
func (p skipPos) beforeProbe(tree *nbtree.BTree, key, probe []byte) bool {
	if p.ok {
		if p.res != 0 {
			return p.res < 0
		}
		if p.nKey > p.nProbe {
			return false
		}
	}
	return tree.CompareLowBound(key, probe) < 0
}

// pastProbe: compareHigh is heap-TID blind, so equal attributes are inside.
func (p skipPos) pastProbe(tree *nbtree.BTree, key, probe []byte) bool {
	if p.ok {
		return p.res > 0
	}
	return tree.CompareHighBound(key, probe) > 0
}

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
	cur := e.walkCur
	// jump is the finaltup test's verdict (below): a re-descent target the
	// rest of this leaf cannot reach. checkedBefore/checkedPassed make it one
	// test per group state per leaf.
	var jump []byte
	jumpExcl, checkedBefore, checkedPassed := false, false, false
	ok, nerr := cur.Next(func(key []byte, ptr storage.ItemPointer, p nbtree.ScanPos) (bool, error) {
		entries++
		at := probePos(tree, key, e.gProbe)
		if e.gPivot == nil || at.pastGroup(tree, key, e.gPivot, s) {
			probe, pivot, gerr := e.groupBounds(ctx, idx, s, key, pos, op)
			if gerr != nil {
				return false, gerr
			}
			e.gProbe, e.gPivot = probe, pivot
			e.gReached, e.gPassed = false, false
			checkedBefore, checkedPassed = false, false
			newGroups++
			at = probePos(tree, key, e.gProbe)
		}
		if !e.gPassed && at.beforeProbe(tree, key, e.gProbe) {
			// Before the group's probe. If even the leaf's final entry is,
			// no entry left here reaches it: descend to the probe now, as
			// _bt_advance_array_keys starts a new primitive scan when the
			// page's finaltup is still before the advanced array keys.
			if !checkedBefore {
				checkedBefore = true
				if last, lok := cur.LeafLastKey(); lok && tree.CompareLowBound(last, e.gProbe) < 0 {
					jump, jumpExcl = e.gProbe, false
					return false, nil
				}
			}
			return true, nil
		}
		if !e.gPassed && at.pastProbe(tree, key, e.gProbe) {
			e.gPassed = true
		}
		if e.gPassed {
			// Past the probe, still inside its group. If the leaf's final
			// entry is too, the rest of the leaf is this group's tail:
			// descend past the group now instead of reading it.
			if !checkedPassed {
				checkedPassed = true
				if last, lok := cur.LeafLastKey(); lok && tree.CompareHighBound(last, e.gPivot) <= 0 {
					jump, jumpExcl = e.gPivot, true
					return false, nil
				}
			}
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
	if jump != nil {
		jc, cerr := tree.NewScanCursor(jump, nil, jumpExcl, false, leafFilter)
		if cerr != nil {
			return false, &ExecError{Code: "XX000", Pos: pos, Message: cerr.Error()}
		}
		e.walkCur = jc
		return true, nil
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
