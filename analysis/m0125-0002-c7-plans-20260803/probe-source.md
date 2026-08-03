# M0125-0002 commit 7 — divergence probe source (verbatim, NOT committed)

Built as `tmp/goopg-c7-probe`. Three one-line call-site hooks (reverted after
the run) plus this file. Result: `probe.server.log` — 2 `C7CALL` lines, ZERO
DELTA lines while planning TPC-DS Q85, which is what refuted the attribution.

```go
package planner

// TEMPORARY M0125-0002 commit 7 divergence probe — NOT COMMITTED.
// Reproduced verbatim in analysis/m0125-0002-c7-plans-20260803/probe-source.md.
//
// The SF0.5 EXPLAIN A/B moved exactly one plan (TPC-DS Q85, cd1/cd2 swap).
// D4 requires every plan-diff hunk to be enumerated in the commit message, so
// this probe answers WHICH of the three consumers changed its verdict, and
// whether the cause was the new totality flag or a name the old 7-arm walker
// never collected. It runs the OLD body alongside the new one and logs only
// disagreements.

import (
	"fmt"
	"os"
	"sort"
)

var c7probeOn = os.Getenv("GOOPG_C7PROBE") != ""

func c7log(format string, a ...any) {
	if c7probeOn {
		fmt.Fprintf(os.Stderr, "C7PROBE "+format+"\n", a...)
	}
}

// c7oldByName is the pre-conversion 7-arm body, verbatim.
func c7oldByName(e Expr, fn func(string)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		if x.Name != "" {
			fn(x.Name)
		}
	case *BinaryOp:
		c7oldByName(x.Left, fn)
		c7oldByName(x.Right, fn)
	case *UnaryOp:
		c7oldByName(x.Operand, fn)
	case *FuncCall:
		for _, a := range x.Args {
			c7oldByName(a, fn)
		}
	case *ExtractExpr:
		c7oldByName(x.Source, fn)
	case *CaseExpr:
		if x.Operand != nil {
			c7oldByName(x.Operand, fn)
		}
		for _, w := range x.Whens {
			c7oldByName(w.When, fn)
			c7oldByName(w.Then, fn)
		}
		if x.Else != nil {
			c7oldByName(x.Else, fn)
		}
	case *InExpr:
		c7oldByName(x.Operand, fn)
	}
}

func c7names(e Expr, old bool) []string {
	var got []string
	if old {
		c7oldByName(e, func(n string) { got = append(got, n) })
	} else {
		visitColumnRefsByName(e, func(n string) { got = append(got, n) })
	}
	sort.Strings(got)
	return got
}

// c7cmp reports a verdict disagreement at one call site.
func c7cmp(site string, c Expr, oldVerdict, newVerdict bool) {
	if !c7probeOn {
		return
	}
	if oldVerdict == newVerdict {
		return
	}
	on := c7names(c, true)
	nn := c7names(c, false)
	_, total := c7names(c, false), visitColumnRefsByName(c, func(string) {})
	c7log("%s DELTA old=%v new=%v total=%v oldnames=%v newnames=%v",
		site, oldVerdict, newVerdict, total, on, nn)
}

// c7extraOld replays the old extraInScans verdict.
func c7extraOld(c Expr, scans []Node) bool {
	allMatched := true
	c7oldByName(c, func(name string) {
		found := false
		for _, s := range scans {
			ss, ok := s.(*SeqScan)
			if !ok {
				continue
			}
			for _, col := range ss.Output() {
				if col.Name == name {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			allMatched = false
		}
	})
	return allMatched
}

// c7scopeOld replays the old allColumnRefNamesInScope verdict.
func c7scopeOld(c Expr, j *Join) bool {
	names := map[string]bool{}
	collectScanOutputNames(j.Left, names)
	collectScanOutputNames(j.Right, names)
	allIn := true
	c7oldByName(c, func(name string) {
		if !names[name] {
			allIn = false
		}
	})
	return allIn
}
```
