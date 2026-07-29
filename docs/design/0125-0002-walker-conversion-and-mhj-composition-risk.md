# 0125-0002 — Converting the seven remaining walkers: a plan-**shape** change and the TPC-H trade-off it re-opens

Status: draft
Date: 2026-07-28
Milestone: M0125-0002 (§13.5 action 4, phase 2.2)
Depends on: `0125-0001-…` (the driver), M0124-0002 (the plan baseline), M0124-0005 (value checksums)

## Problem

Seven §2.4 walkers are unchanged from the diagnosis. All line numbers below are verified at
HEAD and **supersede README §2.4's stale cites**:

| walker | file:line | what it drives |
|---|---|---|
| `visitColumnRefsForTable` | `bushy.go:415` | per-table index collection |
| `visitColumnRefsByName` | `bushy.go:1653` | name collection — feeds `extraInScans` |
| `visitColumnRefs` | `bushy.go:2932` | generic index visit |
| `conjunctIsLocalEligible` | `local_filters.go:89` | may a conjunct be pushed to a leaf scan |
| `localizeExprToLeaf` | `local_filters.go:268` | rebase it onto that leaf |
| `cloneExprShiftIdx` | `nl_index_join.go:777` | NLI residual index shift |
| `exprSide` | `planner.go:5059` | which join side a conjunct belongs to |

Plus `remapByPosMap` (`bushy.go:2154`), 18 arms and still no `default:`.

## STEP 0 (executed 2026-07-30) — the inventory, re-derived from source

M0125-0001's execution record ended with an instruction: **re-derive this list before
converting anything**, because three different figures were in circulation and not one of them
came from the source. README §0 said fourteen, §13.4 said seven, and M0124-0003's review of
`walkColumnRefsImpl` made it nine. The table above is the *seven*.

The census is now mechanical and permanent, in `internal/planner/exprwalk_inventory_test.go`.
A **site** is a function in a non-test file of package `planner` whose body contains a type
switch with at least one `case *T:` where `T` has an `exprNode()` method — the same closed
type set `exprwalk_exhaustive_test.go` derives, so the census cannot drift from the definition
of `Expr`. Classification is by structural recursion: a site is a *walker* if a case body calls
the enclosing function again, calls a closure declared in that function (`conjunctIsLocalEligible`
descends through its `walk` closure), or calls another site's function (`cloneExprLeaf` ↔
`cloneExprReplacingOuter`).

| bucket | count | what it means |
|---|---|---|
| `exprwalkPrimitive` | 2 | `exprChildSlots` / `shallowCloneExpr`, complete over all 32 types |
| `walkerPending` | 50 | recursive traversal enumerating 2–25 of 32 types — the RC-1a class |
| `nonRecursiveClassifier` | 12 | decide-and-return; an unenumerated type falls through to a deliberate "not applicable" |
| **total** | **64** | |

**The live figure is 50, not seven, nine, or fourteen.** None of the three was close, and the
error was not arithmetic: the seven were selected by *blast radius* (MHJ composition and
local-filter partitioning), which is a sound way to scope a conversion and a useless way to
size a defect class. This task's scope does **not** change — the same eight commits, in the
same order — but its closing statement must be scoped to those eight call sites, which §"Explicit
non-goal" and §"Deliverable" already require, and now has a number to be honest about: **eight
of 50 (16 %)**.

The authoritative list is the `exprSwitchInventory` map in that test file, not this table — it is
gate-enforced in both directions, so a new hand-written walker fails the build and a converted
walker fails until its pin is deleted. That deletion is how this milestone's progress becomes
auditable instead of asserted. Arm counts are recorded as comments there and deliberately *not*
asserted: adding an arm to a partial walker is the band-aid 0125-0001 exists to replace, and
pinning the counts would make every band-aid look like progress. The gate was proved to fail in
both directions before being trusted (an unpinned probe walker; a renamed pin), matching
0125-0001 D5's precedent.

### Two fail-opens the census found that are worse than a silent no-op

The seven walkers fail open by *not descending*. Two un-named sites fail open by **colliding**,
which is a wrong answer rather than a missed optimisation, and neither is in any milestone:

