package executor

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// This file is the Stage-9 (S2c, design bundle D4.2) SubPlan execution
// engine: a per-statement handle that keeps a sublink's inner operator
// tree BUILT across outer rows and re-runs it by the cheapest safe
// mechanism, instead of Build+Open+Close per outer row — the shape
// upstream PostgreSQL never runs (nodeSubplan.c only ever rescans).
//
// It also carries the cacheability gate (the PG-fidelity blocker from
// the bundle review): a correlated sublink whose inner plan contains a
// volatile function or a LockRows node must re-execute per outer row —
// its results may never be served from a value cache. Non-correlated
// sublinks are deliberately NOT gated: an uncorrelated sublink is
// upstream's InitPlan, evaluated once per statement even when volatile
// (make_subplan's initPlan branch has no volatility check), so the
// existing constant-key cache is exactly PG's semantics.

// subPlanRescanOn is the operational kill switch for the whole handle
// mechanism. Default ON; GOOPG_SUBPLAN_RESCAN=off at server start (or
// SetSubPlanRescanEnabled(false) from tests) restores the legacy
// Build/Open/Close-per-call paths, including the CorrSubqOps registry,
// exactly as they were before Stage 9. Same pattern as the planner's
// SetIndexKeyHarvestEnabled.
var subPlanRescanOn atomic.Bool

func init() {
	subPlanRescanOn.Store(os.Getenv("GOOPG_SUBPLAN_RESCAN") != "off")
}

// SetSubPlanRescanEnabled toggles the SubPlan handle engine. Test-only
// API; the operational switch is the environment variable read at init.
func SetSubPlanRescanEnabled(on bool) { subPlanRescanOn.Store(on) }

func subPlanRescanEnabled() bool { return subPlanRescanOn.Load() }

// How a handle re-runs its operator tree for a new outer row.
const (
	// rescanReOpen: call Open again on the already-built tree. Every
	// operator on this path is re-Open-safe: indexScanOp detects the
	// re-open and rescans (pre-existing), seqScanOp rewinds in place
	// (Stage 7), limitOp resets its window state (Stage 7), filterOp
	// and projections are stateless, aggregateOp rebuilds from its
	// child on every Open.
	rescanReOpen = iota
	// rescanCloseOpen: Close, then Open. Used for trees containing an
	// operator whose Open is NOT re-entrant (sortOp appends to its
	// buffer; hash joins / MHJ / NLI / window / distinct hold build
	// state) — Close releases everything, Open rebuilds. Still cheaper
	// than a rebuild: the operator tree itself is reused, only its
	// runtime state is reconstructed.
	rescanCloseOpen
	// rescanRebuild: the plan contains a node this file does not
	// model. Fall back to today's behavior — a fresh Build per call —
	// so an unknown operator's lifecycle assumptions are never
	// violated.
	rescanRebuild
)

// subPlanHandle is the per-sublink execution state, keyed by the
// sublink expression's pointer in Context.SubPlanHandles. Handles live
// for the statement; Context.CloseSubPlans tears them down.
type subPlanHandle struct {
	op        Operator
	plan      planner.Node
	kind      int
	cacheable bool
	opOpen    bool // op has been Opened and not yet Closed
	built     bool // op is non-nil and usable
	forExists bool // apply the lockRowsOp maxDrain=1 EXISTS optimisation
}

// volatileBuiltins are the builtin function names whose result can
// change between two calls WITHIN one statement — PG's provolatile 'v'.
// STABLE builtins (now, current_timestamp, statement_timestamp,
// transaction_timestamp, current_date/time) are deliberately absent:
// they are fixed for the statement, so serving them from a
// per-statement cache is exactly PG's contract for STABLE.
var volatileBuiltins = map[string]bool{
	"random": true, "setseed": true,
	"nextval": true, "currval": true, "lastval": true, "setval": true,
	"clock_timestamp": true, "timeofday": true,
	"gen_random_uuid": true, "gen_random_bytes": true, "uuid_generate_v4": true,
	"pg_sleep": true, "txid_current": true, "pg_notify": true,
}

