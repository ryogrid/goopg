// Package executor — runtime hash-join-cascade fusion.
//
// M0126-0006 Stage 1: scaffolding, decision function, differential
// harness. The kill switch GOOPG_RUNTIME_JOIN_FUSION defaults OFF so
// production behaviour is bit-identical to the pre-task run by
// construction.
//
// Design of record: analysis/cost-driven-second-try-200731/
//
//	04-fusion-site-and-data-structures.md (site + data structures)
//	05-qualification-predicate.md (Q0-Q9 predicate)
//	10-rollback-and-kill-switches.md (KS1/KS2)

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
	// Static (populated by tryFuseHashCascade)
	plan     *planner.Join
	probeKey *planner.ColumnRef // plan.LeftKey
	buildKey *planner.ColumnRef // plan.RightKey
	width    int                // len(plan.Right.Output())
	offset   int                // absolute offset of this build side in the top schema
	residual []planner.Expr     // Predicate conjuncts minus the canonical key equality

	// Built by tryFuseHashCascade (child Build of plan.Right)
	buildOp Operator

	// Hash table (populated in Open, read-only during Next)
	ht      map[string][]Row
	intHT   map[int64][]Row
	htIsInt bool

	// Rebound per emitted match; one source in the output VirtualSlot.
	slot *MaterializedSlot

	// Odometer state (mutates per probe row)
	matches []Row
	cursor  int
}

type fusedHashJoinOp struct {
	levels       []fusedLevel      // [0] = innermost, [k-1] = outermost (p)
	probeOp      Operator          // built probe subtree
	probeMatSlot *MaterializedSlot // rebound per probe row; source[0] in out
	out          *VirtualSlot      // composed output: [probe, build[0], ..., build[k-1]]
	schema       planner.Schema    // == levels[k-1].plan.Output()
	ctx          *Context

	// Odometer
	active     bool
	curLevel   int // current level being advanced (-1 = back off past 0)
	probeWidth int // len(probeOp.Schema())
	stepCount  int // cancellation check every 4096 steps
}

func (o *fusedHashJoinOp) Schema() planner.Schema { return o.schema }

