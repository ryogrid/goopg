package executor

import (
	"container/heap"
	"io"
	"os"
	"sort"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// valuesOp emits a fixed sequence of rows produced from literal
// expressions. SELECT 1 plans into a Project over a one-row Values
// with an empty input row.
//
// When the underlying plan node carries a VirtualSource (i.e. the
// query reads from a goopg virtual catalog view like pg_stat_wal_receiver),
// rows are refreshed at Open time by calling VirtualRows() on the
// source table. This prevents the cross-session plan cache from
// serving a stale snapshot — without it, the second and later queries
// against a dynamic view would see the row materialised when the plan
// was first cached. See M0094-0005.
type valuesOp struct {
	plan   *planner.Values
	rows   [][]planner.Expr
	idx    int
	ctx    *Context
	schema planner.Schema
}

func newValuesOp(plan *planner.Values) *valuesOp {
	return &valuesOp{plan: plan, rows: plan.Rows, schema: plan.Output()}
}

// rematerialiseVirtualRows rebuilds the row expressions for a Values
// node whose source is a virtual catalog table. The text payload
// returned by tbl.VirtualRows() is wrapped in typed constant expressions
// (via planner.TypedVirtualCell) matching what the planner produces in
// buildVirtualValues; the returned slice replaces o.rows for this Open cycle.
// rematerialiseVirtualRowsFromStrings converts [][]string rows into planner
// expression rows for a virtual table, used for session-specific tables like
// pg_prepared_statements where the data comes from the session rather than
// the global VirtualRows callback.
func rematerialiseVirtualRowsFromStrings(tbl *catalog.Table, raw [][]string) [][]planner.Expr {
	out := make([][]planner.Expr, len(raw))
	for i, r := range raw {
		cells := make([]planner.Expr, len(tbl.Columns))
		for j := range tbl.Columns {
			if j < len(r) {
				cells[j] = planner.TypedVirtualCell(0, r[j], tbl.Columns[j].Type.Name)
			} else {
				cells[j] = &planner.NullConst{}
			}
		}
		out[i] = cells
	}
	return out
}

func rematerialiseVirtualRows(plan *planner.Values) [][]planner.Expr {
	tbl := plan.VirtualSource
	raw := tbl.VirtualRows()
	out := make([][]planner.Expr, len(raw))
	for i, r := range raw {
		cells := make([]planner.Expr, len(tbl.Columns))
		for j := range tbl.Columns {
			if j < len(r) {
				// Sibling of planner.buildVirtualValues — must use the same
				// typed-cell helper so integer/bool virtual columns compare
				// by value, not lexicographically (sysviews int8 compare).
				cells[j] = planner.TypedVirtualCell(0, r[j], tbl.Columns[j].Type.Name)
			} else {
				cells[j] = &planner.NullConst{}
			}
		}
		out[i] = cells
	}
	return out
}

func (o *valuesOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0
	if o.plan != nil && o.plan.VirtualSource != nil {
		tbl := o.plan.VirtualSource
		// pg_prepared_statements is session-specific: use the per-connection
		// lister when available rather than the static empty VirtualRows.
		if tbl.Name == "pg_prepared_statements" && ctx != nil && ctx.PrepStmtsRows != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PrepStmtsRows())
		} else if tbl.Name == "pg_extension" && ctx != nil && ctx.ExtensionRows != nil {
			// pg_extension is per-database: use the per-connection,
			// database-scoped lister so an extension installed in one database
			// is invisible in another. M0110-0003 (AC-002 gap #7c).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.ExtensionRows())
		} else if tbl.Name == "pg_stat_slru" && ctx != nil {
			// pg_stat_slru reports live cumulative SLRU statistics (notify
			// blks_zeroed) honouring the session's stats_fetch_consistency, which
			// the static catalog VirtualRows fallback cannot do. M0118-0009.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchSLRURows(ctx))
		} else if tbl.Name == "pg_stat_io" {
			// pg_stat_io reports live pool-wide shared-buffer counters (the one
			// IO signal goopg instruments), which the static catalog VirtualRows
			// fallback cannot do. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchIOStatRows(ctx))
		} else if tbl.Name == "pg_stat_all_tables" && ctx != nil {
			// pg_stat_*_tables list the connecting database's own tables (not
			// always DefaultDBOid's), which the static catalog VirtualRows
			// fallback cannot do. Resolved directly from ctx.Catalog like the
			// pg_stat_io/pg_sequences branches. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatTablesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_stat_sys_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatTablesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_stat_user_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatTablesRows(ctx, catalog.StatScopeUser))
		} else if tbl.Name == "pg_stat_all_indexes" && ctx != nil {
			// pg_stat_*_indexes list the connecting database's own indexes (the
			// per-index sibling of pg_stat_*_tables), which the static catalog
			// VirtualRows fallback cannot scope. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatIndexesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_stat_sys_indexes" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatIndexesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_stat_user_indexes" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatIndexesRows(ctx, catalog.StatScopeUser))
		} else if tbl.Name == "pg_statio_all_tables" && ctx != nil {
			// pg_statio_*_tables list the connecting database's own tables' buffer-pool
			// I/O stats (honest-0 counters, no per-relation buffer tracking), which the
			// static catalog VirtualRows fallback cannot scope. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioTablesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_statio_sys_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioTablesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_statio_user_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioTablesRows(ctx, catalog.StatScopeUser))
		} else if tbl.Name == "pg_statio_all_indexes" && ctx != nil {
			// pg_statio_*_indexes list the connecting database's own indexes' buffer-pool
			// I/O stats (the per-index sibling of pg_statio_*_tables). M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioIndexesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_statio_sys_indexes" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioIndexesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_statio_user_indexes" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioIndexesRows(ctx, catalog.StatScopeUser))
		} else if tbl.Name == "pg_class" && ctx != nil && ctx.PgClassRows != nil {
			// pg_class must list the connecting database's own tables/indexes,
			// not always DefaultDBOid's — use the per-connection, dbOid-scoped
			// lister. M0122-0007 4e.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgClassRows())
		} else if tbl.Name == "pg_indexes" && ctx != nil && ctx.PgIndexesRows != nil {
			// pg_indexes must list the connecting database's own indexes, not
			// always DefaultDBOid's — mirrors the pg_class branch above.
			// M0122-0007 4e follow-up 24.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgIndexesRows())
		} else if tbl.Name == "pg_tables" && ctx != nil && ctx.PgTablesRows != nil {
			// pg_tables must list the connecting database's own tables, not
			// always DefaultDBOid's — mirrors the pg_class branch above.
			// M0122-0007 4e follow-up 24.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgTablesRows())
		} else if tbl.Name == "pg_constraint" && ctx != nil && ctx.PgConstraintRows != nil {
			// pg_constraint must list the connecting database's own
			// tables'/indexes' constraints, not always DefaultDBOid's —
			// mirrors the pg_class branch above. M0122-0007 4e follow-up 25.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgConstraintRows())
		} else if tbl.Name == "pg_index" && ctx != nil && ctx.PgIndexRows != nil {
			// pg_index must list the connecting database's own indexes, not
			// always DefaultDBOid's — mirrors the pg_constraint branch
			// above. M0122-0007 4e follow-up 26.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgIndexRows())
		} else if tbl.Name == "pg_attrdef" && ctx != nil && ctx.PgAttrdefRows != nil {
			// pg_attrdef must list the connecting database's own tables'
			// column defaults, not always DefaultDBOid's — mirrors the
			// pg_index branch above. M0122-0007 4e follow-up 27.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgAttrdefRows())
		} else if tbl.Name == "pg_depend" && ctx != nil && ctx.PgDependRows != nil {
			// pg_depend must list the connecting database's own dependency
			// rows, not always DefaultDBOid's — mirrors the pg_attrdef
			// branch above. M0122-0007 4e follow-up 27.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgDependRows())
		} else if tbl.Name == "pg_inherits" && ctx != nil && ctx.PgInheritsRows != nil {
			// pg_inherits must list the connecting database's own
			// inheritance/partition parent-child rows, not always
			// DefaultDBOid's — mirrors the pg_depend branch above.
			// M0122-0007 4e follow-up 28.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgInheritsRows())
		} else if tbl.Name == "pg_policy" && ctx != nil && ctx.PgPolicyRows != nil {
			// pg_policy must list the connecting database's own tables' RLS
			// policies, not always DefaultDBOid's — mirrors the pg_inherits
			// branch above. M0122-0007 4e follow-up 29.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgPolicyRows())
		} else if tbl.Name == "pg_trigger" && ctx != nil && ctx.PgTriggerRows != nil {
			// pg_trigger must list the connecting database's own tables'
			// triggers, not always DefaultDBOid's — mirrors the pg_policy
			// branch above. M0122-0007 4e follow-up 30.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgTriggerRows())
		} else if tbl.Name == "pg_rewrite" && ctx != nil && ctx.PgRewriteRows != nil {
			// pg_rewrite must list the connecting database's own tables'
			// CREATE RULE DO-NOTHING rules, not always DefaultDBOid's —
			// mirrors the pg_trigger branch above. M0122-0007 4e follow-up 31.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgRewriteRows())
		} else if tbl.Name == "pg_foreign_table" && ctx != nil && ctx.PgForeignTableRows != nil {
			// pg_foreign_table must list the connecting database's own
			// foreign tables, not always DefaultDBOid's — mirrors the
			// pg_rewrite branch above. M0122-0007 4e follow-up 32.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgForeignTableRows())
		} else if tbl.Name == "pg_sequence" && ctx != nil && ctx.PgSequenceRows != nil {
			// pg_sequence must list the connecting database's own
			// sequences, not always DefaultDBOid's — mirrors the
			// pg_foreign_table branch above. M0122-0007 4e follow-up 34.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgSequenceRows())
		} else if tbl.Name == "pg_sequences" && ctx != nil {
			// pg_sequences must list the connecting database's own sequences,
			// not always DefaultDBOid's. Unlike pg_sequence (singular) above,
			// this reads straight from the executor's own seqRegistry (no
			// catalog.InMemory indirection needed), so it is resolved here
			// directly rather than through a Context-field wire-up — mirrors
			// the pg_stat_slru/pg_stat_io direct-call branches above.
			// M0122-0007 4e follow-up 35.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, PGSequencesRows(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)))
		} else if tbl.Name == "sequences" && ctx != nil && tbl.Schema == "information_schema" {
			// information_schema.sequences mirrors the pg_sequences branch
			// above. M0122-0007 4e follow-up 35.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, InformationSchemaSequencesRows(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)))
		} else if tbl.Name == "pg_foreign_server" && ctx != nil && ctx.PgForeignServerRows != nil {
			// pg_foreign_server must list the connecting database's own
			// CREATE SERVER'd servers, not always DefaultDBOid's — mirrors
			// the pg_foreign_table branch above. M0122-0007 4e follow-up 36.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgForeignServerRows())
		} else if tbl.Name == "pg_user_mappings" && ctx != nil && ctx.PgUserMappingsRows != nil {
			// pg_user_mappings must list the connecting database's own
			// CREATE USER MAPPING'd mappings, not always DefaultDBOid's —
			// mirrors the pg_foreign_server branch above. M0122-0007 4e
			// follow-up 37.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgUserMappingsRows())
		} else if tbl.VirtualRows != nil {
			o.rows = rematerialiseVirtualRows(o.plan)
		}
	}
	return nil
}
func (o *valuesOp) Schema() planner.Schema { return o.schema }
func (o *valuesOp) Close() error           { return nil }

