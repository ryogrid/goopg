# 0125-0001 — `internal/planner/exprwalk.go`: one child-slot primitive and a build-time exhaustiveness gate

Status: draft
Date: 2026-07-28
Milestone: M0125-0001 (§13.5 action 4, phase 1.1; design body §2.5 as corrected by review D1)

## Problem

§0 of the round-2 document: "goopg's planner contains **fourteen** independent hand-written
expression walkers … each silently passing through the `Expr` node kinds it does not
enumerate. Silence is the problem — a walker that skips `IsNullExpr` does not fail, it just
leaves a stale `ColumnRef.Index` behind, and the query returns the wrong answer."

`internal/planner/plan.go` declares **32** concrete `Expr` types. The marker method
`exprNode()` is unexported, so no other package can implement `Expr` — the set is closed,
which is what makes a build-time gate possible at all.

Round 2 addressed one walker, and not as designed (§13.1 phase 1.2): "hand-patched rather than
converted onto the §2.5 driver (which does not exist), and it **still has no `default:` arm**
— the precise property §0 names as the defect." §13.4 item 5: "the exhaustiveness gate meant
to make the class extinct does not exist."

> **Arithmetic correction to §13.4 item 5.** It says "eleven remain partial". Four walkers
> have since been hand-touched (`remapByPosMap`, `shiftColumnRefs`, `cloneExprForShift`,
> `formatExprPGReg`), so **seven** is the live figure — the seven M0125-0002 converts. Using
> eleven as a Definition of Done would make the milestone unachievable.

This task builds the primitive and the gate and **converts no call site** — §9 labels phase
1.1 "nothing yet — dead code", and separating inert infrastructure from behaviour-changing
conversions is exactly what lets M0125-0002 attribute a TPC-H move to a specific walker.

## Design

### D1. The primitive

```go
type exprSlotKind uint8
const (
    slotExpr     exprSlotKind = iota // *Expr   — an ordinary sub-expression
    slotExprList                     // *[]Expr — a homogeneous list
    slotSubqRow                      // **MultiAssignSubqRow — see D2
    slotPlan                         // Node    — a nested planner scope
)

type exprSlot struct {
    kind exprSlotKind
    expr *Expr
    list *[]Expr
    row  **MultiAssignSubqRow
    plan Node
}

// exprChildSlots returns every child slot of e, in evaluation order.
// It is the ONLY type switch over Expr in internal/planner.
func exprChildSlots(e Expr) []exprSlot
```

Addressability is load-bearing: a slot is a **pointer to the field**, so one primitive serves
the read, rewrite and clone drivers without a second traversal.

### D2. Three typing traps the naive version gets wrong

Review finding D1 recorded the first two; they are why `exprSlot` is a tagged union rather
than a bare `*Expr`.

1. **`MultiAssignSubqElem.Row` is statically `*MultiAssignSubqRow`**, not the `Expr`
   interface — so `&elem.Row` is `**MultiAssignSubqRow` and cannot be assigned to a `*Expr`
   slot, even though `*MultiAssignSubqRow` *does* implement `Expr`. It is a single pointer,
   not a slice. It gets its own arm and a named exception in the gate.
2. **A `Plan Node` child is a different coordinate space.** `MultiAssignSubqRow.Plan`,
   `SubqueryExpr`, `ExistsExpr` and a `Semi`/`Anti` join's inner side carry `ColumnRef.Index`
   in the *inner* scope. `applyJoinTreePosMap`'s own comment states the rule: a Semi/Anti
   join's Right side "is the cloned EXISTS inner plan — an isolated subquery scope whose
   ColumnRefs use inner-scope indices and must NOT be remapped by the outer FROM-bindings
   posMap." A driver that descended into plan slots by default would corrupt every correlated
   subquery.
3. **A scope-opening node reports ZERO `Expr` slots.** Because its children are `Node`, not
   `Expr`, callers must classify by slot **kind**, never by `len(kids) == 0`. Treating "no
   expression children" as "a leaf" silently conflates a literal with a subquery.

### D3. Scope policy — the driver must not decide

Review D1 rejected a single `kindOpaqueScope` because the existing walkers rely on four
distinct behaviours at a nested-scope slot. Each is evidenced by a real call site:

| behaviour | meaning | evidenced by |
|---|---|---|
| **signal** | record that an opaque child exists; the caller decides | `extraInScans` — its vacuous `true` *is* the signal today, wrongly (see 0125-0002) |
| **veto** | decline the whole transformation | `buildBindingsPosMap`'s decline-on-unknown `default:`, landed in `9740fce9` |
| **ignore** | skip and continue | `applyJoinTreePosMap`'s treatment of `Semi`/`Anti` inner plans (`bushy.go:2570-2578`) — note this is the *plan-tree* walker, not `remapByPosMap`, which is a pure `Expr` walker with no join arms at all |
| **descend** | same coordinate space | `Aggregate`'s argument list |

