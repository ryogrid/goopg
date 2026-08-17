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
// Scope: the page-structural, natts, and xmin/xmax numeric-bounds tiers PLUS the
// clog-dependent HOT-chain tier. S5 wired the latter: verifyHeapamXidStatus
// turns the live mvcc.Manager into the engine's XidStatusFunc (committed/aborted
// from the CLOG + aborted set, in-progress from the proc array, the current
// backend's own XID kept distinct), so the previously-disabled clog tier now
// runs. M0110-0003.

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/amcheck"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

type verifyHeapamOp struct {
	plan *optimizer.VerifyHeapam
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

func newVerifyHeapamOp(p *optimizer.VerifyHeapam) *verifyHeapamOp {
	return &verifyHeapamOp{plan: p}
}

func (o *verifyHeapamOp) Schema() optimizer.Schema { return o.plan.Output() }
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
		if argVal.Kind == KindInt {
			if _, isToast := im.ToastParentTable(uint32(argVal.Int)); isToast {
				// Synthetic TOAST relation OID (catalog.go's toastRelidOffset
				// scheme): goopg exposes pg_toast_<oid> only as a pg_class/
				// pg_dump join target with no real backing heap (see
				// tableHasToastRelation's doc comment) — no value is ever
				// actually stored there, so it is vacuously always a healthy,
				// empty relation. Report no findings instead of erroring, so
				// pg_amcheck's default whole-table walk (which checks every
				// table's TOAST relation alongside the table itself) does not
				// treat every table as corrupt just because its TOAST
				// relation cannot be opened.
				return nil
			}
		}
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
		// Clog-backed xmin classifier: enables the HOT-chain corruption tier
		// (S5 of docs/design/0110-0008). Committed / aborted come from the CLOG
		// + in-memory aborted set, in-progress from the proc array, and the
		// current backend's own XID is kept distinct so a verify_heapam run
		// inside a writing transaction does not flag its own tuples.
		XidStatus: verifyHeapamXidStatus(ctx),
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
	// Mirror upstream verify_heapam opening the relation's main fork: a heap
	// whose backing file was removed on disk (the pg_amcheck heap file-removal
	// corruption scenario, 003_check.pl's plan_to_remove_relation_file on an
	// ordinary table) makes RelationGetNumberOfBlocks → mdnblocks fail with
	// `could not open file "<path>": No such file or directory`. This must run
	// BEFORE NBlocks, which opens the rel with O_CREATE and would otherwise
	// recreate the fork as an empty 0-block file — silently reporting the table
	// clean (a false negative exactly where PG reports corruption). 58030 is
	// ERRCODE_IO_ERROR, what mdopenfork's errcode_for_file_access() yields for an
	// ENOENT ("No such file") open failure (its default branch, fd.c).
	if !ctx.Pool.Exists(rel) {
		return &ExecError{Code: "58030", Pos: o.plan.Pos(),
			Message: fmt.Sprintf("could not open file %q: No such file or directory",
				ctx.Pool.RelPath(rel))}
	}
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
func (o *verifyHeapamOp) evalBlockArg(ctx *Context, e optimizer.Expr) (int64, bool, error) {
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

// verifyHeapamXidStatus builds the engine's XidStatusFunc from the live MVCC
// manager, the clog-backed seam the S3 operator left disabled (nil). It mirrors
// upstream get_xid_status (verify_heapam.c): the current backend's own XID maps
// to XidStatusCurrent (so a verify_heapam run inside a writing txn never flags
// its own in-flight tuples), and every other normal xid is classified by
// mvcc.Manager.ClassifyXID into in-progress / committed / aborted, with an
// out-of-range xid resolving to XidStatusUnknown (which disables the
// clog-dependent checks for that tuple). The engine resolves the bootstrap (1)
// and frozen (2) xids before ever calling this, so they need no handling here.
// Returns nil when no manager is wired, which keeps the page-bytes-only tiers
// working exactly as before.
func verifyHeapamXidStatus(ctx *Context) amcheck.XidStatusFunc {
	if ctx.TxnMgr == nil {
		return nil
	}
	mgr := ctx.TxnMgr
	selfXID := ctx.Tx.XID
	return func(xid uint32) amcheck.XidCommitStatus {
		tid := storage.TransactionID(xid)
		if selfXID != storage.InvalidTransactionID && tid == selfXID {
			return amcheck.XidStatusCurrent
		}
		switch mgr.ClassifyXID(tid) {
		case transam.XidVisInProgress:
			return amcheck.XidStatusInProgress
		case transam.XidVisCommitted:
			return amcheck.XidStatusCommitted
		case transam.XidVisAborted:
			return amcheck.XidStatusAborted
		default:
			return amcheck.XidStatusUnknown
		}
	}
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