1. **`planExprContentKey` (`planner.go:7027`, 4 of 32 arms)** keys aggregate **state-sharing
   equality**, and its `default:` returns `fmt.Sprintf("%T", e)` — *the type name alone*. Every
   pair of distinct expressions of the same unenumerated type therefore produces an identical
   key: two different `*CaseExpr`s, two different `*CastExpr`s, two different `*SubqueryExpr`s
   all key as their type and are treated as the same aggregate argument. This is the exact shape
   of M0097-0032, where a dropped FILTER clause collapsed `count(*) FILTER (WHERE …)` onto
   `count(*)` and the filtered count silently reported the unfiltered total.
2. **`exprEqual` (`planner.go:11950`, 5 of 32 arms)** backs DISTINCT ON / ORDER BY matching and
   falls back to `fmt.Sprintf("%T%v", a, a) == fmt.Sprintf("%T%v", b, b)`, which its own comment
   admits is "pointer-safe only for primitives" — for an unenumerated type holding pointers it
   compares *addresses*, so structurally equal expressions read unequal. Independently, its
   `*ColumnRef` arm compares **only `Index`** while `planExprContentKey`'s compares
   `SourceTableIdx/Index`: two `ColumnRef`s from different source tables at the same index are
   equal to one and distinct to the other. That is the sibling-divergence class (encode↔decode,
   fast-path↔interpreted) in a pair nobody had noticed was a pair.

Both are recorded in the deferral ledger and filed as `M0125-0024`; **neither is fixed here**,
because each changes aggregate sharing or DISTINCT semantics and needs its own value-checked
gate, which is precisely what this task's per-commit discipline exists to protect.

## Why this is not "just fixing stale indices"

§9's risk column was corrected during review (finding D4) because the first draft understated
it. `extraInScans` (`bushy.go:1625`) reads, in essence:

```go
allMatched := true
visitColumnRefsForTable(c, func(idx int) {})
visitColumnRefsByName(c, func(name string) {
    found := false
    ...
    if !found { allMatched = false }
})
return allMatched
```

`allMatched` starts `true` and is only falsified **from inside the callback**. A conjunct built
entirely from kinds `visitColumnRefsByName` does not enumerate produces **zero callbacks** and
returns a vacuous `true`. Its caller uses that as the admission test for capturing an extra
conjunct into `MultiHashJoin.Filters`.

**Completing the walker makes conjuncts currently admitted by accident newly visible — and any
whose column name is absent from the MHJ's scan subset will now be rejected.** The conversion
therefore *removes* predicates from `MultiHashJoin.Filters`, changing what the MHJ evaluates,
its output cardinality, and the shape of everything above it. The same holds for
`conjunctIsLocalEligible` / `localizeExprToLeaf`, which decide which predicates reach leaf
scans before the MHJ is built.

## A coupling that was investigated and found NOT to exist

§2.4 files `localizeExprToLeaf` with `conjunctIsLocalEligible` as "shadowed by the
`shouldAttachBeforeMHJ` gate". An earlier draft of this document disputed that, on the
grounds that `estimateBaseRelInfo` calls it at `internal/planner/cardinality.go:145` and is
dormant only because `baseRows` is 0 on an S-cold server — which would have made M0125-0003
the thing that wakes it.

**That is wrong, and the finding is recorded here so it is not re-derived.** The chain:

```go
// bushy.go:157-169 — locals is populated ONLY under the gate
var locals relationLocalFilters
if shouldAttachBeforeMHJ(ctx.bindings) {
    dpConjuncts, locals = partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)
}
...
relInfos[i] = estimateBaseRelInfo(b, leafScan, local)   // local = locals.byBinding[i]

// cardinality.go:142 — returns BEFORE baseRows is consulted
if local == nil || scan == nil || info.baseRows <= 0 { return info }
localized := localizeExprToLeaf(local, binding)
```

With the gate closed, `local` is nil and the walker is unreachable at that site **whatever
the relation sizes are**. With the gate open, `attachRelationLocalFilters` (`bushy.go:219`,
via `local_filters.go:236`) already calls the same walker on the same predicates today — so
M0125-0003 would route this walker's output into cardinality estimation, not make a latent
bug newly live.

Consequence: **M0125-0003 does not depend on this task.** The two are independent.

## The TPC-H trade-off this re-opens

`shouldAttachBeforeMHJ` (`local_filters.go:154`) reads, in order:

```go
if costDrivenJoinOrder { return len(bindings) >= 2 }   // flag-gated, off by default
if len(bindings) < 5 { return false }
for _, b := range bindings {
    if b.table != nil && b.table.SmallDimension { return true }
}
```