func (o *fusedHashJoinOp) Close() error {
	var firstErr error
	if o.probeOp != nil {
		if err := o.probeOp.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for i := range o.levels {
		if o.levels[i].buildOp != nil {
			if err := o.levels[i].buildOp.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	o.ctx = nil
	o.probeOp = nil
	o.out = nil
	for i := range o.levels {
		o.levels[i].ht = nil
		o.levels[i].intHT = nil
		o.levels[i].slot = nil
		o.levels[i].matches = nil
	}
	return firstErr
}

// ---- Open — build hash tables for every level, then open the probe ----

func (o *fusedHashJoinOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.probeWidth = len(o.probeOp.Schema())

	// Build hash tables innermost-first (levels[0] … levels[k-1]).
	for i := range o.levels {
		l := &o.levels[i]
		if err := l.buildOp.Open(ctx); err != nil {
			return err
		}
		budget := ctx.WorkMem
		if budget <= 0 {
			budget = 512 * 1024 * 1024 // default 512 MiB
		}
		bounded, err := drainRowsBounded(l.buildOp, budget)
		_ = l.buildOp.Close()
		if err != nil {
			return err
		}
		if err := bounded.Open(ctx); err != nil {
			return err
		}
		// Drain build rows into the hash table.
		l.ht = make(map[string][]Row)
		l.intHT = make(map[int64][]Row)
		allInt64 := true
		// runningWidth for this level's key evaluation =
		// probeWidth + sum(widths[0..i-1]) = l.offset
		runningWidth := l.offset
		buildCount := 0
		for {
			// C7: ctx cancellation every 4096 rows.
			if buildCount&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
				if err := ctx.Ctx.Err(); err != nil {
					_ = bounded.Close()
					return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
				}
			}
			buildCount++
			rSlot, err := bounded.Next()
			if err == EOF {
				break
			}
			if err != nil {
				_ = bounded.Close()
				return err
			}
			r := slotRow(rSlot)

			// Evaluate the build key against {nullLeft, realBuildRow}.
			keySlot := mergedKeySlot(rSlot, l.width, runningWidth, false)
			kd, ok, kerr := evalKeyDatum(l.buildKey, keySlot, ctx)
			if kerr != nil {
				_ = bounded.Close()
				return kerr
			}
			if !ok {
				continue // NULL key → skip (hash join semantics)
			}

			// Insert into hash table (int64 fast-path aware).
			sk := datumKey(kd)
			l.ht[sk] = append(l.ht[sk], r)
			if allInt64 {
				if ik, iok := datumToInt64Key(kd); iok {
					l.intHT[ik] = append(l.intHT[ik], r)
				} else {
					allInt64 = false
					l.intHT = nil
				}
			}
		}
		_ = bounded.Close()

		// Finalize: keep the int64 table only when every key was int64.
		if allInt64 && len(l.intHT) > 0 {
			l.htIsInt = true
			l.ht = nil
		} else {
			l.intHT = nil
		}

		// Allocate the per-level MaterializedSlot (rebound per match in Next).
		l.slot = SlotFromRow(nil, nil)
	}

	// Build the output VirtualSlot.
	// Sources: [probeMatSlot, levels[0].slot, ..., levels[k-1].slot]
	totalCols := o.probeWidth
	for i := range o.levels {
		totalCols += o.levels[i].width
	}
	sources := make([]TupleSlot, 1+len(o.levels))
	o.probeMatSlot = SlotFromRow(nil, nil)
	sources[0] = o.probeMatSlot
	for i := range o.levels {
		sources[1+i] = o.levels[i].slot
	}
	cols := make([]virtualCol, totalCols)
	ci := 0
	for i := 0; i < o.probeWidth; i++ {
		cols[ci] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
		ci++
	}
	for li := range o.levels {
		for i := 0; i < o.levels[li].width; i++ {
			cols[ci] = virtualCol{sourceIdx: int16(1 + li), sourceCol: int16(i)}
			ci++
		}
	}
	o.out = NewVirtualSlot(o.schema, sources, cols)

	// Open the probe side.
	if err := o.probeOp.Open(ctx); err != nil {
		return err
	}

	return nil
}

// ---- Next — the odometer ----

func (o *fusedHashJoinOp) Next() (TupleSlot, error) {
	// C7: cancellation check every 4096 odometer steps.
	o.stepCount++
	if o.stepCount&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
		if err := o.ctx.Ctx.Err(); err != nil {
			return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
		}
	}

	for {
		if !o.active {
			// Pull next probe row.
			probeSlot, err := o.probeOp.Next()
			if err == EOF {
				return nil, EOF
			}
			if err != nil {
				return nil, err
			}
			o.probeMatSlot.row = slotRow(probeSlot)

			// Look up level 0 (innermost) hash table.
			keySlot := mergedKeySlot(probeSlot, o.probeWidth, o.levels[0].width, true)
			kd, ok, kerr := evalKeyDatum(o.levels[0].probeKey, keySlot, o.ctx)
			if kerr != nil {
				return nil, kerr
			}
			o.levels[0].matches = nil
			o.levels[0].cursor = 0
			if ok {
				o.levels[0].matches = fusedHashLookup(&o.levels[0], kd)
			}
			o.active = true
			o.curLevel = 0
		}

		// Back off past level 0: probe row exhausted.
		if o.curLevel < 0 {
			o.active = false
			continue
		}

		// Try to advance at the current level.
		cur := &o.levels[o.curLevel]
		if cur.cursor >= len(cur.matches) {
			// Matches exhausted at this level → back off.
			o.curLevel--
			continue
		}

		// Bind next match at this level.
		match := cur.matches[cur.cursor]
		cur.cursor++
		cur.slot.row = match

		// Evaluate residual predicate at this level.
		if len(cur.residual) > 0 {
			pass, rerr := evalResidual(cur.residual, o.out, o.ctx)
			if rerr != nil {
				return nil, rerr
			}
			if !pass {
				continue // try next match at same level
			}
		}

		if o.curLevel == len(o.levels)-1 {
			// Outermost level: emit.
			return o.out, nil
		}

		// Descend to the next level: compute key from the output so far
		// and look up the next build side's hash table.
		next := &o.levels[o.curLevel+1]
		kd, ok, kerr := evalKeyDatum(next.probeKey, o.out, o.ctx)
		if kerr != nil {
			return nil, kerr
		}
		next.matches = nil
		next.cursor = 0
		if ok {
			next.matches = fusedHashLookup(next, kd)
		}
		o.curLevel++
		// Loop: try matches at the newly descended level.
	}
}

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
	if p == nil {
		return nil, false
	}

	// Q1/Q2 — collect candidate joins top-down, then validate bottom-up.
	// The chain is left-deep: p is the outermost join, p.Left is the next,
	// ..., deepest .Left is the probe subtree.
	type candidate struct {
		join *planner.Join
		rw   int // len(Right.Output())
	}
	var cand []candidate
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
		_, lok := cur.LeftKey.(*planner.ColumnRef)
		_, rok := cur.RightKey.(*planner.ColumnRef)
		if !lok || !rok {
			break
		}
		rw := len(cur.Right.Output())
		if rw == 0 {
			break
		}
		// Q6 width identity (cheap check, run top-down to fail early).
		if len(cur.Output()) != len(cur.Left.Output())+rw {
			break
		}
		cand = append(cand, candidate{join: cur, rw: rw})

		next, ok := cur.Left.(*planner.Join)
		if !ok {
			break
		}
		cur = next
	}

	if len(cand) < env.fusionCfg.minLevels {
		return nil, false
	}

	// Bottom-up validation: the innermost join's Left is the probe subtree.
	probePlan := cand[len(cand)-1].join.Left
	probeWidth := len(probePlan.Output())
	if probeWidth == 0 {
		// Degenerate: probe has zero columns. Shouldn't happen but guard.
		return nil, false
	}

	// Build levels bottom-up (innermost first → levels[0]).
	levels := make([]fusedLevel, len(cand))
	runningWidth := probeWidth
	for i := len(cand) - 1; i >= 0; i-- {
		cur := cand[i].join
		rw := cand[i].rw
		li := len(cand) - 1 - i // levels index: 0 = innermost

		lk := cur.LeftKey.(*planner.ColumnRef)
		rk := cur.RightKey.(*planner.ColumnRef)

		// Q3: key indices in bound.
		if lk.Index < 0 || lk.Index >= runningWidth {
			return nil, false
		}
		if rk.Index < 0 || rk.Index >= rw {
			return nil, false
		}

		// Q6: structural assertions (width check already done top-down,
		// but re-verify len(Left.Output()) bottom-up since it depends
		// on runningWidth).
		if len(cur.Left.Output()) != runningWidth {
			return nil, false
		}
		if !outputMatchesChildren(cur) {
			return nil, false
		}

		// Q4: residual conjuncts in bound prefix.
		if !residualInBound(cur, runningWidth+rw) {
			return nil, false
		}

		// Extract non-key residual conjuncts.
		var residual []planner.Expr
		if cur.Predicate != nil {
			for _, c := range planner.SplitAnd(cur.Predicate) {
				if planner.IsCanonicalKeyEquality(c, cur.LeftKey, cur.RightKey) {
					continue
				}
				residual = append(residual, c)
			}
		}

		levels[li] = fusedLevel{
			plan:     cur,
			probeKey: lk,
			buildKey: rk,
			width:    rw,
			offset:   runningWidth,
			residual: residual,
		}
		runningWidth += rw
	}

	// Build the probe subtree.
	probeOp, err := Build(probePlan)
	if err != nil {
		return nil, false
	}

	// Build each build-side subtree.
	for i := range levels {
		buildOp, err := Build(levels[i].plan.Right)
		if err != nil {
			return nil, false
		}
		levels[i].buildOp = buildOp
	}

	return &fusedHashJoinOp{
		levels:  levels,
		probeOp: probeOp,
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

// ---- runtime helpers (called from Open / Next) ----

// evalKeyDatum evaluates a hash-key expression against a slot and returns
// the Datum. ok=false means NULL key (skip in both build and probe).
func evalKeyDatum(keyExpr planner.Expr, slot SlotView, ctx *Context) (Datum, bool, error) {
	v, err := evalExprSlot(keyExpr, slot, ctx)
	if err != nil {
		return Datum{}, false, err
	}
	if v.IsNull() {
		return Datum{}, false, nil
	}
	return v, true, nil
}

// evalResidual returns true when every conjunct evaluates to true (non-null
// boolean true) against the slot. nil/empty conjuncts → true.
func evalResidual(conjuncts []planner.Expr, slot SlotView, ctx *Context) (bool, error) {
	for _, c := range conjuncts {
		v, err := evalExprSlot(c, slot, ctx)
		if err != nil {
			return false, err
		}
		if v.IsNull() || v.Kind != KindBool || !v.BoolValue() {
			return false, nil
		}
	}
	return true, nil
}

// fusedHashLookup returns the match list for keyDatum from the level's hash
// table, respecting the int64 fast-path.
func fusedHashLookup(l *fusedLevel, keyDatum Datum) []Row {
	if l.htIsInt {
		if ik, ok := datumToInt64Key(keyDatum); ok {
			return l.intHT[ik]
		}
		return nil
	}
	return l.ht[datumKey(keyDatum)]
}
