package executor

// verify_heapam(regclass, ...) set-returning function — slice S3 of
// docs/design/0110-0008 (the amcheck SQL surface). This operator is the thin
// wire-protocol adapter the design called for: it resolves the relation
// argument, fills a PageSource from goopg's buffer manager, hands the relation's
// block count plus its natts / freeze horizons to the already-committed
// internal/amcheck engine (amcheck.VerifyHeapRelation — the relation walk lives
// there, behind the same PageSource seam the B-tree side uses), and streams one
// (blkno, offnum, attnum, msg) output row per structural-corruption finding.
//
// All the corruption-detection logic is in the engine and is unit-tested there;
// this file owns only the catalog/storage/mvcc plumbing the engine deliberately
// stays decoupled from. Like pgPartitionTreeOp it materialises every row in
// Open() and replays them in Next(), so a read or range error surfaces eagerly
// (matching the upstream SRF's first-call ereport) rather than mid-stream.
//
// Scope (this slice): the page-structural, natts, and xmin/xmax numeric-bounds
// tiers, which need no clog. The clog-dependent HOT-chain tier is left disabled
// (nil XidStatusFunc) because the executor has no clog handle yet; wiring it,
// plus the named-argument / LATERAL call shape pg_amcheck itself emits, is the
// S3→S5 follow-up recorded in the deferral ledger. M0110-0003.

import (
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

type verifyHeapamOp struct {
	plan *planner.VerifyHeapam
	rows []Row
	idx  int

	// outerSlot carries the lateral outer row when this op sits on the right of
	// a `Join.Lateral == true` — the implicit-LATERAL comma-join pg_amcheck
	// emits (`FROM pg_class c, verify_heapam(relation := c.oid, …) v`). Bound by
	// the parent joinOp via BindLateralOuter before each per-outer-row Open. nil
	// means "no lateral binding" — the args must reduce to constants, the
	// original non-lateral SRF entry path. M0110-0003 gap #6.
	outerSlot SlotView
}

func newVerifyHeapamOp(p *planner.VerifyHeapam) *verifyHeapamOp {
	return &verifyHeapamOp{plan: p}
}

func (o *verifyHeapamOp) Schema() planner.Schema { return o.plan.Output() }
func (o *verifyHeapamOp) Close() error           { return nil }

// BindLateralOuter binds the outer row's slot for lateral arg evaluation.
// Called by joinOp before each per-outer-row Open when `plan.Lateral == true`.
// Passing nil clears the binding. M0110-0003 gap #6.
func (o *verifyHeapamOp) BindLateralOuter(slot SlotView) {
	o.outerSlot = slot
}

func (o *verifyHeapamOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0
	if ctx.Pool == nil || ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "verify_heapam requires storage handles in Context"}
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(),
			Message: "verify_heapam requires an in-memory catalog"}
	}

	// Resolve the regclass argument to a heap relation. evalExprSlot threads the
	// lateral outer slot (non-nil only under a Join.Lateral) so a correlated arg
	// like `relation := c.oid` resolves against the outer FROM sibling; nil slot
	// behaves like the original constant-arg path. M0110-0003 gap #6.
	argVal, err := evalExprSlot(o.plan.Arg, o.outerSlot, ctx)
	if err != nil {
		return err
	}
	if argVal.IsNull() {
		// NULL relation → no rows (upstream PG_RETURN_NULL on a NULL regclass).
		return nil
	}
	tbl, ok := verifyHeapamResolveTable(argVal, im)
	if !ok {
		return &ExecError{Code: "42P01", Pos: o.plan.Pos(),
			Message: "could not open relation: relation does not exist"}
	}
	// Only ordinary heap relations have heap pages to check. Views / matviews
	// without storage, and non-table relkinds, yield no rows (mirrors upstream's
	// "only heap AM is supported" guard, which the SRF body skips silently here).
	if tbl.View != nil || tbl.Virtual || (tbl.IsMatView && !tbl.IsPopulated) {
		return nil
	}

	opts := amcheck.HeapRelOptions{
		Rel: amcheck.RelDesc{
			Natts:        len(tbl.Columns),
			NextXid:      uint32(ctx.TxnMgr.NextXID()),
			RelFrozenXid: uint32(tbl.RelFrozenXID),
		},
	}
	if sb, ok2, err2 := o.evalBlockArg(ctx, o.plan.StartBlock); err2 != nil {
		return err2
	} else if ok2 {
		opts.StartBlock = &sb
	}
	if eb, ok2, err2 := o.evalBlockArg(ctx, o.plan.EndBlock); err2 != nil {
		return err2
	} else if ok2 {
		opts.EndBlock = &eb
	}

	rel := ctx.Catalog.RelFileNode(tbl)
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}

	// PageSource yields each block's bytes by copy, so the engine can walk the
	// whole relation without holding a pin across the walk (it only reads).
	src := func(blk storage.BlockNumber) (storage.Page, error) {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			return nil, perr
		}
		page := make(storage.Page, len(s.Page()))
		copy(page, s.Page())
		ctx.Pool.Unpin(s)
		return page, nil
	}

	reports, err := amcheck.VerifyHeapRelation(src, nblocks, opts)
	if err != nil {
		// Range / read errors map to the upstream invalid-parameter-value ereport.
		return &ExecError{Code: "22023", Pos: o.plan.Pos(), Message: err.Error()}
	}

	o.rows = make([]Row, len(reports))
	for i, r := range reports {
		o.rows[i] = Row{
			NewIntDatum(int64(r.Blkno)),  // blkno int8
			NewIntDatum(int64(r.Offset)), // offnum int8
			NullDatum,                    // attnum int4 — NULL for page/header-level findings
			NewStringDatum(r.Msg),        // msg text
		}
	}
	return nil
}

func (o *verifyHeapamOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

// evalBlockArg evaluates an optional int8 startblock/endblock argument. It
// returns (value, present, error): present is false for a nil argument or a SQL
// NULL (both mean "whole relation" in VerifyHeapRelation).
func (o *verifyHeapamOp) evalBlockArg(ctx *Context, e planner.Expr) (int64, bool, error) {
	if e == nil {
		return 0, false, nil
	}
	d, err := evalExprSlot(e, o.outerSlot, ctx)
	if err != nil {
		return 0, false, err
	}
	if d.IsNull() {
		return 0, false, nil
	}
	return d.Int, true, nil
}

// verifyHeapamResolveTable converts a KindInt OID or KindString name Datum to
// the heap relation it names. Mirrors partitionResolveRegclass but returns the
// *catalog.Table the SRF needs (natts, relfrozenxid) rather than just a name.
func verifyHeapamResolveTable(d Datum, im *catalog.InMemory) (*catalog.Table, bool) {
	switch d.Kind {
	case KindInt:
		return im.LookupTableByOID(uint32(d.Int))
	case KindString:
		return im.LookupTable(parser.ObjectName{Name: d.StringValue()})
	default:
		return nil, false
	}
}