Its own comment records why the last clause exists: Q7/Q8/Q21 are 5+-table queries whose MHJ
shape is beneficial and where filtered leaves push the planner away from MHJ packing into a
slower binary chain — "**Without the SmallDim guard, Slice A regresses Q8 / Q21 from PASS to
CANCEL.**"

`SmallDimension` is hardcoded to `region` / `nation` (`internal/initdb/open.go:2911`). §7.3
uses that to argue no TPC-DS relation qualifies — true, and the reverse is equally true: the
TPC-H queries with ≥5 FROM items that reference `region` and/or `nation` pass the gate and
reach both incomplete walkers today.

**The blast radius is {Q2, Q5, Q7, Q8, Q9}** — counted from the query files, not inherited:

| query | FROM items | passes the gate? |
|---|---|---|
| Q2 | 5 (`part, supplier, partsupp, nation, region`) | **yes** |
| Q5 | 6 (`customer, orders, lineitem, supplier, nation, region`) | **yes** |
| Q7 | 6 (`supplier, lineitem, orders, customer, nation n1, nation n2`) | **yes** |
| Q8 | 8 (`part, supplier, lineitem, orders, customer, nation n1, nation n2, region`) | **yes** |
| Q9 | 6 (`part, supplier, lineitem, partsupp, orders, nation`) | **yes** |
| **Q21** | **4** (`supplier, lineitem l1, orders, nation`) | **no** — `len(bindings) < 5` |

> **Q21 is not in the blast radius**, despite `shouldAttachBeforeMHJ`'s own comment calling
> "Q7 / Q8 / Q21" 5+-table queries. Its correlated `EXISTS`/`NOT EXISTS` subqueries are not
> FROM items. The comment is wrong; do not inherit it. Conversely **Q2 and Q9 are in**, and
> they are the two most stats-volatile cells on record — Q2 is round-4's 26× regression *and*
> round-5's 18.8× win, Q9 is round-5's non-completing cell. They must be on the watch list.

The direction of the risk is measured, not hypothetical
(`analysis/tpch-evolution-round5-int64-hashjoin-20260724.md` §6): dropping MultiHashJoin turns
star/snowflake queries into binary cascades that materialise wide intermediates a single MHJ
probe pass would stream — **Q5 and Q21 hang, Q9 times out, Q10 11.4×, Q18 4.3×, Q7 1.9×**. The
same section shows the axis has a favourable direction too (Q2 18.8×, Q8 4.1×), so "any MHJ
change is bad" is the wrong lesson. The right one: **the direction is not predictable from the
code change**, so it must be measured per commit.

And the instrument matters:

> In round-5 §6, **of the queries that completed**, every one returned identical rows to the
> default planner while running up to 1.9× slower; three (Q5, Q9, Q21) did not complete at all
> and returned nothing. `scripts/tpch-spotcheck.sh` compares Q12/Q13 **row counts** — it would
> have passed every completing regression, and Q12/Q13 are not among the non-completing three,
> so it would have reported green for the whole set.

## Design

### D1. One walker per commit

Non-negotiable — the point is that a TPC-H regression is a single `git revert`.

### D2. Conversion order — lowest blast radius first, shape last

**Every one of these is a potential shape mover.** An earlier draft split them into "index
arithmetic, expect an empty plan diff" and "shape". Re-reading the call sites killed that
split — three of the four supposedly inert walkers are admission tests or index rewriters:

| # | walker | what completing it actually does |
|---|---|---|
| 1 | `remapByPosMap` re-base + `default:` | the only genuinely no-op step, because 0125-0001 D6's 18-arm table pins the current behaviour exactly. Adds a veto path. **Expect an empty plan diff**; a hunk is a stop-and-review |
| 2 | `cloneExprShiftIdx` | **not** inert. It returns `(Expr, bool)` and ends `return nil, false`; its caller (`nl_index_join.go:363-370`) sets `okAll = false` and abandons the inner-Filter unwrap. Adding kinds therefore *opens* the NLI inner-unwrap on shapes that decline today. It already fails **closed**, so it is not an instance of §0's silent-passthrough defect — but it does move plans |
| 3 | `visitColumnRefs` | rewrites `ColumnRef.Index` on **join predicates** via `reresolveJoinByName`'s `predRebind` (`bushy.go:2925`), plus `reresolveExprByName` and `nl_index_join.go:703`. Changes which refs get re-resolved by name |
| 4 | `visitColumnRefsForTable` | feeds `tableForCol` (`bushy.go:391`), used by `partitionConjunctsForJoinPlanning` (`local_filters.go:68` — which conjuncts become leaf-locals) and by join-edge left/right classification (`bushy.go:285`, `:825`). README §2.4 says so itself: "`tableForCol` mis-partitions". A first-order shape mover |
| 5 | `exprSide` | decides which side a conjunct is pushed to |
| 6 | `conjunctIsLocalEligible` **+** `localizeExprToLeaf` | producer/consumer pair; splitting them leaves a state where a conjunct is judged eligible and cannot be rebased. **One commit.** Blast radius {Q2, Q5, Q7, Q8, Q9} |
| 7 | `visitColumnRefsByName` | **last** — `extraInScans` gives it the largest and least predictable effect on MHJ composition |

