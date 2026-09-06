# C-20b (P6-02) — `PathTarget` + range table: what is replaceable, what is not

*Design for TODO_ALL `C-20b P6-02`. Parent design: planner-refactor-take3
08 §9.2. Gate: value-level `tpch-runner -diff`, never row counts.*

Status: DESIGN. Written before implementation, per the item's own
"measure before deleting" discipline and the two precedents it inherits
(C-20a deleted nothing; C-20c is BLOCKED on a failed plan-neutrality gate).

---

## 1. What the item asks, and the one thing it assumes

TODO_ALL C-20b:

> **C-20b P6-02 `PathTarget` + range table.** Replace `baseLeaf`/`baseOffset`;
> delete `joinlayout.go` remapping + the `createplanroot.go` boundary
> assertions. Deletes the largest silent wrong-answer class — value-level
> `tpch-runner -diff`, never counts.

The item is written from the PG side, where the claim is true by
construction. In PG a `Var` is `(varno, varattno)` — a *range-table
index* plus an attribute number (`postgres/src/include/nodes/primnodes.h`
`Var`), never a position in a concatenated row. A join reordering
therefore cannot invalidate an expression, because no expression names a
position. Positions appear exactly once, at the very end, when
`set_plan_references` / `fix_upper_expr`
(`postgres/src/backend/optimizer/plan/setrefs.c`) rewrite each `Var` into
`OUTER_VAR` / `INNER_VAR` plus a slot offset against the child tuple the
executor will actually see. `PathTarget`
(`postgres/src/include/nodes/pathnodes.h`) is the other half: it carries
the *expressions* a path emits, so "what this node produces" is a list of
expressions rather than a width and an ordering convention.

The assumption the item makes is that goopg's remapping exists because
goopg lacks a range table. **It does not.** Section 2 shows goopg already
has one. The remapping exists because goopg's `Var` equivalent is
positional.

## 2. goopg already has a range table; what it lacks is `Var`

goopg's range table is `rangeBinding` (`internal/optimizer/planner.go`).
One entry per FROM item, in FROM order, carrying:

* `table` / `alias` — the relation identity (PG's `RangeTblEntry.relid`
  plus `eref->aliasname`, `postgres/src/include/nodes/parsenodes.h`);
* `sourceIdx int16` — a per-statement monotonic identity from 1,
  propagated into every column the binding produces as
  `SchemaColumn.SourceTableIdx` (`plan.go:40`). This is PG's `varno` in
  everything but name and consumption;
* `rtid int32` — the statement-wide range-table identity (A-01(ii));
* `offset int` — the first output-column index for this relation in the
  pre-search *binding concatenation*.

`RelOptInfo.baseLeaf` / `baseOffset` (`path.go:526`/`:556`) are that same
entry, projected onto the search's relid space: `baseLeaf` is "what relid
`1<<i` MEANS", `baseOffset` is `bindings[i].offset`. `buildInitialRels`
(`joinsearch.go:423`) copies both out of the binding list. They are a
*view* of the range table, not a substitute for it.

What goopg does not have is PG's `Var`. `ColumnRef` (`plan.go:429`)
carries:

```go
type ColumnRef struct {
    pos            int
    Index          int            // 0-based into the input row  <-- the coordinate
    Name           string         // "for diagnostics"
    Type           catalog.Type
    SourceTableIdx int16          // varno, carried but not addressed through
}
```

`Index` is the address. `SourceTableIdx` is a *disambiguation hint* that
several rebind helpers consult when `Name` alone collides
(`findColumnIndexByNameAndSource`, `predRebind`), and nothing resolves a
reference *through* it. The executor matches: expression evaluation is a
flat slice lookup into the child's materialised slot.

**Therefore:** replacing `baseLeaf`/`baseOffset` with a range table
changes nothing about the wrong-answer class, because the class is a
property of `ColumnRef.Index`, not of how a base rel finds its leaf. The
landable version of "give goopg PG's addressing" is:

1. `ColumnRef` becomes `(SourceTableIdx, attno)` with `Index` derived;
2. every producer (parser binder, ~40 rewrite sites) stops computing
   positions;
3. a `setrefs` pass computes positions once, at the end;
4. the executor's slot resolution is re-pointed at the setrefs output.