```go
type scopePolicy struct {
    OnPlanSlot func(Node) scopeAction // signal | veto | ignore | descend
    OnUnknown  func(Expr) scopeAction // what to do with a slot-less type
}
```

`OnUnknown` exists so a caller can choose **veto** rather than the current universal
**ignore**. Per review finding D2, `WindowAgg` does **not** belong in a blanket descend set and
`Aggregate` is only accidentally correct there — both are chosen per caller.

### D4. Three drivers, distinct names

```go
func walkExprRefs(e Expr, p scopePolicy, fn func(Expr))             // read-only
func rewriteExprRefsInPlace(e *Expr, p scopePolicy, fn func(*Expr)) // mutate
func cloneExprRefs(e Expr, p scopePolicy, fn func(*Expr)) Expr      // clone then mutate
```

**The clone-vs-mutate asymmetry is a hazard, not a style point.** `remapByPosMap` clones on
rewrite while `remapOuterRefsInSubplan` mutates in place. A driver that flattens the
asymmetry double-remaps a twice-visited subplan; picking the clone driver where the caller
expected in-place mutation compiles, runs, and silently drops the rewrite — the same
silent-failure class this task exists to delete. Distinct names plus a doc comment at each
call site naming its driver are the mitigation, and D5's pins include a double-remap case.

### D5. The exhaustiveness gate — the durable artifact

A Go test in `internal/planner` that

1. parses **every `.go` file in package `planner`** with `go/parser` + `go/ast` — not
   `plan.go` alone. The unexported `exprNode()` marker closes the set to other *packages*, not
   to other *files* in this one, and a type declared elsewhere in the package would evade a
   single-file parse silently, which is the failure mode this gate exists to end;
2. collects every type declaring `exprNode()` (all 32 live in `plan.go` today — the test must
   not depend on that staying true);
3. collects `exprChildSlots`'s case-clause types;
4. asserts set **equality in both directions**, reporting "declared but not handled" and
   "handled but no longer declared" separately.

Standard library only; no new module dependency. Acceptance proof: add a throwaway 33rd `Expr`
type in a scratch commit, observe the failure, remove it. The message must name the missing
type — the point is that the next person adding an `Expr` type is told what to do without
reading this document.

### D6. Regression pins for the conversions to consume

§2.6 required pins that were never added: `internal/planner/bushy_remap_test.go` holds only
`TestBuildJoinFromDP_NonAscendingSubsetKeyRemap`, last touched by `65dd185a`, which predates
this round.

Add a table-driven pin over **all 18 case arms** `remapByPosMap` now carries — the eleven
pre-existing plus the seven `9740fce9` added (`IsNullExpr`, `IsBoolExpr`, `IsDistinctFromExpr`,
`CollateExpr`, `RowExpr`, `MultiAssignSubqRow`, `MultiAssignSubqElem`). `InExpr.List`/`.Args`
and `Exists`/`Subquery` `.Args` are field descents *inside* pre-existing arms, not separate
kinds; pin them as sub-cases. Each pin asserts the remapped index, not merely "does not
panic". Add the double-remap pin from D4.

These exist so M0125-0002's re-base of `remapByPosMap` is *provably* behaviour-preserving.
Without them the conversion is an unpinned rewrite of the pass that produced Q76's wrong
answer.

## Deliberate non-goals

- **No call site converted.**
- The `applyJoinTreePosMap` MHJ-arm defect stays open: its arm remaps `n.Filters` and returns
  without recursing into `n.Tables[i]`, which is why a mispushed conjunct's corruption is
  permanent. That is a *recursion* bug, not a completeness bug; fixing it here would confound
  the conversion measurements. It is the mechanism behind ledger row `tpcds-round2 RC-1b`.
- No `GOOPG_POSMAP_ASSERT=1` diagnostic (§4.4) — ledger row from M0124-0003.
- `walkColumnRefsImpl` (`pushdown.go:362`) and the `shiftColumnRefs` closure are **not** in the
  §2.4 seven, are not converted, and are not covered by the gate. Ledger row from M0124-0003.

## Why hand-patching is not an alternative

§13.1 phase 2.2: "blocked by 1.1 **by design** — §2.5 defines each walker as one driver call,
so hand-converting them would re-create the copy-paste family the phase exists to delete."
`9740fce9` is the empirical case: it hand-patched one walker from 11 to 18 kinds, fixed Q76,
and left both the class and the missing `default:` intact.

## Gate

Units + the new exhaustiveness test + the D6 pins. **No TPC-H run is required**: there are no
call sites, so plan shape provably cannot move. That exemption does not extend to M0125-0002.