Order is still least-to-most blast radius, but **only commit 1 carries an empty-diff
expectation**. Commits 2–7 each expect hunks and carry the full timed 22-query run. The §2.6
regression pins from 0125-0001 D6 land with commit 1, since they pin `remapByPosMap`.

### D3. Scope policy per walker — stated, not defaulted

Each commit message records the `scopePolicy` it selects and why. Two are predetermined:
`remapByPosMap` — plan slots **ignore** (the Semi/Anti rule), unknown **veto** (the missing
`default:`). `extraInScans`'s walker — plan slots **signal**, and the caller must treat "an
opaque child exists" as *not matched*, inverting today's vacuous `true`.

### D4. Per-commit gate

1. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
2. `make plan-diff LABEL=tpcds-round2-head MODE=structural` — **not** `make plan-gate`, which
   selects `ls -t plan_snapshots/*.txt | head -1` (newest by mtime) and would silently
   retarget if any other snapshot is captured meanwhile;
3. a **timed** 22-query TPC-H power run from a fresh capped server at `GOGC=100` /
   `GOMEMLIMIT=12GiB`, appended to one running `analysis/tpch-walker-conversions-<date>.md`;
4. the TPC-DS SF0.5 gate at a **fixed** budget, **with M0124-0005's checksums** — on the first
   and last commit and on any commit whose plan-diff shows a hunk. Row counts alone cannot
   accept a change that moves which rows reach a filter; that is this programme's own lesson.

Acceptance per commit: any TPC-H move **> 10 %** investigated and explained, **> 25 %** blocks
(round-5 §3 puts the no-join noise floor at −1.6 % … +7.1 % and calls 2–8 % moves
unattributable — it labels Q11's +8.4 % noise, so an 8 % threshold would flag its own example); nothing that
completed stops completing; every plan-diff hunk enumerated in the commit message; SF0.5 PASS
and ck-verified counts non-decreasing.

**Budget: ~12–20 h across the eight commits.** State it, because it is the largest single cost
in M0125 and it is why M0125-0003's stage 1 is scheduled before this task.

### D5. Cancellation hazard during measurement

Round-5 §6: a memory-thrashing plan **does not honour cancellation** — the runner's 300 s
timeout was ignored on Q5/Q21 and the server stayed pinned at ~10 GB RSS. A conversion that
pushes Q5 or Q21 into a binary cascade will not report a timeout; it will hang. Run every arm
under the cgroup cap (`scripts/goopg-test-run.sh`) with a per-query external hard cap above the
runner's own timeout, as round-5 §6's harness did.

### D6. Rollback

A conversion that regresses TPC-H is reverted, not fixed forward. Its correctness win is latent
(no TPC-DS query in the current defect set is attributed to walkers 1–7), so no urgency
justifies carrying a measured runtime regression.

## Explicit non-goal

Opening the `SmallDimension` gate (§7.3 RC-5) — ledger row from M0124-0003, reopen after this
task **and** M0125-0005. Changing the walkers and the gate that masks them simultaneously is
the one experiment guaranteed to be uninterpretable.

## Deliverable

STEP 0 (landed 2026-07-30): the census gate `internal/planner/exprwalk_inventory_test.go`, which
pins all 64 Expr type switches and makes the inventory unable to drift again.

Then eight commits (seven walkers plus the `remapByPosMap` re-base), each with its gate evidence;
`analysis/tpch-walker-conversions-<date>.md` with one table per commit; and a closing statement
scoped to **the eight named call sites** — since `walkColumnRefsImpl` and the `shiftColumnRefs`
closure are out of scope and tracked by a ledger row, "the walker class is extinct" is not
claimed. STEP 0 puts a number on that caveat: the eight are **16 % of the 50** recursive,
incomplete walkers, and each conversion is complete only when its pin is deleted from
`exprSwitchInventory`.