Step 1 is in `plan.go`, which this task does not own (C-14/C-10b agent),
and steps 2–4 span the parser, the whole optimizer and the executor's
expression evaluator. That is not this item; it is C-20h/P6-07's `setrefs`
half plus a `Var` migration that no TODO_ALL row currently carries.
Section 6 records the finding.

## 3. Why the boundary assertions must NOT be deleted

`boundaryMap` (`createplanroot.go:180`) checks that the search root's
layout is a permutation of `[base, base+width)` — no HOLE, no
OUT-OF-RANGE coordinate, no DUPLICATE — and panics otherwise. The item
asks for their deletion on the theory that a range table makes them
unnecessary.

They are the *detector* for the class the item names, not a symptom of
its absence. The repo already says so in two places:

* `searchedtree.go`, on `assertSearchedTreeNeedsNoReconcile`: "The
  structural checks in `boundaryMap` (hole, out-of-range, duplicate) are
  **not replaceable by this and remain the primary guard**."
* take3 08 §9.3 marks P6-05 must-not-delete for the sibling reason:
  deleting `reconcileNLILayout` "removes the check, not the code".

While `ColumnRef` stays positional, deleting `boundaryMap`'s panics
converts three loud plan-time aborts into three silent wrong-row classes.
That is the opposite of the item's stated goal. **They stay.**

What *is* available is the inverse move, and it is the real deliverable
of the "PathTarget + range table" half — see §5.

## 4. Measured: the `joinlayout.go` remapping IS deletable

Census instrumentation (temporary, not committed) counted, per pass,
(a) how often it is REACHED, (b) how often it obtains a non-nil posMap
and APPLIES it, and (c) how many `ColumnRef.Index` / `OuterColumnRef.Index`
values it actually MOVED. Plans captured as `EXPLAIN` text over the full
TPC-H (22) and TPC-DS (99) corpora, on both `GOOPG_PGSHAPED_DP` arms.

| corpus | DP | remapWithBindings entry/applied | remapTopProjection entry/applied | remapAgg entry/applied | **moves** |
|---|---|---|---|---|---|
| TPC-H | 1 | 46 / 38 | 14 / 13 | 32 / 25 | **0** |
| TPC-H | 0 | 46 / 19 | 11 / 7 | 32 / 12 | **0** |
| TPC-DS | 1 | 408 / 101 | 134 / 47 | 214 / 54 | **0** |
| TPC-DS | 0 | 408 / 292 | 194 / 106 | 214 / 186 | **0** |

The passes run constantly (up to 408 entries, 292 applications on one
arm) and move nothing. Plan-neutrality of the removal, which is exactly
the gate C-20c failed:

| corpus | DP | plans with passes / without |
|---|---|---|
| TPC-H | 1 | **byte-identical** |
| TPC-H | 0 | **byte-identical** |
| TPC-DS | 1 | **byte-identical** (3968 lines) |
| TPC-DS | 0 | **byte-identical** (3992 lines) |

`go test ./internal/optimizer/ ./internal/executor/` passes with the three
passes forced off. The single move observed anywhere in the repo was one
index in `remapTopProjection` under `TestSAOPMultiTableRewriteMoves`, a
test that explicitly selects the legacy enumerator
(`useLegacyEnumerator`); that test passes unchanged with the pass removed,
so the move was inconsequential.

Separately, `remapColumnRefsAfterRewrite` / `remapPosMapAfterRewrite`
(`joinlayout.go:156`) are deletable **by construction, without
measurement**: `remapPosMapAfterRewrite` takes a `posMap func(int) int`
parameter, never reads it, and its body contains no assignment to any
`Index` field. It is a ~110-line tree walk that mutates nothing. Every
recursive call passes `nil`.

## 5. What lands instead of the assertion deletion: a range-table identity check

`boundaryMap` checks the layout is a *permutation*. It does not check
that the column sitting at root output position `out` is the column the
layout *claims* sits there. A layout that is a valid permutation but
assigns the wrong binding coordinate to a column passes every existing
check and produces the exact failure mode this item names: the right
number of rows, with values from a neighbouring relation.

The range table is what lets that be stated, and this is the sense in
which C-20b's "range table" half is landable:

```
rangeTableFromPath(p)   // binding coordinate -> the SchemaColumn the
                        // range table says lives there, collected from
                        // every base rel reachable from the root path
                        // (rel.baseLeaf.Output()[i] at rel.baseOffset+i)
```

`assertBoundaryColumnIdentity(n, lay, rt)` then requires, for every root
output column `out`, that `n.Output()[out]` agrees with `rt[lay[out]]` on
`(Name, SourceTableIdx)`.

This is an INDEPENDENT check for the same reason
`assertSearchedTreeNeedsNoReconcile` is: `lay` is derived by
`outputLayout` (createplanjoin.go) from `rel.baseOffset` plus a name
lookup in the leaf, while `n.Output()` is the concatenation of the actual
children's schemas that the arms built. Two derivations agreeing is
evidence; a disagreement is a coordinate bug caught at plan time instead
of in a value.

Abstention discipline, transcribed from `assertSearchedTreeNeedsNoReconcile`
(a check that agrees where it can see and says nothing where it cannot):

* a root output column whose `Name` is empty, or whose range-table
  counterpart's `Name` is empty — abstain (an unnamed column is one the
  check cannot see);
* `SourceTableIdx == 0` on either side — compare on `Name` only
  (0 means "unknown / derived" per `plan.go:40`);
* a binding coordinate the range table has no entry for — abstain. A
  padded/`fill`ed slot (M0134-0187) is licensed by construction and a real
  hole is already `boundaryMap`'s panic, so this arm adds nothing there;
* narrowed leaves: an index-only path emits a subset in index-column
  order, so the check compares by identity, never by position within the
  leaf.

It runs where `assertSearchedTreeNeedsNoReconcile` runs — on the join
tree the arms built, below the boundary `Project` — for the same reason:
the Project's target list *is* the coordinate map, not a reference into
it.

Signature constraint: the only production caller of
`createPlanAtSearchRootRange` is `relfromjoinlist.go:739`, which this task
does not own, so the range table is reconstructed from the root `*Path`'s
own `RelOptInfo`s rather than threaded in. That is not a workaround — it
is the correct source, because the coordinate the check must validate is
the one the search actually used.

## 6. Findings recorded rather than implemented

* **`baseLeaf`/`baseOffset` are not replaceable within this task's file
  ownership.** They are read in `createplanjoin.go`, `createplanindex.go`,
  `createplansimple.go`, `createplanbitmap.go`, `considerparallel.go`,
  `pathindexonly.go`, `pathparamindex.go`, `pathbitmap.go` and
  `relfromjoinlist.go` — all off-limits. A value-embedded `rangeTblEntry`
  in `RelOptInfo` would preserve every one of those field accesses through
  Go field promotion and is the recommended shape when the ownership
  allows it; it is ceremony without the `Var` change (§2), so it is
  recorded, not landed.
* **C-20h's `setrefs` half is NOT moot — it is the actual P6-02.** The
  item makes it conditional on "if C-20b shows the executor still needs
  explicit column resolution". C-20b shows exactly that: `ColumnRef.Index`
  is the executor's only column address. See §7.

## 7. Slices

| slice | change | gate |
|---|---|---|
| S0 | this design doc | committed with `-n`, pushed |
| S1 | delete `remapColumnRefsAfterRewrite` + `remapPosMapAfterRewrite` and their `planner.go` call | build/vet/unit; proven no-op by construction |
| S2 | delete `remapWithBindings`, `remapTopProjection`, `remapAggExprsWithBindings`, `buildBindingsPosMap`, `applyJoinTreePosMap`, `scanKey`, `searchedTreeWidth` and their `planner.go` calls | §4 census; byte-identical plans both corpora both arms; unit suites |
| S3 | `rangetable.go` + `assertBoundaryColumnIdentity` in `createplanroot.go` | new assertion must not fire on either corpus; plans unchanged |
| S4 | TODO_ALL C-20b row + ledger | — |

Acceptance for the whole item: `tpch-runner -digest` / `-diff` **24 MATCH
on values**, TPC-DS SF0.5 sweep `PASS=95 MISMATCH=0 CKMISMATCH=0
TIMEOUT=0`, `make plan-gate` + `MODE=costs`, `make ea-ratchet`, full
`go test ./internal/optimizer/ ./internal/executor/`.