func (o *valuesOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	exprs := o.rows[o.idx]
	o.idx++
	row := make(Row, len(exprs))
	for i, e := range exprs {
		v, err := evalExpr(e, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		row[i] = v
	}
	return asSlot(o.schema, row), nil
}

// projectOp evaluates the target list against each child row.
//
// M0071-0015 Stage E: targets are evaluated into a freshly-cloned
// Row each Next() — projectOp's child slot is consumed inside this
// function (slot is no longer accessed after the loop), so the
// child's lifetime contract is satisfied by the slot itself.
type projectOp struct {
	child   Operator
	targets []planner.Expr
	schema  planner.Schema
	ctx     *Context
	out     Row
	// M0092-0007: stack-aliased slot reused across every Next()
	// call so we don't allocate a fresh MaterializedSlot per row.
	slot MaterializedSlot
}

func newProjectOp(plan *planner.Project, child Operator) *projectOp {
	return &projectOp{child: child, targets: plan.Targets, schema: plan.Output()}
}

func (o *projectOp) Open(ctx *Context) error {
	o.ctx = ctx
	if cap(o.out) < len(o.targets) {
		o.out = acquireRow(len(o.targets))
	} else {
		o.out = o.out[:len(o.targets)]
	}
	return o.child.Open(ctx)
}
func (o *projectOp) Schema() planner.Schema { return o.schema }
func (o *projectOp) Close() error {
	releaseRow(o.out)
	o.out = nil
	return o.child.Close()
}

func (o *projectOp) Next() (TupleSlot, error) {
	inSlot, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	for i, t := range o.targets {
		v, err := evalExprSlot(t, inSlot, o.ctx)
		if err != nil {
			return nil, err
		}
		o.out[i] = v
	}
	// propagate hasCTID so downstream operators can access the row's TID.
	switch v := inSlot.(type) {
	case *Slot:
		o.slot.hasCTID = v.hasCTID
		o.slot.ctidBlock = v.ctidBlock
		o.slot.ctidOff = v.ctidOff
	case *MaterializedSlot:
		o.slot.hasCTID = v.hasCTID
		o.slot.ctidBlock = v.ctidBlock
		o.slot.ctidOff = v.ctidOff
	default:
		o.slot.hasCTID = false
	}
	// M0092-0002: the returned slot ALIASES o.out, which is
	// overwritten on the next Next() call. Audited consumers
	// (filterOp, limitOp, simple/extended-query loops, sortOp,
	// windowOp, lockRowsOp, joinOp, aggregateOp, recursiveUnionOp,
	// nestedLoopIndexJoinOp post-prereq) all consume / materialize
	// before the next Next. See
	// docs/design/0092-0002-projectop-slot-aliasing.md.
	// M0092-0007: stack-aliased slot reused across Next() calls.
	o.slot.schema = o.schema
	o.slot.row = o.out
	return &o.slot, nil
}

// filterOp drops rows where the predicate doesn't evaluate to TRUE.
// NULL predicates exclude the row, matching SQL semantics.
//
// M0071-0012 Stage C: pass-through. The predicate is evaluated
// directly against the child's slot via evalExprSlot — no Row
// materialisation in the hot path. Matching slots are forwarded
// to the parent unchanged, so filter never owns Row buffers and
// no borrow contract is needed.
type filterOp struct {
	child Operator
	pred  planner.Expr
	ctx   *Context
}

func newFilterOp(plan *planner.Filter, child Operator) *filterOp {
	return &filterOp{child: child, pred: plan.Predicate}
}

func (o *filterOp) Open(ctx *Context) error { o.ctx = ctx; return o.child.Open(ctx) }
func (o *filterOp) Schema() planner.Schema  { return o.child.Schema() }
func (o *filterOp) Close() error            { return o.child.Close() }

func (o *filterOp) Next() (TupleSlot, error) {
	rejected := 0
	for {
		// M0062-followup: a highly-selective filter can drain millions
		// of child rows without yielding to the parent, blocking
		// cancel propagation. Check ctx every 4096 rejections.
		if rejected&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		if slot == nil {
			// DML / utility ops surface (nil, nil) for "advance
			// done, no row to surface" — propagate without eval.
			return nil, nil
		}
		v, err := evalExprSlot(o.pred, slot, o.ctx)
		if err != nil {
			return nil, err
		}
		if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
			return slot, nil
		}
		rejected++
	}
}

