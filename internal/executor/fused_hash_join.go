// Package executor — runtime hash-join-cascade fusion.
//
// M0126-0006 Stage 1: scaffolding, decision function, differential
// harness. The kill switch GOOPG_RUNTIME_JOIN_FUSION defaults OFF so
// production behaviour is bit-identical to the pre-task run by
// construction.
//
// Design of record: analysis/cost-driven-second-try-200731/
//   04-fusion-site-and-data-structures.md (site + data structures)
//   05-qualification-predicate.md (Q0-Q9 predicate)
//   10-rollback-and-kill-switches.md (KS1/KS2)

package executor

import (
	"os"
	"strconv"

	"github.com/goopg/goopg/internal/planner"
)

// ---- buildEnv — per-build context threaded through Build/buildRec ----

type buildEnv struct {
	root      planner.Node
	inWorker  bool
	fusionCfg fusionConfig

	q0 struct {
		ran         bool
		hasLockRows bool
		hasGather   bool
		hasMHJ      bool
	}
}

var buildEnvInFlight *buildEnv

// ---- kill switches (bundle 10) ----

type fusionConfig struct {
	enabled   bool
	minLevels int
}

func readFusionConfig() fusionConfig {
	cfg := fusionConfig{minLevels: 3}
	if v := os.Getenv("GOOPG_RUNTIME_JOIN_FUSION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.enabled = b
		} else if iv, err := strconv.Atoi(v); err == nil && iv != 0 {
			cfg.enabled = true
		}
	}
	if v := os.Getenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.minLevels = n
		}
	}
	return cfg
}

// ---- fusedLevel / fusedHashJoinOp ----

type fusedLevel struct {
	plan     *planner.Join
	probeKey *planner.ColumnRef
	buildKey *planner.ColumnRef
	width    int
	offset   int
}

type fusedHashJoinOp struct {
	levels  []fusedLevel
	probeOp Operator
	schema  planner.Schema
}

func (o *fusedHashJoinOp) Open(ctx *Context) error { return nil }
func (o *fusedHashJoinOp) Next() (TupleSlot, error)  { return nil, nil }
func (o *fusedHashJoinOp) Close() error               { return nil }
func (o *fusedHashJoinOp) Schema() planner.Schema     { return o.schema }

// ---- tryFuseHashCascade — the qualification predicate + builder ----

func tryFuseHashCascade(env *buildEnv, p *planner.Join) (Operator, bool) {
	if env == nil || !env.fusionCfg.enabled {
		return nil, false
	}

	// Q0 — global preconditions (memoised once per Build).
	if !env.q0.ran {
		env.q0.hasLockRows, env.q0.hasGather, env.q0.hasMHJ =
			walkRootForQ0(env.root)
		env.q0.ran = true
	}
	if env.inWorker {
		return nil, false // C10/F4
	}
	if env.q0.hasLockRows {
		return nil, false // C9
	}
	if env.q0.hasMHJ {
		return nil, false
	}
	if instrumentScope != nil {
		return nil, false // C11/C12 (F8)
	}

	// Q1/Q2 — collect the left-deep chain with per-level whitelist.
	var levels []fusedLevel
	runningWidth := 0
	for cur := p; ; {
		if cur.Type != planner.JoinTypeInner ||
			cur.Algo != planner.JoinAlgoHash ||
			cur.Lateral ||
			cur.NullAware ||
			cur.UsingLeftCols != nil ||
			cur.UsingRightCols != nil ||
			cur.LeftKey == nil ||
			cur.RightKey == nil {
			break
		}
		if cur.BuildLeft {
			break
		}

		lk, lok := cur.LeftKey.(*planner.ColumnRef)
		rk, rok := cur.RightKey.(*planner.ColumnRef)
		if !lok || !rok {
			break
		}

		rw := len(cur.Right.Output())
		if lk.Index < 0 || lk.Index >= runningWidth+rw {
			break
		}
		if rk.Index < 0 || rk.Index >= rw {
			break
		}

		// Q6 structural assertions.
		if len(cur.Output()) != len(cur.Left.Output())+rw {
			break
		}
		if len(cur.Left.Output()) != runningWidth {
			break
		}
		if !outputMatchesChildren(cur) {
			break
		}

		// Q4 residual: every ColumnRef must be in the bound prefix.
		if !residualInBound(cur, runningWidth+rw) {
			break
		}

		levels = append(levels, fusedLevel{
			plan:     cur,
			probeKey: lk,
			buildKey: rk,
			width:    rw,
			offset:   runningWidth,
		})
		runningWidth += rw

		next, ok := cur.Left.(*planner.Join)
		if !ok {
			break
		}
		cur = next
	}

	if len(levels) < env.fusionCfg.minLevels {
		return nil, false
	}

	return &fusedHashJoinOp{
		levels:  levels,
		probeOp: nil,
		schema:  levels[len(levels)-1].plan.Output(),
	}, true
}

