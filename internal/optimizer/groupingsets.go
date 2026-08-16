package optimizer

// GROUP BY GROUPING SETS / ROLLUP / CUBE, lowered the way PostgreSQL lowers
// it (M0125-0048).
//
// SQL:1999 §7.9 *defines* a grouping-sets clause as the UNION ALL of one
// ordinary GROUP BY per listed set, and goopg used to execute that definition
// literally: rewriteGroupingSets built N sibling SelectStmts and threaded them
// through s.SetOp. That is a correct reading of the standard and a poor
// reading of PostgreSQL. Upstream computes every level in ONE pass over the
// source with one hash table per hashable set — nodeAgg.c's AGG_HASHED /
// AGG_MIXED, set up by preprocess_grouping_sets and consider_groupingsets_paths
// in optimizer/plan/planner.c — so the source is read once no matter how many
// sets there are, and the grand-total level is a grouping set (`Group Key: ()`)
// rather than a separate branch of a union.
//
// The N-branch expansion had two costs beyond the shape of EXPLAIN. It scanned
// the source once per set, which M0125-0040 papered over by hoisting FROM+WHERE
// into a synthetic materialized CTE — trading time for a full buffer of the
// source. And GROUPING(...) had to be resolved at plan time to a per-branch
// integer literal, because each branch is a different query with a different
// active set.
//
// This file replaces both. The planner keeps ONE Aggregate node whose
// GroupExprs are the deduplicated union of every set's expressions (PG's
// `parse->groupClause`) and whose GroupingSets lists, per set, which of those
// columns are active. GROUPING(...) becomes an output column of that node
// carrying a per-set bitmask, so the target list resolves it to a plain
// ColumnRef — the analogue of PG evaluating a GroupingFunc from
// AggState->current_set.

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// prepareGroupingSets rewrites s.GroupBy into the deduplicated union of every
// expression named by any grouping set, in first-mention order. The parser
// already leaves a flattened list there, but it is the *concatenation* of the
// grouping units, so `GROUPING SETS ((a),(a,b))` repeats `a` — which would
// give the aggregate two output slots for one column and trip
// buildAggregateStage's M0125-0044 ambiguity guard. PostgreSQL's groupClause
// is deduplicated by sortgroupref for the same reason.
//
// The rewrite is idempotent: planSelect re-enters on the head operand of a
// set-op chain (see M0125-0050), so it must not run twice on one statement.
func prepareGroupingSets(s *parser.SelectStmt) {
	spec := s.GroupingSets
	if spec == nil || spec.Flattened {
		return
	}
	spec.Flattened = true
	if len(spec.Sets) == 0 {
		// `GROUP BY ()` with nothing else: an ungrouped aggregate.
		s.GroupBy = nil
		s.GroupingSets = nil
		return
	}
	seen := map[string]bool{}
	union := make([]parser.Expr, 0, len(s.GroupBy))
	for _, set := range spec.Sets {
		for _, e := range set {
			k := qualifiedGroupKey(resolveOrderBySubstitution(e, s.Targets))
			if seen[k] {
				continue
			}
			seen[k] = true
			union = append(union, e)
		}
	}
	s.GroupBy = union
}

// groupExprSlot finds the aggregate output slot a grouping-set member (or a
// GROUPING argument) occupies. It consults the qualifier-aware key first for
// the M0125-0044 reason — every alias of a self-joined table shares the blind
// key — and falls back to the blind one, which is what an unqualified SELECT
// reference matches on.
func groupExprSlot(e parser.Expr, targets []parser.ResTarget, byExpr, byQual map[string]int) (int, bool) {
	g := resolveOrderBySubstitution(e, targets)
	if idx, ok := byQual[qualifiedGroupKey(g)]; ok {
		return idx, true
	}
	idx, ok := byExpr[parserExprKey(g)]
	return idx, ok
}

// groupingSetIndices maps each expanded grouping set onto the ascending list
// of GroupExprs slots active in it. The empty set (the grand total) maps to an
// empty — not nil — slice, so the executor can tell "no active columns" from
// "not a grouping-sets aggregate".
func groupingSetIndices(spec *parser.GroupingSetsSpec, targets []parser.ResTarget, byExpr, byQual map[string]int) ([][]int, error) {
	out := make([][]int, 0, len(spec.Sets))
	for _, set := range spec.Sets {
		idxs := make([]int, 0, len(set))
		seen := map[int]bool{}
		for _, e := range set {
			idx, ok := groupExprSlot(e, targets, byExpr, byQual)
			if !ok {
				// Unreachable via the parser — prepareGroupingSets built
				// s.GroupBy from these very expressions — but a silent
				// mis-map here would be a wrong answer, not a crash, so it
				// is stated rather than assumed.
				return nil, &PlanError{
					Pos:     e.Pos(),
					Code:    "XX000",
					Message: "internal error: grouping set member is not a GROUP BY expression",
				}
			}
			if seen[idx] {
				continue
			}
			seen[idx] = true
			idxs = append(idxs, idx)
		}
		sortIntsAscending(idxs)
		out = append(out, idxs)
	}
	return out, nil
}