// limitOp implements LIMIT/OFFSET. Both are evaluated once at Open
// so a long stream doesn't re-evaluate.
//
// M0071-0012 Stage C: pass-through. limitOp returns the child's
// slot unchanged on each emitted row — it owns no Row buffer and
// no borrow contract is needed.
//
// WITH TIES support (M0097-0042): when withTies is true and emitted==limitCount,
// continue emitting rows whose ORDER BY key values equal tieKeyVals (the keys of
// the last emitted row). tieKeyExprs are the ORDER BY key expressions evaluated
// against each row via evalExpr.
type limitOp struct {
	child       Operator
	limitExpr   planner.Expr
	offsetExpr  planner.Expr
	limitCount  int64 // -1 for no limit
	offsetCount int64
	emitted     int64
	skipped     int64
	withTies    bool
	tieKeyExprs []planner.Expr
	tieKeyVals  Row // key values of last emitted row (set when emitted==limitCount)
	inTiesPhase bool
	ctx         *Context
}

func newLimitOp(plan *planner.Limit, child Operator) *limitOp {
	return &limitOp{
		child:       child,
		limitExpr:   plan.Limit,
		offsetExpr:  plan.Offset,
		limitCount:  -1,
		withTies:    plan.WithTies,
		tieKeyExprs: plan.TiesKeys,
	}
}