// ---- predicate helpers ----

func walkRootForQ0(n planner.Node) (hasLockRows, hasGather, hasMHJ bool) {
	if n == nil {
		return
	}
	switch n.(type) {
	case *planner.LockRows:
		return true, false, false
	case *planner.Gather, *planner.GatherMerge:
		return false, true, false
	case *planner.MultiHashJoin:
		return false, false, true
	}
	for _, child := range nodeChildren(n) {
		lr, ga, mhj := walkRootForQ0(child)
		hasLockRows = hasLockRows || lr
		hasGather = hasGather || ga
		hasMHJ = hasMHJ || mhj
	}
	return
}

func nodeChildren(n planner.Node) []planner.Node {
	switch x := n.(type) {
	case *planner.Join:
		return []planner.Node{x.Left, x.Right}
	case *planner.Filter:
		return []planner.Node{x.Child}
	case *planner.Project:
		return []planner.Node{x.Child}
	case *planner.Sort:
		return []planner.Node{x.Child}
	case *planner.Aggregate:
		return []planner.Node{x.Child}
	case *planner.Limit:
		return []planner.Node{x.Child}
	case *planner.Gather:
		return []planner.Node{x.Child}
	case *planner.GatherMerge:
		return []planner.Node{x.Child}
	case *planner.LockRows:
		return []planner.Node{x.Child}
	case *planner.Distinct:
		return []planner.Node{x.Child}
	case *planner.WindowAgg:
		return []planner.Node{x.Child}
	case *planner.Memoize:
		return []planner.Node{x.Child}
	case *planner.OrdinalityWrap:
		return []planner.Node{x.Child}
	case *planner.CTEScan:
		return []planner.Node{x.Child}
	case *planner.CTEDMLPrefix:
		return []planner.Node{x.Body}
	case *planner.SetOp:
		return []planner.Node{x.Left, x.Right}
	case *planner.MultiHashJoin:
		return x.Tables
	case *planner.NestedLoopIndexJoin:
		return []planner.Node{x.Outer, x.Inner}
	case *planner.Insert:
		return []planner.Node{x.Source}
	case *planner.Explain:
		return []planner.Node{x.Child}
	}
	return nil
}

func outputMatchesChildren(j *planner.Join) bool {
	lw := len(j.Left.Output())
	for c, col := range j.Output() {
		var want planner.SchemaColumn
		if c < lw {
			want = j.Left.Output()[c]
		} else {
			want = j.Right.Output()[c-lw]
		}
		if col.Name != want.Name || col.Type.Name != want.Type.Name {
			return false
		}
	}
	return true
}

func residualInBound(j *planner.Join, bound int) bool {
	if j.Predicate == nil {
		return true
	}
	for _, c := range planner.SplitAnd(j.Predicate) {
		if planner.IsCanonicalKeyEquality(c, j.LeftKey, j.RightKey) {
			continue
		}
		if !exprRefsInBound(c, bound) {
			return false
		}
	}
	return true
}

func exprRefsInBound(e planner.Expr, bound int) bool {
	ok := true
	walkExpr(e, func(x planner.Expr) bool {
		switch x := x.(type) {
		case *planner.ColumnRef:
			if x.Index < 0 || x.Index >= bound {
				ok = false
				return false
			}
		case *planner.OuterColumnRef:
			ok = false
			return false
		case *planner.SubqueryExpr, *planner.ExistsExpr,
			*planner.InExpr, *planner.MultiAssignSubqElem:
			ok = false
			return false
		}
		return true
	})
	return ok
}

func walkExpr(e planner.Expr, fn func(planner.Expr) bool) {
	if e == nil {
		return
	}
	if !fn(e) {
		return
	}
	for _, child := range exprChildren(e) {
		walkExpr(child, fn)
	}
}

func exprChildren(e planner.Expr) []planner.Expr {
	switch x := e.(type) {
	case *planner.BinaryOp:
		return []planner.Expr{x.Left, x.Right}
	case *planner.UnaryOp:
		return []planner.Expr{x.Operand}
	case *planner.CastExpr:
		return []planner.Expr{x.Operand}
	case *planner.IsNullExpr:
		return []planner.Expr{x.Operand}
	case *planner.IsBoolExpr:
		return []planner.Expr{x.Operand}
	case *planner.IsDistinctFromExpr:
		return []planner.Expr{x.Left, x.Right}
	case *planner.CollateExpr:
		return []planner.Expr{x.Operand}
	case *planner.FuncCall:
		return x.Args
	case *planner.InExpr:
		return append([]planner.Expr{x.Operand}, x.List...)
	}
	return nil
}
