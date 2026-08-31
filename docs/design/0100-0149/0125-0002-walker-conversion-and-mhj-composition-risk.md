# 0125-0002 — Converting the seven remaining walkers: a plan-**shape** change and the TPC-H trade-off it re-opens

Status: superseded — MHJ retired (M0127); see [leftdeep-joins/](leftdeep-joins/)
Date: 2026-07-28 (STEP 0 2026-07-30; commit 1 2026-07-30)
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

## Execution record

### Commit 1 of 8 — `remapByPosMap` re-based onto `rewriteExprRefsInPlace` (2026-07-30)

D2 row 1. `internal/planner/bushy.go`: the 18-arm hand-written type switch is gone; child
structure now comes from `exprChildSlots` alone. **The empty plan diff D2 predicted holds — all
22 TPC-H queries MATCH** against `plan_snapshots/tpcds-round2-head.txt` in `structural` mode.

Three decisions the design left open, resolved by reading the pins rather than the prose:

1. **The driver is `rewriteExprRefsInPlace`, not `cloneExprRefs`** — D2 said "re-base" and the
   §2.6 pin comment guessed `cloneExprRefs`. That guess is refuted by another pin in the same
   file: `TestRemapByPosMap_IdentityMapSharesNodes` requires an identity remap to leave the node
   *shared*, and `cloneExprRefs` shallow-clones **every** node including the root, so it would
   replace it. The pre-conversion walker mutates containers in place and copies a `ColumnRef`
   only when its index actually moves; `rewriteExprRefsInPlace` reproduces exactly that.
   `TestRemapByPosMap_ContainerNodesAreNotCloned` now pins the container half, which nothing did
   before — it matters because `Aggs[i].Arg`, `Keys[i].Expr` and `GroupExprs[i]` are remapped
   through separate top-level calls in `bushy.go` while callers hold handles on the containers.
2. **The scope policy is `scopeIgnore`, and D3's "plan slots ignore" is only half the story.**
   Taken literally it would have *dropped* the `remapOuterRefsInSubplan` calls and reintroduced
   the TPC-H Q21 defect (AI-20260707-000712-005: read `l_comment` where `l_suppkey` was meant).
   The walker has **two** kinds of inner plan needing **opposite** treatment — `InExpr.Plan` was
   already remapped by the caller and must not be touched, while
   `Exists`/`Subquery`/`ArraySubquery`/`MultiAssignSubq*` must have their Level-1 outer refs
   translated — and a `scopePolicy` is per-driver, not per-type, so it cannot express the split.
   The `Rewrite` callback owns it instead: plans are invisible to the driver, and the six types
   that need work beyond child descent are dispatched bottom-up. `scopeDescend` + `OnScope` was
   the alternative and is wrong: `OnScope` receives the `Node` with no parent context, so it
   cannot tell an `InExpr`'s plan from an `ExistsExpr`'s.