// subPlanExprVolatile reports whether expression e (recursively,
// including nested sublinks' inner plans) contains a call that must
// re-execute per outer row.
//
// Resolution order per FuncCall name: the hardcoded volatile-builtin
// deny list, then the user-routine registry (Volatile "v", or the
// registry default "" which PG treats as VOLATILE). A name in neither
// place is a plain builtin scalar (substring, abs, coalesce-family, …)
// and is treated as non-volatile: goopg's builtins are not registered
// in the catalog routine registry, and refusing to cache on every
// unknown builtin would disable subquery caching wholesale — the
// opposite of PG, whose builtins carry explicit (overwhelmingly i/s)
// provolatile markings.
func subPlanExprVolatile(e planner.Expr, ctx *Context) bool {
	vol := false
	planner.WalkExprTree(e, func(sub planner.Expr) {
		if vol {
			return
		}
		switch x := sub.(type) {
		case *planner.FuncCall:
			name := strings.ToLower(x.Name)
			if volatileBuiltins[name] {
				vol = true
				return
			}
			if ctx != nil && ctx.Catalog != nil {
				if rs := ctx.Catalog.Routines(); rs != nil {
					for _, r := range rs.LookupByName(parser.ObjectName{Name: name}) {
						// Any overload volatile (or unmarked — the
						// registry default, which PG treats as
						// VOLATILE) taints the name: overload
						// resolution is type-driven and not worth
						// replicating here.
						if r.Volatile == "v" || r.Volatile == "" {
							vol = true
							break
						}
					}
				}
			}
		case *planner.SubqueryExpr:
			if x.Plan != nil && subPlanContainsVolatile(x.Plan, ctx) {
				vol = true
			}
		case *planner.ExistsExpr:
			if x.Plan != nil && subPlanContainsVolatile(x.Plan, ctx) {
				vol = true
			}
		case *planner.InExpr:
			if x.Plan != nil && subPlanContainsVolatile(x.Plan, ctx) {
				vol = true
			}
		}
	})
	return vol
}

// subPlanContainsVolatile walks every expression the planner knows how
// to enumerate on n's tree (planner.WalkPlanExprs) looking for volatile
// calls. Expressions on node kinds WalkPlanExprs does not model are not
// seen — the same blind spot today's unconditional result caching has;
// classifySubPlan independently downgrades unknown NODE kinds to
// rescanRebuild, and result caching for those plans keeps today's
// semantics.
func subPlanContainsVolatile(n planner.Node, ctx *Context) bool {
	vol := false
	planner.WalkPlanExprs(n, func(e planner.Expr) {
		if !vol && subPlanExprVolatile(e, ctx) {
			vol = true
		}
	})
	return vol
}

// classifySubPlan walks the inner plan and decides (rescanKind,
// cacheable).
//
//   - Any node kind outside the modelled set → rescanRebuild (never
//     violate an unknown operator's lifecycle).
//   - LockRows anywhere → rescanCloseOpen AND cacheable=false: row
//     locks must stamp for every qualifying outer row (ch.07 M13's
//     FOR-UPDATE case), so neither results nor the operator's drained
//     state may be reused.
//   - Sort / Distinct / WindowAgg / any join family → rescanCloseOpen
//     (their Open is not re-entrant).
//   - Chains of Filter/Project/Aggregate/Limit over SeqScan/IndexScan/
//     Values/GenerateSeries → rescanReOpen.
//   - A volatile function anywhere (incl. nested sublinks) →
//     cacheable=false.
func classifySubPlan(n planner.Node, ctx *Context) (kind int, cacheable bool) {
	kind = rescanReOpen
	cacheable = true
	var walk func(planner.Node)
	walk = func(node planner.Node) {
		if node == nil || kind == rescanRebuild {
			return
		}
		switch x := node.(type) {
		case *planner.Filter:
			walk(x.Child)
		case *planner.Project:
			walk(x.Child)
		case *planner.Aggregate:
			walk(x.Child)
		case *planner.Limit:
			walk(x.Child)
		case *planner.Sort:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			walk(x.Child)
		case *planner.Distinct:
			// Re-Open-safe since the Stage-9 reset in distinctOp.Open
			// (rows/idx cleared, child re-drained); the tree's kind is
			// then decided by the child.
			walk(x.Child)
		case *planner.WindowAgg:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			walk(x.Child)
		case *planner.LockRows:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			cacheable = false
			walk(x.Child)
		case *planner.Join:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			walk(x.Left)
			walk(x.Right)
		case *planner.MultiHashJoin:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			for _, t := range x.Tables {
				walk(t)
			}
		case *planner.NestedLoopIndexJoin:
			if kind == rescanReOpen {
				kind = rescanCloseOpen
			}
			walk(x.Outer)
			walk(x.Inner)
		case *planner.SeqScan, *planner.IndexScan, *planner.IndexOnlyScan,
			*planner.Values, *planner.GenerateSeries, *planner.GenerateSubscripts:
			// Leaves. IndexOnlyScan re-Open safety has not been
			// audited; keep it conservative.
			if _, ios := node.(*planner.IndexOnlyScan); ios && kind == rescanReOpen {
				kind = rescanCloseOpen
			}
		default:
			kind = rescanRebuild
		}
	}
	walk(n)
	if cacheable && subPlanContainsVolatile(n, ctx) {
		cacheable = false
	}
	return kind, cacheable
}

