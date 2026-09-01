package executor

import (
	"container/heap"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
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
	plan   *optimizer.Values
	rows   [][]optimizer.Expr
	idx    int
	ctx    *Context
	schema optimizer.Schema
}

func newValuesOp(plan *optimizer.Values) *valuesOp {
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
func rematerialiseVirtualRowsFromStrings(tbl *catalog.Table, raw [][]string) [][]optimizer.Expr {
	out := make([][]optimizer.Expr, len(raw))
	for i, r := range raw {
		cells := make([]optimizer.Expr, len(tbl.Columns))
		for j := range tbl.Columns {
			if j < len(r) {
				cells[j] = optimizer.TypedVirtualCell(0, r[j], tbl.Columns[j].Type.Name)
			} else {
				cells[j] = &optimizer.NullConst{}
			}
		}
		out[i] = cells
	}
	return out
}

func rematerialiseVirtualRows(plan *optimizer.Values) [][]optimizer.Expr {
	tbl := plan.VirtualSource
	raw := tbl.VirtualRows()
	out := make([][]optimizer.Expr, len(raw))
	for i, r := range raw {
		cells := make([]optimizer.Expr, len(tbl.Columns))
		for j := range tbl.Columns {
			if j < len(r) {
				// Sibling of planner.buildVirtualValues — must use the same
				// typed-cell helper so integer/bool virtual columns compare
				// by value, not lexicographically (sysviews int8 compare).
				cells[j] = optimizer.TypedVirtualCell(0, r[j], tbl.Columns[j].Type.Name)
			} else {
				cells[j] = &optimizer.NullConst{}
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
		} else if tbl.Name == "pg_stat_xact_all_tables" && ctx != nil {
			// pg_stat_xact_*_tables list the connecting database's own tables'
			// per-transaction delta counters (honest-0, no per-xact tracking), the
			// transaction-scoped sibling of pg_stat_*_tables. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatXactTablesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_stat_xact_sys_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatXactTablesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_stat_xact_user_tables" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatXactTablesRows(ctx, catalog.StatScopeUser))
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
		} else if tbl.Name == "pg_statio_all_sequences" && ctx != nil {
			// pg_statio_*_sequences list the connecting database's own sequences'
			// buffer-pool I/O stats (the sequence sibling of pg_statio_*_tables). M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioSequencesRows(ctx, catalog.StatScopeAll))
		} else if tbl.Name == "pg_statio_sys_sequences" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioSequencesRows(ctx, catalog.StatScopeSys))
		} else if tbl.Name == "pg_statio_user_sequences" && ctx != nil {
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatioSequencesRows(ctx, catalog.StatScopeUser))
		} else if tbl.Name == "pg_stats" && ctx != nil {
			// pg_stats projects the connecting database's own per-column ANALYZE
			// statistics (pg_statistic), which the static DefaultDBOid VirtualRows
			// fallback cannot scope. M0122-0003.
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, fetchStatsRows(ctx))
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
		} else if tbl.Name == "pg_collation" && ctx != nil && ctx.PgCollationRows != nil {
			// pg_collation must list the connecting database's own CREATE
			// COLLATION'd collations, not always DefaultDBOid's — mirrors
			// the pg_user_mappings branch above. M0122-0007 4e follow-up
			// (DU-002 round-trip probe unblock).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgCollationRows())
		} else if tbl.Name == "pg_conversion" && ctx != nil && ctx.PgConversionRows != nil {
			// pg_conversion must list the connecting database's own CREATE
			// [DEFAULT] CONVERSION'd conversions, not always DefaultDBOid's
			// — mirrors the pg_collation branch above. M0122-0007 4e
			// follow-up (DU-002 round-trip probe unblock).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgConversionRows())
		} else if tbl.Name == "pg_ts_dict" && ctx != nil && ctx.PgTSDictRows != nil {
			// pg_ts_dict must list the connecting database's own CREATE TEXT
			// SEARCH DICTIONARY'd dictionaries, not always DefaultDBOid's —
			// mirrors the pg_conversion branch above. M0122-0007 4e
			// follow-up (DU-002 round-trip probe unblock).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgTSDictRows())
		} else if tbl.Name == "pg_ts_config" && ctx != nil && ctx.PgTSConfigRows != nil {
			// pg_ts_config must list the connecting database's own CREATE TEXT
			// SEARCH CONFIGURATION'd configurations, not always DefaultDBOid's
			// — mirrors the pg_ts_dict branch above. M0122-0007 4e
			// follow-up (DU-002 round-trip probe unblock).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgTSConfigRows())
		} else if tbl.Name == "pg_publication" && ctx != nil && ctx.PgPublicationRows != nil {
			// pg_publication must list the connecting database's own CREATE
			// PUBLICATION'd publications, not always DefaultDBOid's — mirrors
			// the pg_ts_config branch above. M0119-0004 (DU-002 per-DB
			// publication scoping).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgPublicationRows())
		} else if tbl.Name == "pg_subscription" && ctx != nil && ctx.PgSubscriptionRows != nil {
			// pg_subscription must list the connecting database's own CREATE
			// SUBSCRIPTION'd subscriptions, not always DefaultDBOid's —
			// mirrors the pg_publication branch above. M0119-0004 (DU-002
			// per-DB subscription scoping).
			o.rows = rematerialiseVirtualRowsFromStrings(tbl, ctx.PgSubscriptionRows())
		} else if tbl.VirtualRows != nil {
			o.rows = rematerialiseVirtualRows(o.plan)
		}
	}
	return nil
}
func (o *valuesOp) Schema() optimizer.Schema { return o.schema }
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
	targets []optimizer.Expr
	schema  optimizer.Schema
	ctx     *Context
	out     Row
	// M0092-0007: stack-aliased slot reused across every Next()
	// call so we don't allocate a fresh MaterializedSlot per row.
	slot MaterializedSlot
}

