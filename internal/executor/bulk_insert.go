package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M0146-0009h — COPY FROM's bulk insert state and multi-insert page plan.
//
// PG's COPY keeps one BulkInsertState per target relation for the whole
// statement (copyfrom.c CopyMultiInsertBufferInit / GetBulkInsertState) and
// inserts through it. With a bistate, RelationGetBufferForTuple (hio.c) looks
// for room on the page it used last (current_buf), then on the pages the last
// bulk extension left unused (next_free .. last_free), then in the FSM, and
// only then extends — and RelationAddBlocks extends by the pages the caller
// asked for, raised to everything this bistate has extended by so far and
// capped at 64. The pages of the last extension that the load never reaches
// stay empty at the end of the relation, so a loaded PG table counts more
// relpages than it has data pages (TPC-DS SF0.25 `item`: 1238 + 46).
//
// The pages a multi-insert asks for are heap_multi_insert's estimate of what
// the rest of its batch needs (heap_multi_insert_pages), which is why COPY's
// flush prepares the whole batch before placing any of it.

// maxBuffersToExtendBy is hio.c's MAX_BUFFERS_TO_EXTEND_BY.
const maxBuffersToExtendBy = 64

// itemIDSize is sizeof(ItemIdData), one line pointer.
const itemIDSize = 4

// fsmCategoryStep is freespace.c's FSM_CAT_STEP: the FSM keeps free space in
// 32-byte categories, rounding a page's space down and a request up.
const fsmCategoryStep = 32

type bulkInsertState struct {
	rel storage.RelFileNode

	// current_buf: the page the last tuple went to.
	cur     storage.BlockNumber
	haveCur bool
	// next_free / last_free: pages of the last bulk extension not used yet.
	nextFree, lastFree storage.BlockNumber
	haveFree           bool
	// already_extended_by: every page this state has extended the relation by.
	alreadyExtendedBy int

	// The batch a multi-insert flush is placing; lens is nil outside one, and
	// then every request is for one page (heap_insert passes num_pages 0).
	lens          []int // t_len of each prepared tuple of the batch
	saveFree      int   // the fillfactor reserve (saveFreeSpace)
	idx           int   // the tuple being placed
	npages        int   // heap_multi_insert's npages
	npagesUsed    int   // and npages_used
	startingEmpty bool  // starting_with_empty_page
}

func newBulkInsertState(rel storage.RelFileNode) *bulkInsertState {
	return &bulkInsertState{rel: rel}
}

// beginBatch starts placing a multi-insert batch whose prepared tuples have
// the given lengths.
func (bs *bulkInsertState) beginBatch(lens []int, saveFree int) {
	bs.lens, bs.saveFree = lens, saveFree
	bs.idx, bs.npages, bs.npagesUsed, bs.startingEmpty = 0, 0, 0, false
}

func (bs *bulkInsertState) endBatch() { bs.lens = nil }

// heapMultiInsertPages is heap_multi_insert_pages (heapam.c): the pages the
// tuples need when packed in order, each page holding what fits after the
// fillfactor reserve.
func heapMultiInsertPages(lens []int, saveFree int) int {
	pageAvail := storage.BlockSize - storage.SizeOfPageHeaderData - saveFree
	npages := 1
	for _, l := range lens {
		tupSz := itemIDSize + (l+7)&^7 // MAXALIGN
		if pageAvail < tupSz {
			npages++
			pageAvail = storage.BlockSize - storage.SizeOfPageHeaderData - saveFree
		}
		pageAvail -= tupSz
	}
	return npages
}

// fsmRequest is the goopg FSM threshold that asks what PG's FSM search asks:
// a page whose heap free space, rounded down to a category, covers the
// request rounded up to one. goopg's FSM keeps exact pd_upper - pd_lower, one
// line pointer more than PageGetHeapFreeSpace.
func fsmRequest(targetFreeSpace int) uint16 {
	need := (targetFreeSpace + fsmCategoryStep - 1) / fsmCategoryStep * fsmCategoryStep
	return uint16(need + itemIDSize)
}

// place puts the prepared tuple whose targetFreeSpace is given on a page,
// through try (placeHeapTuple's tryAppendToBlock). wasEmpty reports whether
// the page try last appended to held no tuple before.
func (bs *bulkInsertState) place(ctx *Context, targetFreeSpace int,
	try func(storage.BlockNumber, int) (bool, error), wasEmpty func() bool) error {
	multi := bs.lens != nil
	skipCur := false
	if multi && bs.idx > 0 && bs.haveCur {
		// heap_multi_insert keeps filling the page the previous tuple went
		// to while the next one fits; only a tuple that does not fit asks
		// RelationGetBufferForTuple for a page.
		ok, err := try(bs.cur, targetFreeSpace)
		if err != nil {
			return err
		}
		if ok {
			bs.idx++
			return nil
		}
		skipCur = true
	}
	numPages := 1
	if multi {
		if bs.idx == 0 || !bs.startingEmpty {
			bs.npages = heapMultiInsertPages(bs.lens[bs.idx:], bs.saveFree)
			bs.npagesUsed = 0
		} else {
			bs.npagesUsed++
		}
		numPages = bs.npages - bs.npagesUsed
	}
	blk, err := bs.getBuffer(ctx, targetFreeSpace, numPages, try, skipCur)
	if err != nil {
		return err
	}
	bs.cur, bs.haveCur = blk, true
	if multi {
		bs.startingEmpty = wasEmpty()
		bs.idx++
	}
	return nil
}