func (o *limitOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	if o.limitExpr != nil {
		v, err := evalExpr(o.limitExpr, nil, ctx)
		if err != nil {
			return err
		}
		if v.IsNull() {
			// NULL LIMIT means no limit (return all rows) — unless WITH TIES,
			// which requires a concrete row count. M0097-0042.
			if o.withTies {
				return &ExecError{Code: "22004", Pos: o.limitExpr.Pos(),
					Message: "row count cannot be null in FETCH FIRST ... WITH TIES clause"}
			}
		} else {
			if v.Kind != KindInt {
				return &ExecError{Code: "42804", Pos: o.limitExpr.Pos(), Message: "LIMIT must be integer"}
			}
			o.limitCount = v.Int
		}
	}
	if o.offsetExpr != nil {
		v, err := evalExpr(o.offsetExpr, nil, ctx)
		if err != nil {
			return err
		}
		// NULL OFFSET means no offset (start from beginning). M0097-0042.
		if !v.IsNull() {
			if v.Kind != KindInt {
				return &ExecError{Code: "42804", Pos: o.offsetExpr.Pos(), Message: "OFFSET must be integer"}
			}
			o.offsetCount = v.Int
		}
	}
	return nil
}

func (o *limitOp) Schema() planner.Schema { return o.child.Schema() }
func (o *limitOp) Close() error           { return o.child.Close() }