func newProjectOp(plan *optimizer.Project, child Operator) *projectOp {
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
func (o *projectOp) Schema() optimizer.Schema { return o.schema }
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
// resultOp implements PostgreSQL's T_Result (nodeResult.c). In its childless
// shape it evaluates Targets exactly once against an EMPTY input and emits
// exactly one row, then EOF — the S6 min/max rewrite top node whose sole target
// is a non-correlated SubqueryExpr feeding the rewritten aggregate value (the
// InitPlan). With a Child (S6 Slice 3d const-arg rewrite) it emits one projected
// row per child row, exactly PG's `outerPlan(plan)` variant.
//
// OneTimeFilter is PG's resconstantqual (nodeResult.c rs_checkqual latch): when
// non-nil it is evaluated ONCE at Open against a nil slot, and a NULL/false
// result short-circuits the node to emit no rows at all (the child is never
// even opened).
//
// M0071-0015 Stage E note: the returned slot ALIASES o.out. Childless that is
// safe — the first Next() sets o.emitted and the second returns EOF. With a
// child, o.out is overwritten per Next() under the standard operator contract
// (the parent must consume the slot before pulling again); the const-arg
// rewrite sits under a LIMIT 1 which pulls exactly once.
type resultOp struct {
	targets []optimizer.Expr
	schema  optimizer.Schema
	ctx     *Context
	out     Row
	emitted bool
	slot    MaterializedSlot
	// child is nil for the childless single-emit Result (the S6 InitPlan top
	// node); set for the const-arg rewrite's inner Result (SeqScan child).
	child Operator
	// qual is the optional One-Time Filter (resconstantqual); nil = none.
	qual optimizer.Expr
	// qualFailed is set at Open when the one-time filter evaluated NULL/false:
	// Next then returns EOF immediately and the child is never opened.
	qualFailed bool
}

func newResultOp(plan *optimizer.Result, child Operator) *resultOp {
	return &resultOp{targets: plan.Targets, schema: plan.Output(),
		child: child, qual: plan.OneTimeFilter}
}

func (o *resultOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.emitted = false
	o.qualFailed = false
	if o.qual != nil {
		// One-Time Filter: evaluate ONCE with a nil slot (the const qual reads
		// no input row). A NULL/false result short-circuits the node — no rows,
		// child never opened (nodeResult.c ExecResult: resconstantqual false →
		// return NULL).
		v, err := evalExpr(o.qual, nil, ctx)
		if err != nil {
			return err
		}
		if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
			o.qualFailed = true
			return nil
		}
	}
	if o.child != nil {
		return o.child.Open(ctx)
	}
	if cap(o.out) < len(o.targets) {
		o.out = acquireRow(len(o.targets))
	} else {
		o.out = o.out[:len(o.targets)]
	}
	return nil
}