// commonGroupingSlots returns the group-expression slots present in EVERY
// grouping set — PostgreSQL's gset_common (parse_agg.c parseCheckAggregates).
// It is the only part of the GROUP BY list a functional dependency may be
// proven against, because a level that drops a column cannot carry a value
// determined by it. Always non-nil, so a caller can tell "grouping sets, empty
// intersection" from "no grouping sets".
func commonGroupingSlots(sets [][]int) map[int]bool {
	common := map[int]bool{}
	if len(sets) == 0 {
		return common
	}
	for _, idx := range sets[0] {
		common[idx] = true
	}
	for _, set := range sets[1:] {
		in := make(map[int]bool, len(set))
		for _, idx := range set {
			in[idx] = true
		}
		for idx := range common {
			if !in[idx] {
				delete(common, idx)
			}
		}
	}
	return common
}

func sortIntsAscending(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// groupingCallMasks computes GROUPING(args...)'s value for every grouping set:
// bit i counting from the RIGHTMOST argument is 1 iff that argument is not
// part of the set that produced the row (PostgreSQL's GroupingFunc, see
// src/backend/executor/execExprInterp.c ExecEvalGroupingFunc and the
// "GROUPING" entry in the functions-aggregate documentation).
//
// PostgreSQL rejects an argument that is not a grouping expression of this
// query level with 42803; so does this.
func groupingCallMasks(gc *parser.GroupingCall, sets [][]int, targets []parser.ResTarget, byExpr, byQual map[string]int) ([]int64, error) {
	slots := make([]int, len(gc.Args))
	for i, a := range gc.Args {
		idx, ok := groupExprSlot(a, targets, byExpr, byQual)
		if !ok {
			return nil, &PlanError{
				Pos:     a.Pos(),
				Code:    "42803",
				Message: "arguments to GROUPING must be grouping expressions of the associated query level",
			}
		}
		slots[i] = idx
	}
	masks := make([]int64, len(sets))
	for si, set := range sets {
		active := make(map[int]bool, len(set))
		for _, idx := range set {
			active[idx] = true
		}
		var mask int64
		n := len(slots)
		for i, idx := range slots {
			if !active[idx] {
				mask |= int64(1) << uint(n-1-i)
			}
		}
		masks[si] = mask
	}
	return masks, nil
}

// collectGroupingCalls returns the distinct GROUPING(...) calls written
// anywhere in the statement's target list, HAVING, or ORDER BY, in first-
// mention order. They have to be found BEFORE the target list is resolved,
// because each one takes an output column of the Aggregate node and
// functionally-determined passthrough columns — discovered during that
// resolution — are appended after them.
//
// The walk is reflective for the reason appendRefQualifiers is: a hand-written
// switch over the ~40 parser expression types would silently miss a GROUPING
// call nested in whichever shape it forgot, and a missed call is a plan error
// at resolution time rather than a wrong answer.
func collectGroupingCalls(s *parser.SelectStmt) []*parser.GroupingCall {
	var out []*parser.GroupingCall
	seen := map[string]bool{}
	for _, t := range s.Targets {
		appendGroupingCalls(&out, seen, reflect.ValueOf(t.Expr), 0)
	}
	if s.Having != nil {
		appendGroupingCalls(&out, seen, reflect.ValueOf(s.Having), 0)
	}
	for _, ob := range s.OrderBy {
		appendGroupingCalls(&out, seen, reflect.ValueOf(ob.Expr), 0)
	}
	return out
}

func appendGroupingCalls(out *[]*parser.GroupingCall, seen map[string]bool, v reflect.Value, depth int) {
	if depth > maxStructuralKeyDepth || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		if _, ok := v.Interface().(*parser.SelectStmt); ok {
			// A sub-SELECT is its own query level: a GROUPING call inside it
			// belongs to that level's grouping sets, not to this one.
			return
		}
		if gc, ok := v.Interface().(*parser.GroupingCall); ok {
			k := groupingCallKey(gc)
			if !seen[k] {
				seen[k] = true
				*out = append(*out, gc)
			}
			return
		}
		appendGroupingCalls(out, seen, v.Elem(), depth+1)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			appendGroupingCalls(out, seen, v.Field(i), depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			appendGroupingCalls(out, seen, v.Index(i), depth+1)
		}
	}
}

// groupingCallKey identifies a GROUPING(...) call by its argument list, so the
// same call written in the SELECT list and in HAVING shares one output column.
func groupingCallKey(gc *parser.GroupingCall) string {
	var b strings.Builder
	b.WriteString("grouping(")
	for i, a := range gc.Args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(qualifiedGroupKey(a))
	}
	b.WriteByte(')')
	return b.String()
}