func (o *limitOp) Next() (TupleSlot, error) {
	for o.skipped < o.offsetCount {
		if _, err := o.child.Next(); err != nil {
			return nil, err
		}
		o.skipped++
	}
	if o.limitCount >= 0 && o.emitted >= o.limitCount {
		if !o.withTies {
			return nil, EOF
		}
		// WITH TIES: continue while ORDER BY key values equal those of
		// the last emitted row. M0097-0042.
		slot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		if !o.inTiesPhase {
			// First time we hit the limit — we need tieKeyVals already set
			// from the last emitted row (set below). Now check if this next
			// row ties.
			o.inTiesPhase = true
		}
		row := slotRow(slot)
		if !o.tiesRowMatches(row) {
			return nil, EOF
		}
		return slot, nil
	}
	slot, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	o.emitted++
	// Save the ORDER BY key values of this (possibly last-within-limit) row
	// for WITH TIES comparison. M0097-0042.
	if o.withTies && o.limitCount >= 0 && o.emitted == o.limitCount {
		row := slotRow(slot)
		o.tieKeyVals = make(Row, len(o.tieKeyExprs))
		for i, expr := range o.tieKeyExprs {
			v, err := evalExpr(expr, row, o.ctx)
			if err != nil {
				return nil, err
			}
			o.tieKeyVals[i] = v
		}
	}
	return slot, nil
}