// getBuffer is RelationGetBufferForTuple with a bistate (hio.c): the current
// page, the bulk extension's next free page, the FSM, then an extension.
// skipCur skips the first try of the current page when the caller has just
// found it full.
func (bs *bulkInsertState) getBuffer(ctx *Context, targetFreeSpace, numPages int,
	try func(storage.BlockNumber, int) (bool, error), skipCur bool) (storage.BlockNumber, error) {
	rel := bs.rel
	var target storage.BlockNumber
	have := false
	if bs.haveCur {
		target, have = bs.cur, true
	} else {
		// RelationGetTargetBlock: goopg caches no smgr_targblock, so a
		// fresh state starts from the FSM, then the last page (hio.c:576-597).
		skipCur = false
		if blk, ok := ctx.FSM.GetPageWithFreeSpace(rel, fsmRequest(targetFreeSpace)); ok {
			target, have = blk, true
		} else {
			n, err := ctx.Pool.NBlocks(rel)
			if err != nil {
				return 0, err
			}
			if n > 0 {
				target, have = n-1, true
			}
		}
	}
	for have {
		if !skipCur {
			ok, err := try(target, targetFreeSpace)
			if err != nil {
				return 0, err
			}
			if ok {
				return target, nil
			}
		}
		skipCur = false
		// try has recorded the page's remaining space in the FSM.
		if bs.haveFree {
			target = bs.nextFree
			if bs.nextFree >= bs.lastFree {
				bs.haveFree = false
			} else {
				bs.nextFree++
			}
			continue
		}
		target, have = ctx.FSM.GetPageWithFreeSpace(rel, fsmRequest(targetFreeSpace))
	}
	return bs.extend(ctx, numPages, try)
}

// extend is RelationAddBlocks with a bistate (hio.c): extend by the pages the
// caller needs, at least by as many as this state has extended by before, at
// most by 64; remember the extra pages as next_free and enter those beyond the
// caller's own need into the FSM. goopg counts no extension-lock waiters, so
// the waiter multiplier is that of a lone loader.
func (bs *bulkInsertState) extend(ctx *Context, numPages int,
	try func(storage.BlockNumber, int) (bool, error)) (storage.BlockNumber, error) {
	rel := bs.rel
	extendBy := max(numPages, bs.alreadyExtendedBy)
	extendBy = min(extendBy, maxBuffersToExtendBy)
	notInFSM := numPages

	unlock, _ := lockHeapExtend(rel, ctx.ProcNum)
	first, err := ctx.Pool.ExtendRelationBatchWithXID(rel, extendBy, ctx.Tx.XID)
	unlock()
	if err != nil {
		return 0, err
	}
	if ctx.FSM != nil {
		emptyFree := uint16(storage.BlockSize - storage.SizeOfPageHeaderData)
		for i := notInFSM; i < extendBy; i++ {
			ctx.FSM.RecordFreeSpace(rel, first+storage.BlockNumber(i), emptyFree)
		}
	}
	if extendBy > 1 {
		bs.nextFree, bs.lastFree, bs.haveFree = first+1, first+storage.BlockNumber(extendBy-1), true
	} else {
		bs.haveFree = false
	}
	bs.alreadyExtendedBy += extendBy
	ok, err := try(first, 0)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, &ExecError{Code: "XX000", Message: "freshly extended page did not accept tuple"}
	}
	return first, nil
}

// copyUsesMultiInsert is CopyFrom's CIM_MULTI test (copyfrom.c): no BEFORE
// or INSTEAD OF row insert trigger and no volatile default for a column the
// COPY does not supply — such a trigger or default may query the table, and
// must see every earlier row already there. nextval does not count
// (contain_volatile_functions_not_nextval).
func copyUsesMultiInsert(ctx *Context, tbl *catalog.Table, cols []catalog.Column, missing []bool) bool {
	if tbl == nil {
		return false
	}
	for _, tr := range tbl.Triggers {
		if !tr.ForEachRow || (tr.Timing != catalog.TriggerBefore && tr.Timing != catalog.TriggerInsteadOf) {
			continue
		}
		for _, ev := range tr.Events {
			if strings.EqualFold(ev, "insert") {
				return false
			}
		}
	}
	for i, m := range missing {
		if m && i < len(cols) && cols[i].DefaultExpr != nil && defaultExprVolatile(ctx, cols[i].DefaultExpr) {
			return false
		}
	}
	return true
}

// defaultExprVolatile reports a volatile function other than nextval in a
// column default's parse tree.
func defaultExprVolatile(ctx *Context, e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.FuncCall:
		name := strings.ToLower(x.Name.Name)
		if name != "nextval" {
			if volatileBuiltins[name] {
				return true
			}
			if ctx != nil && ctx.Catalog != nil {
				if rs := ctx.Catalog.Routines(); rs != nil {
					for _, r := range rs.LookupByName(parser.ObjectName{Name: name}) {
						if r.Volatile == "v" || r.Volatile == "" {
							return true
						}
					}
				}
			}
		}
		for _, a := range x.Args {
			if defaultExprVolatile(ctx, a) {
				return true
			}
		}
	case *parser.CastExpr:
		return defaultExprVolatile(ctx, x.Operand)
	case *parser.BinaryOp:
		return defaultExprVolatile(ctx, x.Left) || defaultExprVolatile(ctx, x.Right)
	case *parser.UnaryOp:
		return defaultExprVolatile(ctx, x.Operand)
	case *parser.CaseExpr:
		if x.Operand != nil && defaultExprVolatile(ctx, x.Operand) {
			return true
		}
		for _, w := range x.Whens {
			if defaultExprVolatile(ctx, w.When) || defaultExprVolatile(ctx, w.Then) {
				return true
			}
		}
		if x.Else != nil {
			return defaultExprVolatile(ctx, x.Else)
		}
	}
	return false
}
