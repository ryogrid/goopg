# The commit-6 divergence probe, as built

Reproduce with `git worktree add /tmp/c6probe-wt 53e46eaf` (the commit BEFORE
commit 6), then add the file below and apply the two-call-site patch. Never
committed to the branch — it exists to be re-runnable, not to ship.

Build: `cd /tmp/c6probe-wt && go build -o <repo>/tmp/goopg-c6-probe ./cmd/goopg`.
Run it through `capture-tpch.sh` and `capture-plans.sh` in this directory (both
take an ABSOLUTE binary path — a relative one silently fails to start the
server, a trap that cost the commit-5 loop a run).

## 1. `internal/planner/zz_c6probe.go` (new)

```go
package planner

// M0125-0002 commit 6 divergence probe — MEASUREMENT ONLY, never committed.
//
// Same instrument as commits 3/4/5: both walker bodies run side by side on the
// live planner path, the LIVE answer stays the OLD one, and every
// disagreement is logged. The commit-6 pair needs this more than commit 5 did,
// for two independent reasons:
//
//   - conjunctIsLocalEligible fails OPEN, so completing it REMOVES conjuncts
//     from the leaf-local set. That IS visible in a plan diff (a Filter above a
//     leaf scan appears/disappears), so the plan A/B is a real instrument here.
//   - localizeExprToLeaf rewrites ColumnRef.Index, and goopg's EXPLAIN prints
//     column NAMES, not indices (M0125-0042). A newly-rebased container is
//     therefore INVISIBLE to a byte-identical plan diff while changing which
//     slot the executor reads. Only this probe (or a full answer sweep) can
//     see it.
//
// Counters are greppable from the server log:
//
//	C6CALL  — every conjunctIsLocalEligible call (positive control: a zero
//	          delta is only meaningful if the site was reached at all)
//	C6ELIG  — the two eligibility answers disagree
//	C6LOCC  — every localizeExprToLeaf call (positive control)
//	C6LOC   — the two localized trees disagree (compared by exprIdentityKey,
//	          which INCLUDES ColumnRef.Index — the field EXPLAIN hides)
//	C6ABORT — the new consumer aborted where the old one passed through; in
//	          the shipped code this is the panic, so it must never fire on a
//	          conjunct the new producer admits

import (
	"log"
	"sync/atomic"
)

var (
	c6Calls  atomic.Int64
	c6Elig   atomic.Int64
	c6LocC   atomic.Int64
	c6Loc    atomic.Int64
	c6Abort  atomic.Int64
)

// conjunctIsLocalEligibleNew is commit 6's proposed body, verbatim.
func conjunctIsLocalEligibleNew(e Expr) bool {
	eligible := true
	ok := walkExprRefs(e, scopeVeto, exprVisitor{
		Visit: func(n Expr) bool {
			switch n.(type) {
			case *OuterColumnRef:
				eligible = false
				return false
			case *SubqueryExpr, *ExistsExpr, *ArraySubqueryExpr,
				*MultiAssignSubqRow, *MultiAssignSubqElem:
				eligible = false
				return false
			}
			return true
		},
	})
	return eligible && ok
}

// localizeExprToLeafNew is commit 6's proposed body, verbatim except that the
// abort PANIC is reported as a counter instead of crashing the probe.
func localizeExprToLeafNew(e Expr, binding rangeBinding) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if cr, isCol := n.(*ColumnRef); isCol {
				cr.Index -= binding.offset
			}
			return n
		},
	})
	return out, ok
}

// c6ProbeEligible replaces the live conjunctIsLocalEligible call sites.
func c6ProbeEligible(e Expr) bool {
	old := conjunctIsLocalEligible(e)
	nw := conjunctIsLocalEligibleNew(e)
	c6Calls.Add(1)
	log.Printf("C6CALL n=%d root=%T old=%v new=%v", c6Calls.Load(), e, old, nw)
	if old != nw {
		c6Elig.Add(1)
		k, _ := exprIdentityKey(e, scopeIgnore)
		log.Printf("C6ELIG delta=%d old=%v new=%v root=%T key=%s",
			c6Elig.Load(), old, nw, e, k)
	}
	return old
}

// c6ProbeLocalize replaces the live localizeExprToLeaf call sites. It returns
// the OLD tree so the probe binary plans exactly like the before binary.
func c6ProbeLocalize(e Expr, binding rangeBinding) Expr {
	old := localizeExprToLeaf(e, binding)
	nw, ok := localizeExprToLeafNew(e, binding)
	c6LocC.Add(1)
	log.Printf("C6LOCC n=%d root=%T offset=%d", c6LocC.Load(), e, binding.offset)
	if !ok {
		c6Abort.Add(1)
		log.Printf("C6ABORT n=%d root=%T — new consumer aborted", c6Abort.Load(), e)
		return old
	}
	// exprIdentityKey includes ColumnRef.Index, which is the whole point:
	// EXPLAIN prints the name and would render both trees identically.
	ko, okO := exprIdentityKey(old, scopeIgnore)
	kn, okN := exprIdentityKey(nw, scopeIgnore)
	if okO != okN || ko != kn {
		c6Loc.Add(1)
		log.Printf("C6LOC delta=%d offset=%d\n  old=%s\n  new=%s",
			c6Loc.Load(), binding.offset, ko, kn)
	}
	return old
}
```

## 2. The call-site patch (3 lines, 2 files)

```diff
--- a/internal/planner/local_filters.go
+++ b/internal/planner/local_filters.go
@@ partitionConjunctsForJoinPlanning
-		if !conjunctIsLocalEligible(c) {
+		if !c6ProbeEligible(c) {
@@ attachRelationLocalFilters
-				localized = append(localized, localizeExprToLeaf(p, binding))
+				localized = append(localized, c6ProbeLocalize(p, binding))
--- a/internal/planner/cardinality.go
+++ b/internal/planner/cardinality.go
@@ estimateBaseRelInfo
-	localized := localizeExprToLeaf(local, binding)
+	localized := c6ProbeLocalize(local, binding)
```

Those three are the COMPLETE live population of both functions
(`grep -rn 'conjunctIsLocalEligible\|localizeExprToLeaf' internal/planner/*.go`),
which is what makes a zero delta a statement about the whole decision set
rather than a sample.

## 3. Result (2026-08-03)

277 `C6CALL` + 175 `C6LOCC` over 22 TPC-H + 96 TPC-DS SF0.5 queries;
**0 `C6ELIG`, 0 `C6LOC`, 0 `C6ABORT`**.