// tiesRowMatches returns true when the supplied row has ORDER BY key values
// equal to tieKeyVals (the boundary values saved from the last within-limit row).
func (o *limitOp) tiesRowMatches(row Row) bool {
	for i, expr := range o.tieKeyExprs {
		v, err := evalExpr(expr, row, o.ctx)
		if err != nil {
			return false
		}
		if !datumEquals(v, o.tieKeyVals[i]) {
			return false
		}
	}
	return true
}

// sortOp buffers the child's output then sorts under the supplied
// key list. Stable sort matches upstream's behaviour.
//
// M0068-0006: when the in-memory chunk exceeds sortChunkBytes the
// chunk is sorted, written to a spill file, and freed. After the
// child is fully drained an N-way merge over the spill files plus
// the in-memory tail produces the final ordered stream. This keeps
// peak heap residency bounded by the chunk size regardless of input
// row count, eliminating the heap blow-up that the M0066 review
// flagged for large sorts.
type sortOp struct {
	child Operator
	keys  []planner.SortKey
	ctx   *Context

	// chunk size threshold for triggering a spill. Default 256 MiB.
	chunkLimitBytes int64

	// In-memory chunk / tail.
	rows []Row
	idx  int

	// ctids carries the per-row TID side-channel (hasCTID / ctidBlock /
	// ctidOff) in lockstep with rows so a parent LockRows can stamp row locks
	// on `ORDER BY ... FOR UPDATE` queries (PG plans `LockRows → Sort`, with
	// ctid as a resjunk column the sort preserves). Only the fully in-memory
	// path preserves it: once the sort spills, ctidsDisabled is set and ctids
	// is dropped — the N-way merge can't carry it. That is rare for row-locking
	// queries and no worse than the prior behaviour (Sort lost the TID
	// entirely). M0118-0003.
	ctids         []sortCTID
	ctidsDisabled bool

	// External-sort state. Populated only when at least one spill
	// has occurred during Open().
	spillFiles []string
	heap       *sortHeap
	mergeReady bool

	sortErr error
}

// sortCTID is the compact per-row TID side-channel carried through the
// in-memory sort (see sortOp.ctids).
type sortCTID struct {
	block uint32
	off   uint16
	has   bool
}

func newSortOp(plan *planner.Sort, child Operator) *sortOp {
	return &sortOp{child: child, keys: plan.Keys}
}

// sortChunkBytes is the in-memory threshold at which a sort chunk
// is flushed to a spill file. 256 MiB matches the build-side
// drainRowsBounded default and keeps a single chunk's footprint
// well below typical container-memory limits while remaining big
// enough to absorb every TPC-H SF=1 sort that doesn't otherwise
// require external sort.
const sortChunkBytes = int64(256 * 1024 * 1024)

func (o *sortOp) chunkLimit() int64 {
	if o.chunkLimitBytes > 0 {
		return o.chunkLimitBytes
	}
	return sortChunkBytes
}

