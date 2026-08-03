# The commit-5 divergence probe, as built

Reproduce with `git worktree add /tmp/c5probe-wt <the commit BEFORE commit 5>`,
then add the file below and apply the one-line patch. Never committed to the
branch — it exists to be re-runnable, not to ship.

## 1. `internal/planner/zz_c5probe.go` (new)

```go
package planner

import (
	"log"

	"github.com/goopg/goopg/internal/parser"
)

// exprSideNew is commit 5's proposed body, verbatim.
func exprSideNew(e Expr, leftWidth int) joinSide {
	side := sideUnknown
	ok := walkExprRefs(e, scopeVeto, exprVisitor{
		Visit: func(x Expr) bool {
			switch ref := x.(type) {
			case *ColumnRef:
				if ref.Index < leftWidth {
					side = mergeSides(side, sideLeft)
				} else {
					side = mergeSides(side, sideRight)
				}
			case *OuterColumnRef, *CTIDExpr:
				side = sideMixed
			}
			return true
		},
	})
	if !ok {
		return sideMixed
	}
	return side
}

// splitEqualityForHashNew is splitEqualityForHash verbatim except that it
// consults exprSideNew.
func splitEqualityForHashNew(pred Expr, leftWidth int) (Expr, Expr, bool) {
	for _, conjunct := range splitAnd(pred) {
		bin, ok := conjunct.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			continue
		}
		lSide := exprSideNew(bin.Left, leftWidth)
		rSide := exprSideNew(bin.Right, leftWidth)
		switch {
		case lSide == sideLeft && rSide == sideRight:
			return bin.Left, bin.Right, true
		case lSide == sideRight && rSide == sideLeft:
			return bin.Right, bin.Left, true
		}
	}
	return nil, nil, false
}

func c5ProbeCompare(pred Expr, leftWidth int, oldL, oldR Expr, oldOK bool) {
	// Positive control: a zero C5DELTA count is only evidence if the probe
	// actually ran.
	log.Printf("C5CALL")
	newL, newR, newOK := splitEqualityForHashNew(pred, leftWidth)
	if oldOK != newOK || oldL != newL || oldR != newR {
		log.Printf("C5DELTA oldOK=%v newOK=%v oldL=%T oldR=%T newL=%T newR=%T pred=%T",
			oldOK, newOK, oldL, oldR, newL, newR, pred)
	}
	for _, conjunct := range splitAnd(pred) {
		bin, ok := conjunct.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			continue
		}
		for _, operand := range []Expr{bin.Left, bin.Right} {
			o := exprSide(operand, leftWidth)
			n := exprSideNew(operand, leftWidth)
			if o != n {
				log.Printf("C5SIDE old=%v new=%v operand=%T", o, n, operand)
			}
		}
	}
}
```

## 2. `internal/planner/planner.go` (patch)

Rename the existing `splitEqualityForHash` to `splitEqualityForHashOld` and
interpose:

```go
func splitEqualityForHash(pred Expr, leftWidth int) (Expr, Expr, bool) {
	l, r, ok := splitEqualityForHashOld(pred, leftWidth)
	c5ProbeCompare(pred, leftWidth, l, r, ok)
	return l, r, ok
}
```

The live path returns the OLD answer, so the probe binary plans exactly like
its base commit — confirmed by `plan_snapshots/m0125-0002-c5-probe.txt` being
byte-identical to `-before.txt`.

## 3. Run

```bash
analysis/m0125-0002-c5-plans-20260803/capture-plans.sh probe <abs-path-to-probe-bin>  # TPC-DS SF0.5, 96 q
analysis/m0125-0002-c5-plans-20260803/capture-tpch.sh  m0125-0002-c5-probe <abs-path> # TPC-H, 22 q
grep -c 'C5CALL\|C5DELTA\|C5SIDE' <server log>
```

Pass the binary as an **absolute** path: both scripts `cd` to the repo root, so
a relative path resolves against the wrong directory and the server silently
fails to start (bitten once — `Failed to find executable …/analysis/…/tmp/…`).

Result on 2026-08-03: **232 `C5CALL`, 0 `C5DELTA`, 0 `C5SIDE`**
(223 TPC-DS + 9 TPC-H).