func (o *resultOp) Schema() optimizer.Schema { return o.schema }

func (o *resultOp) Close() error {
	if o.child != nil {
		if err := o.child.Close(); err != nil {
			return err
		}
	}
	releaseRow(o.out)
	o.out = nil
	return nil
}

func (o *resultOp) Next() (TupleSlot, error) {
	if o.qualFailed {
		return nil, EOF
	}
	if o.child != nil {
		// Result-with-child: one projected row per child row (PG's
		// `outerPlan(plan)` ExecResult variant). The parent Limit stops after
		// the first row; we never latch a single-emit flag here.
		childSlot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		if childSlot == nil {
			return nil, EOF
		}
		if cap(o.out) < len(o.targets) {
			o.out = acquireRow(len(o.targets))
		} else {
			o.out = o.out[:len(o.targets)]
		}
		for i, t := range o.targets {
			v, err := evalExprSlot(t, childSlot, o.ctx)
			if err != nil {
				return nil, err
			}
			o.out[i] = v
		}
		o.slot.schema = o.schema
		o.slot.row = o.out
		return &o.slot, nil
	}
	if o.emitted {
		return nil, EOF
	}
	o.emitted = true
	for i, t := range o.targets {
		v, err := evalExpr(t, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		o.out[i] = v
	}
	o.slot.schema = o.schema
	o.slot.row = o.out
	return &o.slot, nil
}

type filterOp struct {
	child         Operator
	pred          optimizer.Expr
	ctx           *Context
	filterRemoved *int64 // set by maybeInstrument; nil when not instrumented
}

func newFilterOp(plan *optimizer.Filter, child Operator) *filterOp {
	return &filterOp{child: child, pred: plan.Predicate}
}

func (o *filterOp) Open(ctx *Context) error { o.ctx = ctx; return o.child.Open(ctx) }
func (o *filterOp) Schema() optimizer.Schema  { return o.child.Schema() }
func (o *filterOp) Close() error            { return o.child.Close() }

func (o *filterOp) setFilterRemoveCounter(p *int64) { o.filterRemoved = p }

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
		if o.filterRemoved != nil {
			*o.filterRemoved++
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
	limitExpr   optimizer.Expr
	offsetExpr  optimizer.Expr
	limitCount  int64 // -1 for no limit
	offsetCount int64
	emitted     int64
	skipped     int64
	withTies    bool
	tieKeyExprs []optimizer.Expr
	tieKeyVals  Row // key values of last emitted row (set when emitted==limitCount)
	inTiesPhase bool
	ctx         *Context
}

func newLimitOp(plan *optimizer.Limit, child Operator) *limitOp {
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
	// S2a (design bundle ch.04 §4.2): reset the per-execution counters so a
	// re-Open restarts the limit window. Neither Open nor Close cleared
	// them before; every consumer happened to Build a fresh operator, so a
	// retained `LIMIT 1` subplan under handle reuse would return EOF for
	// every outer row after the first.
	o.emitted = 0
	o.skipped = 0
	o.inTiesPhase = false
	o.tieKeyVals = nil
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

func (o *limitOp) Schema() optimizer.Schema { return o.child.Schema() }
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
		//
		// No boundary row means no ties to keep: `FETCH FIRST 0 ROWS WITH
		// TIES` never emitted one, so tieKeyVals is still nil and
		// tiesRowMatches would index an empty Row. PG returns zero rows for a
		// zero count (nodeLimit.c never enters the tie window)
		// (review/260831-2 EO1-3).
		if len(o.tieKeyVals) != len(o.tieKeyExprs) {
			return nil, EOF
		}
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
	keys  []optimizer.SortKey
	ctx   *Context

	// chunk size threshold for triggering a spill. Default 256 MiB.
	chunkLimitBytes int64

	// In-memory chunk / tail.
	rows []Row
	idx  int
	// keyvals[i] holds the ORDER BY key values of rows[i], evaluated once when
	// the row is pulled instead of re-derived inside every comparison
	// (M0134-0191). goopg's per-key cost is an interpreted evalExpr dispatch,
	// so paying it O(N log N) times dominated the sort; PG stores only its
	// first key (SortTuple.datum1) because heap_getattr is cheap enough that
	// its complexity tradeoff lands elsewhere — see
	// docs/design/not_ralph/parallel-sort/DESIGN.md §4.1 for why goopg
	// deliberately diverges and stores all k.
	//
	// Kept in lockstep with rows through every permutation, exactly as ctids
	// is, and TRUNCATED with rows at the spill point below — a keyvals that
	// outlived a flush would be offset by every spilled row and silently
	// compare the wrong keys.
	keyvals [][]Datum

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

func newSortOp(plan *optimizer.Sort, child Operator) *sortOp {
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
		kv, kerr := o.sortKeyVals(row)
		if kerr != nil {
			return kerr
		}
		o.rows = append(o.rows, row)
		o.keyvals = append(o.keyvals, kv)
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
			o.keyvals = o.keyvals[:0]
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

// sortKeyVals evaluates every ORDER BY key for one row, once. This is the ONLY
// place sort keys are computed: the in-memory tail fills it as rows are pulled,
// and each spill-merge source fills it as it advances, so both paths compare
// through the same lessKeyVals and cannot drift apart (DESIGN §5.6 — a tail
// sorted by one comparator and merged against spill files by another emits
// out-of-order rows with no error).
//
// evalSortKeyValue, not evalExpr: the reg*-OID family sorts by the underlying
// OID (see isRegSortFamilyTypeName).
func (o *sortOp) sortKeyVals(row Row) ([]Datum, error) {
	if len(o.keys) == 0 {
		return nil, nil
	}
	kv := make([]Datum, len(o.keys))
	for i, k := range o.keys {
		v, err := evalSortKeyValue(k.Expr, row, o.ctx)
		if err != nil {
			return nil, err
		}
		kv[i] = v
	}
	return kv, nil
}

// lessKeyVals is the comparator, over PRECOMPUTED key values. It is the same
// rule lessRows applied — NULL placement by NullsFirst, then compareDatum,
// then Desc — with the evaluation lifted out.
func (o *sortOp) lessKeyVals(a, b []Datum) bool {
	for i, k := range o.keys {
		av, bv := a[i], b[i]
		if av.IsNull() && !bv.IsNull() {
			return k.NullsFirst
		}
		if !av.IsNull() && bv.IsNull() {
			return !k.NullsFirst
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

// sortChunk sorts o.rows and o.keyvals together. It is permutation-based
// rather than a bare in-place sort because keyvals has to move with rows;
// before M0134-0191 there was nothing to keep in step and it sorted rows
// directly.
func (o *sortOp) sortChunk(rows []Row) {
	if len(o.keyvals) != len(rows) {
		// Defensive: nothing should reach here with the two out of step, and
		// comparing on stale keys would be a silent wrong answer.
		sort.SliceStable(rows, func(i, j int) bool { return o.lessRows(rows[i], rows[j]) })
		return
	}
	perm := make([]int, len(rows))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(i, j int) bool {
		return o.lessKeyVals(o.keyvals[perm[i]], o.keyvals[perm[j]])
	})
	applySortPerm(perm, rows, o.keyvals, nil)
}

// applySortPerm rewrites rows / keyvals / ctids in place under perm. Every
// slice moves under the SAME permutation, which is what keeps a row with its
// own keys and its own TID.
func applySortPerm(perm []int, rows []Row, keyvals [][]Datum, ctids []sortCTID) {
	newRows := make([]Row, len(perm))
	for i, p := range perm {
		newRows[i] = rows[p]
	}
	copy(rows, newRows)
	if keyvals != nil {
		newKV := make([][]Datum, len(perm))
		for i, p := range perm {
			newKV[i] = keyvals[p]
		}
		copy(keyvals, newKV)
	}
	if ctids != nil {
		newC := make([]sortCTID, len(perm))
		for i, p := range perm {
			newC[i] = ctids[p]
		}
		copy(ctids, newC)
	}
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
	if len(o.keyvals) != len(o.rows) {
		o.sortChunk(o.rows)
		return
	}
	perm := make([]int, len(o.rows))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(i, j int) bool {
		return o.lessKeyVals(o.keyvals[perm[i]], o.keyvals[perm[j]])
	})
	if o.sortErr != nil {
		return
	}
	applySortPerm(perm, o.rows, o.keyvals, o.ctids)
}

// isRegSortFamilyTypeName reports whether name is one of the reg* OID-alias
// types that define NO comparison operators/opclass of their own in PG
// (grepped postgres/src/include/catalog/pg_operator.dat and pg_opclass.dat —
// zero hits for all eleven). Per pg_cast.dat:182-185 each has an IMPLICIT
// BINARY-COERCIBLE cast to/from oid, so operator resolution for `<`/`ORDER
// BY` falls back to oid's own btree comparator (btoidcmp,
// nbtcompare.c:441) — an unsigned OID compare, not a name compare.
// M0134-0005aj (hunk 12): scoped to the Sort operator only; do NOT reuse
// this for `=`/`IN`/hashing — those paths don't yet carry a KindInt OID for
// the CastExpr-sourced case (see the round-2 research report for why that's
// a separate, larger slice).
func isRegSortFamilyTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "regclass", "regproc", "regprocedure", "regtype", "regoper",
		"regoperator", "regrole", "regnamespace", "regcollation",
		"regconfig", "regdictionary":
		return true
	}
	return false
}

// evalSortKeyValue evaluates a single ORDER BY key expression for one row,
// yielding the value the Sort comparator should compare against its peer.
// For a reg*-family cast (`*optimizer.CastExpr` whose TargetType is in
// isRegSortFamilyTypeName), PG sorts by the underlying OID rather than the
// cast's own (nonexistent) comparison operator — see
// isRegSortFamilyTypeName's doc comment. Evaluating the operand directly
// yields that OID as a KindInt datum when the operand is already numeric
// (the `conrelid::regclass` shape); when the operand is instead a string
// literal (`'pg_class'::regclass`), the operand alone is a relation name,
// not an OID, so the full cast is evaluated instead — its KindString arm
// (expr.go's CastExpr evaluator) already resolves the name to the OID and
// returns it as KindInt. Both branches therefore yield a KindInt OID, so
// the two source shapes compare consistently. Any other expression shape
// is evaluated exactly as before. M0134-0005aj (hunk 12).
func evalSortKeyValue(e optimizer.Expr, row Row, ctx *Context) (Datum, error) {
	if ce, ok := e.(*optimizer.CastExpr); ok && isRegSortFamilyTypeName(ce.TargetType) {
		ov, err := evalExpr(ce.Operand, row, ctx)
		if err == nil && ov.Kind == KindInt {
			return ov, nil
		}
	}
	return evalExpr(e, row, ctx)
}

// lessRows returns true iff a should sort before b under the
// configured key list. Records the first evaluator error in
// o.sortErr and returns false on error so the comparator stays
// strict-weak-ordered for the rest of the sort.
func (o *sortOp) lessRows(a, b Row) bool {
	for _, k := range o.keys {
		av, err := evalSortKeyValue(k.Expr, a, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		bv, err := evalSortKeyValue(k.Expr, b, o.ctx)
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
	// M0127-P3.3: the sort's chunks go to <datadir>/base/pgsql_tmp via the
	// statement's registry, so a sort that errors between flushChunk and
	// Close no longer strands them (o.ctx is set in Open).
	w, err := newSpillWriter(o.ctx)
	if err != nil {
		return err
	}
	for _, r := range o.rows {
		if werr := w.WriteRow(r); werr != nil {
			w.Close()
			o.ctx.removeSpillFile(w.Path())
			return werr
		}
	}
	if err := w.Close(); err != nil {
		o.ctx.removeSpillFile(w.Path())
		return err
	}
	o.spillFiles = append(o.spillFiles, w.Path())
	return nil
}

func (o *sortOp) Schema() optimizer.Schema { return o.child.Schema() }
func (o *sortOp) Close() error {
	// Captured before o.ctx is cleared: the spill-file unlink below has to
	// tell the statement's registry it no longer owns these paths.
	ctx := o.ctx
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
		ctx.forgetSpillFile(p)
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
	// Same comparator as the in-memory tail (DESIGN §5.6): each source
	// computes its current row's keys on advance, and the heap orders those.
	o.heap = &sortHeap{less: o.lessKeyVals, keysOf: o.sortKeyVals}
	for _, p := range o.spillFiles {
		r, err := newSpillReader(p)
		if err != nil {
			return err
		}
		s := &sortSource{reader: r, keysOf: o.sortKeyVals}
		if err := s.advance(); err != nil {
			return err
		}
		if !s.eof {
			heap.Push(o.heap, s)
		}
	}
	if len(o.rows) > 0 {
		// The tail already has its keys; hand them over rather than recompute.
		s := &sortSource{rows: o.rows, keyvals: o.keyvals, keysOf: o.sortKeyVals}
		if err := s.advance(); err != nil {
			return err
		}
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

	// curKeys are cur's ORDER BY key values, so the merge heap compares
	// precomputed keys exactly as the in-memory sort does (M0134-0191).
	// keyvals, when set, is the caller's already-computed table for `rows`
	// (the in-memory tail hands its own over rather than recomputing);
	// keysOf computes them for rows read back from a spill file, which carry
	// no keys of their own.
	curKeys []Datum
	keyvals [][]Datum
	keysOf  func(Row) ([]Datum, error)
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
		return s.loadKeys(-1)
	}
	if s.idx >= len(s.rows) {
		s.eof = true
		s.cur = nil
		s.curKeys = nil
		return nil
	}
	s.cur = s.rows[s.idx]
	i := s.idx
	s.idx++
	return s.loadKeys(i)
}

// loadKeys fills curKeys for the row just taken. `at` is that row's index in
// s.rows when it came from the in-memory tail (so its keys are already known),
// or -1 when it was read from a spill file and must be computed.
func (s *sortSource) loadKeys(at int) error {
	if s.keysOf == nil {
		return nil
	}
	if at >= 0 && at < len(s.keyvals) {
		s.curKeys = s.keyvals[at]
		return nil
	}
	kv, err := s.keysOf(s.cur)
	if err != nil {
		return err
	}
	s.curKeys = kv
	return nil
}

// sortHeap is a min-heap of sortSources keyed by their current row
// under a row-comparator function.
type sortHeap struct {
	sources []*sortSource
	// less compares PRECOMPUTED key values — the same comparator the
	// in-memory sort uses, so the tail and the spill files are merged under
	// one rule (M0134-0191, DESIGN §5.6).
	less   func(a, b []Datum) bool
	keysOf func(Row) ([]Datum, error)
}

func (h *sortHeap) Len() int { return len(h.sources) }
func (h *sortHeap) Less(i, j int) bool {
	return h.less(h.sources[i].curKeys, h.sources[j].curKeys)
}
func (h *sortHeap) Swap(i, j int)      { h.sources[i], h.sources[j] = h.sources[j], h.sources[i] }
func (h *sortHeap) Push(x any)         { h.sources = append(h.sources, x.(*sortSource)) }
func (h *sortHeap) Pop() any {
	n := len(h.sources)
	x := h.sources[n-1]
	h.sources = h.sources[:n-1]
	return x
}