func (o *sortOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	limit := o.chunkLimit()
	var chunkBytes int64
	pulled := 0
	for {
		// M0062-followup: a sort over millions of rows can otherwise
		// drain the child without a cancel opportunity. ctx check
		// every 4096 rows pulled.
		if pulled&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// Materialize at retention boundary: sortOp holds rows
		// across many Next() calls, so the slot's lifetime
		// contract requires us to take ownership of an
		// independent row. (M0071-0010 Stage B.)
		ms := slot.Materialize()
		row := ms.Row()
		o.rows = append(o.rows, row)
		if !o.ctidsDisabled {
			o.ctids = append(o.ctids, sortCTID{block: ms.ctidBlock, off: ms.ctidOff, has: ms.hasCTID})
		}
		chunkBytes += estimatedRowBytes(row)
		pulled++
		if chunkBytes >= limit {
			if err := o.flushChunk(); err != nil {
				return err
			}
			o.rows = o.rows[:0]
			// Spilling drops the TID side-channel: the N-way merge over spill
			// files reconstructs rows without ctids. Disable it permanently so
			// the in-memory Next() path doesn't emit stale/misaligned TIDs.
			o.ctidsDisabled = true
			o.ctids = nil
			chunkBytes = 0
		}
	}
	// Sort the final in-memory tail, keeping ctids in lockstep.
	o.sortTailWithCTIDs()
	if o.sortErr != nil {
		return o.sortErr
	}
	return nil
}

// sortChunk in-place sorts a slice using the configured key list.
// Sets o.sortErr if an evaluator error surfaces during comparison.
func (o *sortOp) sortChunk(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		return o.lessRows(rows[i], rows[j])
	})
}

// sortTailWithCTIDs sorts the final in-memory tail (o.rows). When the TID
// side-channel is live (no spill occurred), it reorders o.ctids in lockstep
// with o.rows via a permutation so each emitted row keeps its own ctid.
// Falls back to the plain row sort when ctids are disabled/absent.
func (o *sortOp) sortTailWithCTIDs() {
	if o.ctidsDisabled || len(o.ctids) != len(o.rows) {
		o.sortChunk(o.rows)
		return
	}
	perm := make([]int, len(o.rows))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(i, j int) bool {
		return o.lessRows(o.rows[perm[i]], o.rows[perm[j]])
	})
	if o.sortErr != nil {
		return
	}
	newRows := make([]Row, len(o.rows))
	newCtids := make([]sortCTID, len(o.ctids))
	for i, p := range perm {
		newRows[i] = o.rows[p]
		newCtids[i] = o.ctids[p]
	}
	o.rows = newRows
	o.ctids = newCtids
}