3. **The missing `default:` is a panic**, matching PG: `expression_tree_walker_impl` and
   `expression_tree_mutator_impl` both close with
   `elog(ERROR, "unrecognized node type: %d")` (`postgres/src/backend/nodes/nodeFuncs.c:2667`,
   `:3743`). Fail-open here is a **wrong answer**, not a missed optimisation — a subtree the
   remap stepped over keeps pre-rewrite indices inside an otherwise-remapped predicate. It is
   unreachable while `exprwalk_exhaustive_test.go` is green, which is the point: the gate catches
   a 33rd type at build time, the panic is the runtime backstop. Deferral-ledger row 2026-07-30
   records what is *not* PG-faithful about it (a bare panic rather than `ereport(ERROR)` with a
   SQLSTATE, reaching the client as `XX000` only through `server.go`'s single `recover()`).

**The census pin DEMOTED rather than disappeared, and the Deliverable section's rule needs that
qualification.** The census keys a type switch by its *enclosing function*, and closures count,
so the six-arm dispatch inside `Rewrite` keeps `bushy.go:remapByPosMap` in the census. Its role
in `exprSwitchInventory` therefore moves `walkerPending` → `nonRecursiveClassifier`: the
recursion and the exhaustiveness both moved to `exprChildSlots`, which is the property the role
names. Pin *deletion* is the audit signal only for walkers whose switch vanishes entirely; a
converted rewriter that still has to dispatch on node type is audited by the role change. Any
attempt to force a deletion here would mean either hiding the switch behind an `if`-chain of
type assertions (gaming the gate) or renaming it into a helper (the same switch, a different
key).

**Gates.** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`make plan-diff LABEL=tpcds-round2-head MODE=structural` = 22/22 MATCH; pgbench smoke via the
commit hook; four new pins in `remap_arms_test.go` (subplan `Args` are same-scope — untested
before; containers not cloned; unenumerated type panics at the root and nested). **D4 item 4's
SF0.5 arm is OWED, not run** — the `ci/batch` nightly held the host (load ~10, its TPC-DS stage
mid-flight at 11 GB RSS on port 65435), and a concurrent 99-query sweep would have risked the
memory guard killing that stage. It must run before commit 2, which is the first commit expected
to move a plan.

**Host hazard found while capturing the plan diff:** `bench/tpch/setup_goopg.sh` rebuilds
`tmp/goopg-bench-bin`, the *same* path the `ci/batch` nightly lane runs its clone servers from.
The running nightly server survived (Go's linker unlinks and recreates, so the live inode stays),
but any nightly stage that starts a server *after* such a rebuild silently picks up the new
binary mid-run. Use a private `-o` path when a nightly may be in flight.

### Commit 2 of 8 — `cloneExprShiftIdx` re-based onto `cloneExprRefs` (2026-07-30)

Measured in `analysis/m0125-0002-c2-sf05-plans-20260730/` (quiet host).

**D2 row 2's prediction is REFUTED, and it was refuted by measurement rather than argued away.**
The row said commit 2 is "not inert … it does move plans", and the reasoning was right in every
step: `cloneExprShiftIdx` fails *closed*, its caller abandons the whole inner-`Filter{SeqScan}`
unwrap on a `false`, so teaching it 20 more kinds can only *open* the unwrap on shapes that
declined before. What the row could not know is whether any such shape reaches this site. None
does. TPC-H is **22/22 MATCH in `MODE=strict-text`** — byte-identical, not merely structurally
equal — and TPC-DS SF0.5 is **96/96 byte-identical `EXPLAIN`**. The conjuncts that actually
arrive on an inner `Filter{SeqScan}` at a Semi/Anti/Inner join were already inside the old
12-arm set on both benchmarks. This is a negative result with teeth: goopg's `EXPLAIN` prints
residual predicates in full, so a flipped unwrap decision would have shown as a conjunct moving
from a leaf scan's `Filter:` onto the `Nested Loop`'s.

**The SF0.5 answer sweep still ran, because the plan gate is blind to what the conversion
actually changed on the queries it does touch.** The old arms *rebuilt* `*BinaryOp`, `*UnaryOp`
and `*FuncCall` from a field list instead of copying the struct, and the lists were stale:
`BinaryOp.ResultType`, `FuncCall.Variadic` and `FuncCall.ReturnType` were **dropped on every
hoisted conjunct**. `shallowCloneExpr` copies the whole struct, so they now survive — a silent
type-metadata loss removed, on a path where `EXPLAIN` renders both versions identically because
it prints predicates by name. 99 cells, **PASS 83 / TIMEOUT 12 / MISMATCH 0 / CKMISMATCH 0 /
ERROR 0**, 50 value checksums, every one equal to the `m0125-0003-sf05-relsize-20260730`
baseline. The single differing cell is **Q72 `TIMEOUT 307 s` → `PASS 313 s`**, which is a cap
flap and **not a rescue**: the newer run is slower, still over the 300 s cap, and Q72's plan is
one of the 96 that are byte-identical. Q72 remains M0125-0005's unexplained 1.13× carried cost.

**Completeness is not the same as admission, and this commit is where that distinction had to be
written down.** `exprChildSlots` reports `*OuterColumnRef` and `*CTIDExpr` as childless leaves —
a correct description of their child structure — so a conversion driven only by "the primitive
knows this type" would have *admitted* both. Both are wrong-answer shapes here.
`*OuterColumnRef` names a scope above this join that a flat outer++inner row cannot supply (the
original walker's own documented decline, preserved). `*CTIDExpr` is worse than unsupported:
`seqScanOp` injects the block/offset pair into the *scanned* row's slot
(`MaterializedSlot.hasCTID`), so hoisting it to the NLI residual re-points it at the joined row
and it reads the **outer** side's ctid. Both are vetoed explicitly, and
`TestCloneExprShiftIdx_DeclinesRowBoundLeaves` was proved to fail with the veto arm removed
before it was trusted.

**D3's scope policy for this walker: `scopeVeto`.** Any node carrying an inner `Plan`
(`*SubqueryExpr`, `*ExistsExpr`, a lowered `*InExpr`, `*ArraySubqueryExpr`,
`*MultiAssignSubq*`) aborts the clone. The subplan's `OuterColumnRef`s are resolved against the
*inner scan's* scope; the hoist moves the conjunct one level out and silently changes what
Level 1 names, which no positional shift of the enclosing expression can repair. A Plan-*less*
`*InExpr` — `col IN (1,2,3)` — is pure same-scope and is admitted.

**Census pin DEMOTED, not deleted**, for exactly commit 1's reason: a two-arm bottom-up dispatch
survives inside the `Rewrite` closure (shift a `*ColumnRef`; veto the two row-bound leaves) and
the census attributes a closure's switch to its enclosing function. RC-1a class 48 → 47.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`make plan-diff LABEL=m0125-0005-relsize-default-stage2` 22/22 MATCH in both `structural` and
`strict-text`; TPC-DS SF0.5 `EXPLAIN` 96/96 identical; TPC-DS SF0.5 answer sweep 99/99 as above;
new pins in `internal/planner/nli_shift_arms_test.go`, every one proved to fail before the change
(the admission pins against `HEAD`'s walker, the veto pins against a veto-less conversion);
pgbench smoke via the commit hook.

**Two deviations from D4, both deliberate and both recorded as ledger rows.** *Item 3's timed
22-query TPC-H power run was NOT executed*: with `strict-text` reporting byte-identical plans the
arms are the same plan on the same engine, and a published timing could only be host noise that
a later loop would read as an effect. If any later commit in this series moves a TPC-H plan the
timed run is owed again — this reasoning does not carry over. *Item 2's
`LABEL=tpcds-round2-head` was retargeted* to `m0125-0005-relsize-default-stage2`, because
`tpcds-round2-head` predates the M0125-0005 default flip and that flip moves 22/22 TPC-H plans by
itself; diffing against it would bury this commit's signal under a previous commit's. D4's real
requirement — name the label explicitly, never let `plan-gate`'s newest-by-mtime choose it — is
kept. **Every remaining commit in this series must use the same retargeted label.**

**A false line this commit found in the gate itself.** Every SF0.5 report header captured after
M0125-0005 said `# planner-flags: GOOPG_RELSIZE_FALLBACK=unset(off)`. The flip made unset mean
*stage 2* and `=0` the opt-out; the reporter was never updated, so the artefact stated the
opposite of the regime it measured — the M0125-0011 defect class (a report naming a binary it
never ran) in its labelling form. Corrected to `unset(2)` in
`scripts/tpcds-sf05-regression.sh` in this commit. The four raw chunk files are left as the
harness wrote them and the merged report carries the correction as a note.

### Commit 3 of 8 — `visitColumnRefs` re-based onto `walkExprRefs` (2026-08-03)

Measured in `analysis/m0125-0002-c3-plans-20260803/` (LOADED host — the nightly TPC-DS stage
ran on :65435 throughout; every instrument here is EXPLAIN-only or a unit test, so nothing is
a timing).

**D2 row 3's prediction ("changes which refs get re-resolved by name") is REFUTED by
measurement, by a stronger instrument than commit 2 needed.** TPC-H is 22/22 byte-identical
(`plan_snapshots/m0125-0002-c3-before.txt` vs `-after.txt`, same-cluster fresh-server arms —
both also equal to `post-mhj-retire`, the 2026-08-02 baseline) and TPC-DS SF0.5 is 96/96
byte-identical `EXPLAIN`. But for THIS commit a plan diff cannot carry the verdict alone:
M0125-0042 established that EXPLAIN prints a predicate's Name over its Index, and Index
mutation is this conversion's only behavioural surface. So a **divergence probe** closed the
hole: a measurement-only binary (throwaway worktree, never committed) ran BOTH walker bodies
inside `visitColumnRefs` and logged any difference in the visited `*ColumnRef` stream
(pointer-for-pointer, in order). All three rebind call sites run at plan time; planning all
118 benchmark queries produced **zero deltas**. Identical visit sets ⇒ identical Index
mutations ⇒ identical executed plans, not merely identically printed ones. The ~10
newly-visited same-scope shapes (refs under IS NULL, casts, row constructors, IN-list
elements, subquery-node PARAM_EXEC Args, …) evidently never reach these sites on either
benchmark today — the walker's incompleteness was load-bearing for correctness nowhere in
the two workloads, and the conversion removes the latency of that defect class rather than a
live defect.

**D3's scope policy for this walker: `scopeIgnore`.** All three call sites rebind SAME-scope
indices; an inner plan's ColumnRefs live in the subplan's own coordinate space (rebinding
them against the outer child schema is the mirror-image of RC-1a), and an `*OuterColumnRef`
names a scope above. A subquery node's `Args` ARE same-scope (evaluated against the current
outer row) and are now visited — the old walker missed them. Unknown types PANIC
(commit 1's convention, PG's `elog(ERROR, "unrecognized node type")`, nodeFuncs.c:2667);
a silent skip is the RC-1a defect itself, and the void-visitor signature has no decline path.

**Census pin DELETED, not demoted** — unlike commits 1–2, no dispatch switch survives: the
`*ColumnRef` filter in the new body is a type assertion. This is the first deletion the
milestone's audit trail records for the eight named sites.
`internal/planner/visit_refs_arms_test.go` pins the surface: 11 newly-visited kinds (each
proved to FAIL against the old walker before conversion), the preserved arms, both scope
declines (inner plans, outer refs), and the panic — mirroring `remap_arms_test.go`.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units` PASS; TPC-H A/B byte-identical 22/22; SF0.5 EXPLAIN
A/B 96/96; probe 0 deltas / 118 queries; `tpch-spotcheck.sh` RESULT=PASS (Q12=2 Q13=35);
pgbench smoke via the commit hook. **D4 deviations:** the timed TPC-H run was again NOT
executed (byte-identical plans + zero-delta probe; ledger row 2026-08-03) and the SF0.5
answer sweep was not run — D4 owes it on "first/last/any-hunk" commits, commit 3 has zero
hunks, and the probe additionally shows the callback stream is unchanged (commit 2's
metadata-loss concern does not arise: the old body was read-only, it rebuilt nothing).
**Label note for commits 4–8:** `m0125-0005-relsize-default-stage2` is itself stale now —
`e85e5347` (M0126-0011) retired MHJ packing and moved 19/22 TPC-H plans; the current
baseline label is **`post-mhj-retire`**, and a same-cluster A/B remains the
staleness-immune instrument.

**Found and fixed en route (separate commit `4fb87456`):** `TestMHJParallelNoDuplicates`
had been red at HEAD since `e85e5347`, which updated the three planner-side MHJ tests for
the retired default but missed `internal/executor/parallel_mhj_test.go` — the units
pre-commit gate was broken for every commit until repaired. Both tests in that file now opt
in via `SetMHJPackingEnabled(true)` (the identity test had gone silently vacuous).

### Commit 4 of 8 — `visitColumnRefsForTable` re-based onto `walkExprRefs` (2026-08-03)

Measured in `analysis/m0125-0002-c4-plans-20260803/` (QUIET host — the nightly batch
`20260803-013955` ended at 03:52, its scheduler asleep ~22 h; this is the first commit in
the series measured without co-load).

**D2 row 4's prediction ("a first-order shape mover") is REFUTED by measurement.** TPC-H is
22/22 byte-identical (`plan_snapshots/m0125-0002-c4-before.txt` vs `-after.txt`,
same-cluster fresh-server arms; both == the `post-mhj-retire` lineage, verified against
`m0125-0002-c3-after.txt`), TPC-DS SF0.5 is 96/96 byte-identical `EXPLAIN` (`head/` vs
`c4/`), and the divergence probe closed the residual hole: a measurement-only binary
(throwaway worktree, never committed) computed `tableForCol` — the walker's ONE live
consumer — with BOTH bodies and logged `C4DELTA` on any disagreement in the returned table
attribution. Planning all 118 benchmark queries produced **zero deltas**. Identical
attributions ⇒ identical local-filter partitioning and join-edge classification ⇒ the
zero-hunk plan diffs are load-bearing, not lucky. The headline semantic change —
`col IN (subquery)` now attributes to col's table instead of -1, because the old `InExpr`
arm returned before visiting ANYTHING when `Plan != nil` — evidently never decides a
partition on either benchmark today.

**D3's scope policy for this walker: `scopeIgnore`.** `tableForCol`'s cumOffsets
attribution is only meaningful for indices in the current scope's coordinate space; an
inner plan's ColumnRefs index the subplan's own schema and an `*OuterColumnRef` names a
scope above, so neither reaches `onIdx` (the old walker's documented declines, preserved).
A subquery node's PARAM_EXEC `Args` are same-scope and now contribute, as does the Operand
of a Plan-carrying `InExpr`. Unknown types PANIC (commit 1's convention).

**Census pin DELETED (second deletion in the series; RC-1a pinned population 48 → 47)** —
no dispatch switch survives; the `*ColumnRef` filter is a type assertion.
`internal/planner/visit_refs_for_table_arms_test.go` pins the surface: 11 newly-visited
kinds, preserved arms (including the Q12 `IN`-list descent), both scope declines, the
`tableForCol` IN-subquery behaviour pin, and the panic — 15 subtests proved to FAIL against
the old walker before conversion. Also removed: the DEAD `visitColumnRefsForTable` call in
`extraInScans` (`bushy.go:1703`) — a pure traversal with an empty callback; walker #7's
site (`visitColumnRefsByName`, same function) is untouched.

**En-route discovery — SF0.5 Q85's alias order is restart-nondeterministic (filed
`M0125-0047`).** The probe arm's Q85 EXPLAIN swapped `cd1`/`cd2` (two scans of
`customer_demographics`, identical estimated rows) relative to the head/c4 arms; 3 restarts
of the SAME after-binary reproduced the flip (2× cd2-first, 1× cd1-first), so it is
pre-existing tie-break instability, not a walker effect (the probe logged 0 deltas). It is
an instrument hazard for commits 5–8: an EXPLAIN A/B can report a phantom Q85 hunk. Until
-0047 lands, treat a Q85-only alias-swap hunk as suspected noise and confirm by restarting
the SAME binary before attributing it to the commit under test.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units` PASS; TPC-H A/B byte-identical 22/22; SF0.5
EXPLAIN A/B 96/96; probe 0 deltas / 118 queries; `tpch-spotcheck.sh` RESULT=PASS (Q12=2
Q13=35, 34.5 s); pgbench smoke via the commit hook. **D4 deviations (ledger row):** the
timed TPC-H run and the SF0.5 answer sweep were again NOT executed — zero hunks + a
zero-delta probe on the sole consumer; the walker is read-only, so commit 2's
metadata-loss class cannot arise. Both become mandatory at the first commit with a
non-empty diff. Next: commit 5, `exprSide`.

### Commit 5 of 8 — `exprSide` re-based onto `walkExprRefs` (2026-08-03)

D2 row 5, "decides which side a conjunct is pushed to". Measured in
`analysis/m0125-0002-c5-plans-20260803/`.

**The instrument had to be extended before the result meant anything.** `exprSide` has
exactly ONE caller — `splitEqualityForHash` — and **goopg's EXPLAIN never prints hash
keys**: `grep -c 'Hash Cond'` is 0 across all 22 TPC-H and 96 SF0.5 plans. So a change in
*which* conjunct is promoted to `LeftKey`/`RightKey` is invisible to a plan-snapshot A/B
unless it also flips the printed join algorithm. This is commit 3's hole in a new place
(there: `Index` mutation hidden behind EXPLAIN's Name-over-Index printing), and it is
closed the same way — a divergence probe on the consumer, not on the walker.

**D2 row 5's "expect hunks" prediction is REFUTED by measurement**, as commit 4's was.
TPC-H is 22/22 byte-identical (the before arm re-derived `m0125-0002-c4-after.txt`
byte-for-byte, confirming the lineage and the instrument), SF0.5 is 96/96 once q85 is
attributed (below), and the probe — a measurement-only binary computing
`splitEqualityForHash`'s `(leftKey, rightKey, ok)` triple with BOTH `exprSide` bodies while
the live path keeps the OLD answer — logged **0 `C5DELTA` and 0 `C5SIDE` over 232 calls**
(223 TPC-DS + 9 TPC-H). Because `splitEqualityForHash` is the walker's only caller, 232 is
the COMPLETE live decision population on these benchmarks, not a sample; a `C5CALL`
positive control was added precisely so the zero could not be vacuous, and the probe arm's
TPC-H snapshot is byte-identical to the before arm (the probe is pure observation). The
newly-admitted shapes are real and unit-pinned — `IS NULL`, `IS BOOL`, `CollateExpr`,
`RowExpr`, `IS DISTINCT FROM`, literal-list `IN`, plus the row-independent leaves — but no
`=` conjunct on either benchmark carries one in an operand position today.

**D3's scope policy for this walker: `scopeVeto`** — the first in the series. A node
carrying an inner `Plan` is not a per-row hashable key, and the veto preserves the old
fall-through decline regardless of what the node's same-scope `Args` merged to (pinned:
`SubqueryExpr` with a one-sided `Args` must NOT be rescued). `*OuterColumnRef` and
`*CTIDExpr` are vetoed explicitly for the reason commit 4 recorded — `exprChildSlots`
correctly reports both as childless leaves, so a completeness-driven conversion would
ADMIT them: an outer ref is fixed only per outer binding, so a cached hash table would go
stale across re-executions, and ctid is injected into the scanned row's slot, so a side
misattribution would hash the WRONG side's ctid. Unknown types resolve `sideMixed`, NOT
the panic of commits 3–4: this walker has always failed CLOSED (a decline costs an
optimisation, never a wrong answer), so `sideMixed` preserves the old contract while the
panic would invent a new crash surface. `*ExecParamRef`, `*TableOidExpr` and the `Merge*`
leaves join the `ParamRef` class as `sideUnknown` — commit 2's row-independence argument.

**Census pin DEMOTED, not deleted (RC-1a pinned population 47 → 46)** — for commits 1–2's
reason: the recursion and the exhaustiveness moved to `exprChildSlots`, but a two-arm
bottom-up dispatch survives inside the `Visit` closure and the census attributes a
closure's switch to its enclosing function. `internal/planner/expr_side_arms_test.go` pins
the surface: newly-classified containers (one case per kind — `IsNullExpr` proves nothing
about `CollateExpr`, which is how the original hole survived), the row-independent leaves,
every preserved arm, both classes of preserved decline, the fail-closed unknown, and — the
headline — a semantic pin on the live consumer showing `(l IS NULL) = r` now yields a hash
key pair instead of being stranded on the NL path.

**q85: the M0125-0047 hazard fired on its first use, and the protocol held.** The lone
differing SF0.5 cell was q85's `cd1`/`cd2` alias tie-swap. Commit 4 told commits 5–8 to
confirm with a same-binary restart before attributing such a hunk; doing so showed the
BEFORE binary restarted 3× and the AFTER binary restarted 4× all produce byte-identical
plans (md5 `b1bc99cf`) — the captured before-arm's ordering is the outlier, so the hunk is
instrument noise, not commit 5's effect.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units` PASS; full `internal/planner` package green;
TPC-H A/B 22/22 byte-identical; SF0.5 EXPLAIN A/B 96/96 (q85 attributed); probe 232 calls
/ 0 deltas; `tpch-spotcheck.sh` RESULT=PASS (Q12=2 Q13=35, 35.5 s); pgbench smoke via the
commit hook. **D4 deviations (ledger row):** the timed TPC-H run and the SF0.5 answer
sweep were again NOT executed — zero hunks on both benchmarks plus a zero-delta probe over
the walker's COMPLETE consumer population; `exprSide` is read-only and returns an enum, so
commit 2's metadata-loss class cannot arise. Both become mandatory at the first commit with
a genuine non-empty diff. Next: commit 6, `conjunctIsLocalEligible` + `localizeExprToLeaf`
— the first pair in the series, and the first where a fail-open ADMITS a predicate rather
than declining one (`extraInScans` starts `allMatched := true`), so completing the walker
can REMOVE predicates and the timed run should be assumed owed until the diff says
otherwise.

## Commit 6 of 8 — `conjunctIsLocalEligible` + `localizeExprToLeaf` (2026-08-03)

**Landed as one commit, as D2 row 6 requires**, because the two functions are a
producer/consumer pair with a shared invariant:
`partitionConjunctsForJoinPlanning` *moves* an eligible conjunct out of
`joinConjuncts` into `locals.byBinding`, and `attachRelationLocalFilters` is the only
thing that puts it back into the plan. The producer's admission is therefore a promise
that the consumer can rebase it; splitting the commit would have left a window where a
conjunct is judged eligible and cannot be rebased — i.e. a dropped or mis-indexed
predicate.

**Scope policy: `scopeVeto` on both sides, with asymmetric unknown handling.**

- The producer does **not** panic on an unenumerated type — it declines. A decline costs
  an optimisation (the conjunct stays in the join residual and is evaluated above the
  join), never a wrong answer, so this is the commit-5 `exprSide` treatment, not the
  commit-3/4 `visitColumnRefs*` treatment. Three declines are explicit rather than
  inherited from `scopeVeto`, because `exprChildSlots` emits `slotInnerPlan` **only when
  `Plan != nil`**: `*OuterColumnRef` (a childless leaf), and the subquery-bearing kinds
  in their unplanned form. `*ArraySubqueryExpr` / `*MultiAssignSubqRow` /
  `*MultiAssignSubqElem` join `*SubqueryExpr` / `*ExistsExpr` in that set — the old
  switch never named them, so they were admitted by accident.
- The consumer **panics** on an abort. By the time it runs, the producer has already
  accepted the conjunct over the SAME primitive with the SAME policy, so an abort means
  the pair has diverged. It cannot decline: the predicate is no longer in
  `joinConjuncts`, so returning it un-rebased or dropping it would be a wrong answer.
  `TestLeafLocalPairAgreesOnEveryExprKind` pins the invariant over all 32 kinds, which is
  what makes the panic unreachable by construction rather than by argument.

**The latent defect this closes.** Both functions were incomplete in the same direction,
which is precisely why it stayed latent — the producer usually declined what the consumer
could not rebase. `WHERE t.a IS NULL`, on a binding with `offset > 0`, in a query passing
`shouldAttachBeforeMHJ`, was judged eligible (the old 9-arm switch never descended
`*IsNullExpr`, so the walk produced zero callbacks and `eligible` stayed `true`), moved
into `locals`, then returned **unchanged** by `localizeExprToLeaf` — whose trailing
pass-through ("Constants … no ColumnRef; pass through") was true of the seven kinds it
knew and a silent lie about the other twenty-five. The leaf `Filter` then carried
FROM-cumulative indices and read the wrong column. Commit 4 widened the reachability
rather than creating it: a complete `tableForCol` attributes `t.a IS NULL` to a binding
where the old one answered −1.

**D2 row 6's shape-move prediction is REFUTED by measurement** — TPC-H A/B **22/22
byte-identical** (the before arm re-derived `m0125-0002-c5-after.txt` byte-for-byte, so
the instrument is stable across loops), SF0.5 `EXPLAIN` A/B **96/96 byte-identical**, and
a divergence probe on BOTH functions at all three live call sites logged **0 `C6ELIG` /
0 `C6LOC` / 0 `C6ABORT`** over **277 eligibility calls + 175 localization calls** across
118 planned queries, with `C6CALL`/`C6LOCC` positive controls so the zeros cannot be
vacuous. The probe was mandatory, not belt-and-braces: eligibility changes ARE visible in
the plan text (a leaf `Filter` appears or disappears), but the `Index` rebase is
**invisible** — goopg's EXPLAIN prints column names (M0125-0042) — so the probe compares
localized trees by `exprIdentityKey`, which includes `Index`. Commit 2's metadata-loss
class cannot arise here: `shallowCloneExpr` is a whole-struct copy, where commit 2's old
arms rebuilt nodes from stale field lists. Evidence
`analysis/m0125-0002-c6-plans-20260803/` (incl. `probe-source.md`).

**Census pins moved in BOTH directions in one commit** — the first time in the series.
`conjunctIsLocalEligible` DEMOTED (`walkerPending` → `nonRecursiveClassifier`: its veto
dispatch survives inside the `Visit` closure, and the census keys a site by its enclosing
function) and `localizeExprToLeaf` DELETED (`cloneExprRefs` left it with a `*ColumnRef`
type assertion and no switch at all). RC-1a 46 → 45.

**Gates.** `RALPH_PRECOMMIT_SCOPE=units` PASS; full `internal/planner` package green; 48
new pin subtests proved to FAIL against the old bodies first; `tpch-spotcheck.sh`
RESULT=PASS (Q12=2 rows / 23.1 s, Q13=35 rows / 11.3 s); pgbench smoke via the commit
hook. **D4 deviation (ledger row):** the timed 22-query TPC-H run and the SF0.5 answer
sweep were again not executed — commit 5 declared them mandatory *here*, and the
measurement discharged the premise that made them so (zero hunks on both benchmarks plus
a zero-delta probe over the complete live population of both functions, including the
index field EXPLAIN hides). Because that is now four consecutive byte-plan-identical
commits, the ledger converts the per-commit obligation into **one cumulative timed TPC-H
run at commit 8**, covering commits 2–8 as a block: a per-commit run over identical plans
measures host noise, but a cumulative drift across seven commits is a real question.
Next: commit 7, `visitColumnRefsByName` — the last and largest, whose consumer
`extraInScans` starts `allMatched := true`, so completing it removes conjuncts from
`MultiHashJoin.Filters` directly.

---

## Commit 7 of 8 — `visitColumnRefsByName` (2026-08-03, LAST of the series)

`bushy.go:visitColumnRefsByName` is re-based onto `walkExprRefs` under
**`scopeSignal`**, the policy D3 predetermined for it, and the conversion **changes the
signature**: it now returns whether the name test COVERED the expression.

The signature is the whole commit. This walker's three consumers do not read the callback
stream — they seed a verdict `true` and falsify it only from inside the callback, so a
conjunct built entirely from unenumerated kinds produced zero callbacks and returned a
**vacuous true**. §"Why this is not just fixing stale indices" identified that as the
series' largest blast radius, because for `extraInScans` the vacuous true is not a missed
optimisation but an ADMISSION: the conjunct is captured into `MultiHashJoin.Filters` and
evaluated on the MHJ output row. D3's instruction — treat "an opaque child exists" as
*not matched* — is discharged by `return total && allMatched`.

**"Opaque" is wider than D3's inner plans, and the widening is the design decision of this
commit.** A name-based scope test cannot certify anything that reads row data without
naming the column it reads, so four such cases clear `total` alongside the scope crossing
and the unknown type: `*OuterColumnRef` (names a DIFFERENT scope — matching it against
this subtree's names would be coincidence, not evidence; commit 2 vetoed it on the
rewriting side for the same reason), `*CTIDExpr` (`seqScanOp` injects the scanned row's
block/offset into its slot), `*MergeWholeRowRef` (a composite materialised from ctx over
the whole row), and a `*ColumnRef` whose `Name` is empty — "for diagnostics" per its own
struct comment, and empty on some construction paths, which the old body skipped silently.
`*ParamRef`, `*ExecParamRef`, `*TableOidExpr` and `*MergeActionExpr` stay total: they read
no row column. The rule, not the list, is what the pin table encodes, so a 33rd Expr type
forces a decision rather than inheriting one.

**The third call site takes TWO escapes, and they must not be merged.**
`pushOuterQualsIntoLaterals` already had one — `!allIn && len(leftNames) > 0` — which
means "we cannot enumerate the NODE's columns" and deliberately falls back on the index
verdict from `classifyConjunctSide`. `!total` means "we cannot enumerate the CONJUNCT",
where that fallback is worthless: `classifyConjunctSide` is built on `walkColumnRefsImpl`,
which has no `default:` either, so an unenumerated kind is invisible to BOTH tests and a
conjunct wrapping e.g. an `*ArraySubqueryExpr` reads as conclusively `sideLeft` on its
other operand alone. `!total` is therefore unconditional and returns before the leftNames
escape is consulted.

**Measurement: no plan moved, and proving that took four sweeps rather than one.** TPC-H
A/B **22/22 byte-identical**, and byte-identical against `post-mhj-retire` as well — so
the CUMULATIVE TPC-H diff across commits 1–7 is empty, not just this commit's. The SF0.5
EXPLAIN A/B first read 95/96, with TPC-DS **Q85** showing its two `customer_demographics`
aliases swapped between two join positions of identical cost. That hunk is the
INSTRUMENT: `before` vs a second `before` sweep is 96/96; a second *after* sweep
reproduces the `before` plan set 96/96 and differs from the first *after* sweep only at
Q85; and three fresh single-query server starts per binary produced the same ordering all
six times. The divergence probe closed it — while planning Q85 the old and new bodies
disagreed on **zero** verdicts. Q85 has a nondeterministic join-order tie-break that
surfaces only in the long-lived-server sweep context; ledger row, because the same
instrument accepted commits 2–6 on single runs.

**Census pin DEMOTED** (`walkerPending` → `nonRecursiveClassifier`): the three-arm
"reads row data but names no column" veto survives inside the `Visit` closure, and the
census keys a site by its enclosing function. RC-1a 45 → 44. Eighteen new pin subtests in
`visit_refs_byname_arms_test.go`, each proved to fail against the old body first — by
reproducing that body under a `_c7old` name, because the signature change means the pins
cannot be *compiled* against the pre-conversion source the way commits 3–6 were
(`analysis/m0125-0002-c7-plans-20260803/oldbody-harness.md`).

**Gates.** `RALPH_PRECOMMIT_SCOPE=units` PASS; full `internal/planner` green;
`tpch-spotcheck.sh` RESULT=PASS (Q12=2 rows / 21.6 s, Q13=35 rows / 11.2 s); pgbench smoke
via the commit hook. **The cumulative timed TPC-H run owed here is answered with a
different instrument, and the substitution is a ledger row.** D4 item 3 exists to catch a
regression caused by a moved plan; the cumulative diff is byte-empty, so an execution run
re-measures an unchanged plan set at a noise floor (round-5 §3: 2–8 % unattributable)
wider than anything it could attribute. What the eight conversions did change is planning
cost — a hand switch became a driver that builds an `[]exprSlot` per node — which a plan
diff cannot see and an execution run would bury under 20-minute scans. Measured directly
(`capture-plantime.sh`, 22 queries × 5 `EXPLAIN` sweeps in one session with `\timing`):
4.41 → 4.54 ms total, ~6 µs per query, with a *within-arm* spread per query wider than the
between-arm delta. Unchanged within resolution. The execution arm stays owed at milestone
level.

**The series is complete**: all eight walkers named in §2 are on the exprwalk primitives,
RC-1a's pinned population is 50 → 44, and M0127-P5.2 has the stable base it waits on.