// getSubPlanHandle returns (allocating on first use) the handle for
// sublink key whose inner plan is plan.
func getSubPlanHandle(ctx *Context, key planner.Expr, plan planner.Node, forExists bool) *subPlanHandle {
	if ctx.SubPlanHandles == nil {
		ctx.SubPlanHandles = make(map[planner.Expr]*subPlanHandle)
	}
	h, ok := ctx.SubPlanHandles[key]
	if !ok {
		kind, cacheable := classifySubPlan(plan, ctx)
		h = &subPlanHandle{plan: plan, kind: kind, cacheable: cacheable, forExists: forExists}
		ctx.SubPlanHandles[key] = h
	}
	return h
}

// subPlanResultCacheable reports whether results of the sublink keyed
// by key (with inner plan `plan`) may be served from a value cache.
// Only meaningful for CORRELATED sublinks — non-correlated ones follow
// InitPlan semantics and cache unconditionally (see the file comment).
// On the legacy path (kill switch off) this always reports true, which
// is exactly the pre-Stage-9 behavior.
func subPlanResultCacheable(ctx *Context, key planner.Expr, plan planner.Node, forExists bool) bool {
	if !subPlanRescanEnabled() {
		return true
	}
	return getSubPlanHandle(ctx, key, plan, forExists).cacheable
}

// acquireSubPlanOp returns an OPEN operator for the sublink's inner
// plan plus a done() the caller must invoke after draining.
//
// Handle path (kill switch on): the operator is built once per
// statement and re-run per the handle's rescanKind; done() is a no-op —
// the operator stays open, owned by the handle, until CloseSubPlans.
// Counters: the first acquisition and every rescanRebuild count as
// Rebuilds; a reOpen or Close+Open of the retained tree counts as
// Rescans (no Build() ran — Close+Open reconstructs runtime state only,
// which is what upstream's ExecReScan does for hash state too).
//
// Legacy path (kill switch off): Build+Open per call, done() Closes —
// byte-for-byte the pre-Stage-9 lifecycle.
func acquireSubPlanOp(ctx *Context, key planner.Expr, plan planner.Node, forExists bool) (Operator, func(), error) {
	stat := ctx.subPlanStat(key)
	if !subPlanRescanEnabled() {
		stat.Rebuilds++
		op, err := Build(plan)
		if err != nil {
			return nil, nil, err
		}
		if forExists {
			if lop, ok := op.(*lockRowsOp); ok {
				lop.maxDrain = 1
			}
		}
		if err := op.Open(ctx); err != nil {
			_ = op.Close()
			return nil, nil, err
		}
		return op, func() { _ = op.Close() }, nil
	}

	h := getSubPlanHandle(ctx, key, plan, forExists)
	noop := func() {}
	if !h.built {
		op, err := Build(plan)
		if err != nil {
			return nil, nil, err
		}
		if forExists {
			if lop, ok := op.(*lockRowsOp); ok {
				lop.maxDrain = 1
			}
		}
		h.op = op
		h.built = true
		h.opOpen = false
		stat.Rebuilds++
		if err := h.op.Open(ctx); err != nil {
			_ = h.op.Close()
			h.built = false
			h.op = nil
			return nil, nil, err
		}
		h.opOpen = true
		return h.op, noop, nil
	}

	switch h.kind {
	case rescanReOpen:
		stat.Rescans++
		if err := h.op.Open(ctx); err != nil {
			h.close()
			return nil, nil, err
		}
		h.opOpen = true
	case rescanCloseOpen:
		stat.Rescans++
		if h.opOpen {
			_ = h.op.Close()
			h.opOpen = false
		}
		if err := h.op.Open(ctx); err != nil {
			h.close()
			return nil, nil, err
		}
		h.opOpen = true
	default: // rescanRebuild
		h.close()
		stat.Rebuilds++
		op, err := Build(plan)
		if err != nil {
			return nil, nil, err
		}
		if forExists {
			if lop, ok := op.(*lockRowsOp); ok {
				lop.maxDrain = 1
			}
		}
		h.op = op
		h.built = true
		if err := h.op.Open(ctx); err != nil {
			_ = h.op.Close()
			h.built = false
			h.op = nil
			return nil, nil, err
		}
		h.opOpen = true
	}
	return h.op, noop, nil
}

// close tears down the handle's operator (idempotent).
func (h *subPlanHandle) close() {
	if h.built && h.op != nil {
		_ = h.op.Close()
	}
	h.op = nil
	h.built = false
	h.opOpen = false
}

// CloseSubPlans closes every SubPlan handle's operator tree and clears
// the table. Called at the statement-dispatch teardown seam (simple and
// extended protocol paths); Operator.Close never releases heavyweight
// locks (design bundle ch.04 §10.5), so this is lock-safe at any point
// after execution. Idempotent and nil-safe.
func (c *Context) CloseSubPlans() {
	if c == nil || c.SubPlanHandles == nil {
		return
	}
	for _, h := range c.SubPlanHandles {
		h.close()
	}
	c.SubPlanHandles = nil
}