// lessRows returns true iff a should sort before b under the
// configured key list. Records the first evaluator error in
// o.sortErr and returns false on error so the comparator stays
// strict-weak-ordered for the rest of the sort.
func (o *sortOp) lessRows(a, b Row) bool {
	for _, k := range o.keys {
		av, err := evalExpr(k.Expr, a, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		bv, err := evalExpr(k.Expr, b, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		if av.IsNull() && !bv.IsNull() {
			return k.NullsFirst // NULL sorts first when NullsFirst=true
		}
		if !av.IsNull() && bv.IsNull() {
			return !k.NullsFirst // non-NULL sorts first when NullsFirst=false
		}
		if av.IsNull() && bv.IsNull() {
			continue
		}
		cmp, err := compareDatum(av, bv, k.Expr.Pos())
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		if cmp == 0 {
			continue
		}
		if k.Desc {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

// flushChunk sorts the current in-memory chunk and writes it to a
// new spill file. The caller must reset o.rows after the call.
func (o *sortOp) flushChunk() error {
	o.sortChunk(o.rows)
	if o.sortErr != nil {
		return o.sortErr
	}
	w, err := newSpillWriter(os.TempDir())
	if err != nil {
		return err
	}
	for _, r := range o.rows {
		if werr := w.WriteRow(r); werr != nil {
			w.Close()
			return werr
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	o.spillFiles = append(o.spillFiles, w.Path())
	return nil
}

func (o *sortOp) Schema() planner.Schema { return o.child.Schema() }
func (o *sortOp) Close() error {
	o.rows = nil
	o.ctids = nil
	o.idx = 0
	o.ctx = nil
	if o.heap != nil {
		for _, s := range o.heap.sources {
			if s.reader != nil {
				_ = s.reader.Close()
			}
		}
		o.heap = nil
	}
	for _, p := range o.spillFiles {
		_ = os.Remove(p)
	}
	o.spillFiles = nil
	return o.child.Close()
}

func (o *sortOp) Next() (TupleSlot, error) {
	if len(o.spillFiles) == 0 {
		// Fully in-memory path.
		if o.idx >= len(o.rows) {
			return nil, EOF
		}
		row := o.rows[o.idx]
		slot := SlotFromRow(o.Schema(), row)
		// Re-attach the per-row TID side-channel so a parent LockRows can stamp
		// row locks (ORDER BY ... FOR UPDATE). M0118-0003.
		if !o.ctidsDisabled && o.idx < len(o.ctids) && o.ctids[o.idx].has {
			slot.hasCTID = true
			slot.ctidBlock = o.ctids[o.idx].block
			slot.ctidOff = o.ctids[o.idx].off
		}
		o.idx++
		return slot, nil
	}
	if !o.mergeReady {
		if err := o.initMerge(); err != nil {
			return nil, err
		}
	}
	row, err := o.popMerge()
	if err != nil {
		return nil, err
	}
	return asSlot(o.Schema(), row), nil
}

// initMerge opens spill readers, primes each source with one row,
// and builds the min-heap.
func (o *sortOp) initMerge() error {
	o.heap = &sortHeap{less: o.lessRows}
	for _, p := range o.spillFiles {
		r, err := newSpillReader(p)
		if err != nil {
			return err
		}
		s := &sortSource{reader: r}
		if err := s.advance(); err != nil {
			return err
		}
		if !s.eof {
			heap.Push(o.heap, s)
		}
	}
	if len(o.rows) > 0 {
		s := &sortSource{rows: o.rows}
		s.advance()
		if !s.eof {
			heap.Push(o.heap, s)
		}
	}
	o.mergeReady = true
	return nil
}

// popMerge returns the smallest row across all sources, advancing
// the source it came from.
func (o *sortOp) popMerge() (Row, error) {
	if o.heap.Len() == 0 {
		return nil, EOF
	}
	s := heap.Pop(o.heap).(*sortSource)
	row := s.cur
	if err := s.advance(); err != nil {
		return nil, err
	}
	if !s.eof {
		heap.Push(o.heap, s)
	}
	if o.sortErr != nil {
		return nil, o.sortErr
	}
	return row, nil
}

// sortSource is a single input to the N-way merge. Either a
// spillReader (file-backed chunk) or an in-memory rows slice
// (the un-spilled tail).
type sortSource struct {
	reader *spillReader
	rows   []Row
	idx    int

	cur Row
	eof bool
}

func (s *sortSource) advance() error {
	if s.reader != nil {
		row, err := s.reader.ReadRow()
		if err == io.EOF {
			s.eof = true
			s.cur = nil
			_ = s.reader.Close()
			s.reader = nil
			return nil
		}
		if err != nil {
			return err
		}
		s.cur = cloneRow(row) // ReadRow's buffer is reused; clone for retain
		return nil
	}
	if s.idx >= len(s.rows) {
		s.eof = true
		s.cur = nil
		return nil
	}
	s.cur = s.rows[s.idx]
	s.idx++
	return nil
}

// sortHeap is a min-heap of sortSources keyed by their current row
// under a row-comparator function.
type sortHeap struct {
	sources []*sortSource
	less    func(a, b Row) bool
}

func (h *sortHeap) Len() int           { return len(h.sources) }
func (h *sortHeap) Less(i, j int) bool { return h.less(h.sources[i].cur, h.sources[j].cur) }
func (h *sortHeap) Swap(i, j int)      { h.sources[i], h.sources[j] = h.sources[j], h.sources[i] }
func (h *sortHeap) Push(x any)         { h.sources = append(h.sources, x.(*sortSource)) }
func (h *sortHeap) Pop() any {
	n := len(h.sources)
	x := h.sources[n-1]
	h.sources = h.sources[:n-1]
	return x
}
