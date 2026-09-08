# C-10c (P4-00c) — outer-join qual-placement contract for upper rels

Status: accepted (contract + fixture; deliberately **not** a refactor).
Filed by TODO_ALL.md C-10c, scoped by
`analysis/planner-refactor-take3/c10-p400-scoping-20260905/README.md` §3.

Reads: `docs/design/planner-c02-qual-placement/DESIGN.md` (C-02, all four
slices landed), take3 `08-target-design.md` §7.

Oracle: PG 18.3 under `./postgres/` (read-only). Line numbers below are
`global -x` hits in that tree; goopg line numbers are HEAD of
`plan-narrowing-and-etc` on 2026-09-05.

---

## 1. What this item is for

Phase 4 turns the upper planner into paths. Five of its items delete or
replace plan-tree nodes that today carry an *implicit* outer-join safety
property, and one landed compute-only payload
(`stampAggregateInputTarget`) is where the first applying cut will go.
The property is not written down anywhere those items will look, and it
is invisible to every gate the repo runs: violating it produces the right
number of rows with the wrong values.

So this item writes the property down per Phase-4 checkbox, and pins a
fixture (`internal/optimizer/upper_target_oj_contract_test.go`) that goes
red if the guard is dropped.

## 2. `reduceOuterJoins` needs no Phase-4 change

`reduceOuterJoins` (`internal/optimizer/reduce_outer_joins.go:36`) is an
AST-level prep pass with exactly one production call site,
`internal/optimizer/planner.go:2730`, immediately before
`deconstructJointreeScoped` on the next line. It runs once per planning
scope, on `s.FromExprs` / `s.Where`, and it is finished before any path
or plan node exists. Phase 4 does not move it, does not re-run it, and
does not read it.

That ordering is already load-bearing, and C-01/C-02 already say so:

- `internal/optimizer/specialjoin.go:105-109` —
  "SEMI never reaches deconstruction — the parser produces only
  INNER/LEFT/RIGHT/FULL/CROSS from SQL (select.go:1246); SEMI/ANTI are
  planner-internal, and only ANTI arrives here (via reduceOuterJoins
  LEFT→ANTI demotion, which runs before deconstruction in
  planFromClause)."
- `internal/optimizer/outerjoin_delay.go:140-145` —
  "`check_outerjoin_delay` no longer exists upstream (removed in the
  nullingrels rework); strictness does NOT exempt a qual here —
  strictness feeds `reduceOuterJoins` demotion separately, which runs
  before deconstruction, so every link this ever sees is a surviving
  outer link."
- `internal/optimizer/planner.go:2728-2729` — the call site's own comment:
  "M0128-P4.1: reduce outer joins before deconstruction so that demoted
  joins enter the joinlist as plain INNER joins."

Consequence for Phase 4, stated once so no item has to re-derive it:
**every outer-join link a Phase-4 item can see has already survived
demotion.** There is no strictness escape hatch left. A link that is
still LEFT/RIGHT/FULL at path-generation time null-extends for real, and
the only remaining lever is placement.

Two edges recorded by the scoping analysis and re-stated here because
they bound the contract: the pass is per-scope and name-based, so a
parent predicate can never demote an outer join inside a derived table
(this couples to C-10d); and the pass is the *only* producer of ANTI, so
an item that synthesises join types must not invent one.

## 3. `pushSingleSideQualsIntoInnerJoinInputs` is the sole oracle consumer

`delayedAboveOJ` (`internal/optimizer/outerjoin_delay.go:150`) has exactly
one production consumer: `pushConjunctTraced`
(`internal/optimizer/inner_join_qual_pushdown.go:341`), in its `case *Join`
arm — the single call is at `inner_join_qual_pushdown.go:452`. It is
reached from `pushSingleSideQualsIntoInnerJoinInputs`
(`inner_join_qual_pushdown.go:88`), whose one production call site is
`internal/optimizer/planner.go:1449` — the last pass of `planSelect`,
after `remapWithBindings`.

Its node walk (`inner_join_qual_pushdown.go:92-129`) has these arms:

| arm | lines |
|---|---|
| `*Filter` (the entry shape — `pushInnerJoinInputQuals`) | 93-102 |
| `*Join` | 103-106 |
| `*NestedLoopIndexJoin` | 107-110 |
| `*CTEScan` | 111-113 |
| `*Project` | 114-116 |
| `*Sort` | **117-119** |
| `*Limit` | **120-122** |
| `*Aggregate` | **123-125** |
| `*WindowAgg` | **126-128** |

The four bolded arms exist for one reason: to keep descending past an
upper-planner node so the residual `Filter` below it is still found.
Phase 4 deletes those nodes as plan-tree carriers of upper-planner
semantics, and with them these arms. When the arms go, the *walk* that
reaches the delay oracle goes with them.

There is no `*Distinct` / `*DistinctOn` arm today, and no
`*MaterializedCTEScan` arm: the walk simply stops there. Stopping is the
safe direction (no push happens at all), so this is a missed
optimisation, not a hazard — but it means C-16 retires no arm and
therefore has no arm-shaped reminder to consult the oracle.

## 4. The failure mode, in plain terms

Two distinct operations cross an outer-join link, and both are wrong for
the same reason:

1. **Pushing a qual below the link.** A qual reading the nullable side,
   evaluated below the join, tests the *base* row. Above the join the
   same row may have been null-extended, where the qual's answer is
   different (`o.amount > 10` is false for a NULL-extended `o`, but the
   base row it was extended from never reached the test). C-02a/b/c/d
   already guard this; `delayedAboveOJ` *is* that guard.

2. **Narrowing an upper-rel input target across the link.** An upper rel's
   input target is not just a column keep-list: it is a list of
   *expressions* the layer below must compute (`make_group_input_target`
   flattens non-group expressions into their component Vars precisely so
   the expressions themselves stay above). If an applying cut evaluates
   one of those expressions below the link, the result is computed on the
   base row and does **not** become NULL when the row is null-extended.
   `sum(o.amount)`'s argument, a `GROUP BY o.x + 1` key, a `coalesce`
   over a nullable-side column — each of them silently becomes "the value
   this row would have had if it had matched".

PG's guard for (2) is the PlaceHolderVar: an expression that must go to
NULL when a join null-extends it is wrapped so the nulling is explicit.
**goopg has no PlaceHolderVar machinery at all** —
`internal/optimizer/specialjoin.go:109` states it outright
("PlaceHolderVar handling is vacuous: goopg has no placeholder
machinery"), and there is no `PlaceHolder`/`nullingrel` type anywhere in
`internal/optimizer/`. So goopg has exactly one available guard for both
operations: **do not evaluate below the link at all**, decided by
`delayedAboveOJ`.

Why this is worth a design doc: the wrong answer here is *shaped like a
right answer*. Row counts are unchanged — the narrowing does not add or
remove rows, it changes a value inside surviving rows. The TPC-H row
anchors, the TPC-DS SF0.5 row-count gate, the pgbench smoke and the plan
gate all pass. Only a values-diff catches it, and only on a query that
both aggregates over a nullable side and has a plan where the narrowing
lands below the link. (See the standing lesson: a row-count gate cannot
catch a plan-shape regression.)

## 5. Per-Phase-4-item contract table

"Arm retired" = which arm of `pushSingleSideQualsIntoInnerJoinInputs`
(§3) stops existing when that item lands. "Re-assert at" = the place in
the item's own replacement where the delay test must be consulted, named
in both trees.

| item | arm retired | PG equivalent | where the delay test must be re-asserted |
|---|---|---|---|
| **C-11** P4-02 upper `RelOptInfo`s | none directly — it creates the rels the other items hang paths on, and it is what makes the four nodes stop being the upper planner | `apply_scanjoin_target_to_paths` (`plan/planner.c:7829`); `create_projection_path` / `apply_projection_to_path` (`util/pathnode.c:2902` / `:3012`) | At the scan/join → upper-rel boundary. PG's structural safety is that the scan/join target is applied to the **final scan/join rel**, which by construction sits above every outer-join link in the scope; the only place PG deliberately pushes the work *lower* is below a partitioning `Append` (`planner.c:7843-7862`), which is not a null-extending link. C-11 must make the same statement true in goopg: an upper rel's input is the join-search root, and a target it wants is materialised as a projection **above** that root — never folded into a join input. C-11 must also keep the per-link SJI reachable from an upper rel (today `planJoinDelaySJI`, `outerjoin_delay.go:110`, derives it from the plan `*Join` node; once the upper rels exist, the scope's SJI list is the natural home), or items C-12/C-15/C-18 have no oracle to consult. |
| **C-12** P4-03 upper-rel `PathSort` | `*Sort` (117-119) | `make_sort_input_target` (`plan/planner.c:6441`) | At `stampSortInputTarget` (`internal/optimizer/sort_input_target.go:202`) when it grows its applying cut. `make_sort_input_target` postpones expensive/SRF/volatile expressions to *above* the Sort, which is the same direction the delay test demands; the delay test is the additional condition that an expression may not be postponed *downward* past a link. Any Sort whose input target reaches a nullable side must take its input from at-or-above that link. |
| **C-13** P4-04 bounded / top-N sort *(added: otherwise the `*Limit` arm has no owner)* | `*Limit` (120-122) | `create_limit_path` (`util/pathnode.c:4118`); `cost_sort`'s `limit_tuples` arm | A bound is not a target, so there is no narrowing to delay — but a bound applied **below** an outer-join link stops producing preserved-side rows that the link would still have null-extended, which is the same wrong-answer class by a different route. C-13's contract is simpler than a delay test and must be stated as such: a physical bound (top-N sort, `Limit` node) may only sit above the link; `limit_tuples` may propagate downward as a *costing* hint only. |
| **C-15** P4-06 `create_grouping_paths` | `*Aggregate` (123-125) | `create_grouping_paths` (`plan/planner.c:3780`); target from `make_group_input_target` (`plan/planner.c:5528`) | **At `stampAggregateInputTarget` (`internal/optimizer/group_input_target.go:268`), which is compute-only today and always called with `above == nil` (`planner.go:1527`, `:7743`, `:8138`, `:8317`).** This is the fixture's pin (§6). The applying cut must, before narrowing, take the union of `SourceTableIdx` over the kept input columns *and* over every expression the Aggregate evaluates, and refuse the narrowing at any link where `delayedAboveOJ` says delay. `make_group_input_target` is exactly the right oracle to copy: it flattens everything except GROUP BY items into component Vars *so that the expressions stay above*, which is what keeps a nullable-side expression out of the layer below. |
| **C-16** P4-07 `create_distinct_paths` | **none** — the pass has no `*Distinct`/`*DistinctOn` arm, so it already stops there (fail-closed, no push) | `create_distinct_paths` (`plan/planner.c:4790`); `create_final_distinct_paths` (`plan/planner.c:5043`) | Neither PG function builds a target of its own: both take `input_rel` and add paths at the same target, so the delay obligation is **inherited** from whoever produced `input_rel` (C-12's sort input target, or C-15's group input target). C-16's obligation is therefore negative and must be written into its commit: it must not introduce a "distinct input target" narrowing. If it ever does, the C-15 rule applies verbatim — and because C-16 retires no arm, nothing else will remind it. |
| **C-17** P4-08 `tuple_fraction` end-to-end | none — this is a costing input, not a placement change | `create_ordered_paths` (`plan/planner.c:5308`) and the `tuple_fraction` threading through `create_grouping_paths` / `query_planner` | No delay test applies to a cost. The obligation is the boundary between the two: `tuple_fraction` may reach every rel's *costing*, but it may not be realised as a row-count-limiting **executor** node below an outer-join link — that is C-13's rule, and C-17 is the item most likely to be tempted to shortcut into it. C-17 must also not be surprised that its input got worse: C-10a's landed `dNumGroups` fix is what makes the fraction meaningful for grouping sets. |
| **C-18** P4-09 `create_window_paths` | `*WindowAgg` (126-128) | `create_window_paths` (`plan/planner.c:4533`); target from `make_window_input_target` (`plan/planner.c:6193`) | At `stampWindowInputTarget` (`internal/optimizer/window_input_target.go:249`) when it grows its applying cut — same rule as C-15, same oracle. `make_window_input_target` keeps every PARTITION BY / ORDER BY expression unflattened in the input target (it must be computed before the window runs) and flattens the rest; the flattened remainder is precisely what must not sink past a link. |

The qual half of all of the above — "which rel does a clause get filed
on" — has a single PG home,
`distribute_qual_to_rels` (`plan/initsplan.c:2545`), whose
`outerjoin_nonnullable` / `ojscope` / `incompatible_relids` computation
is what `delayedAboveOJ` is a reduction of (already cited at
`outerjoin_delay.go:138-140`). Whatever replaces
`pushSingleSideQualsIntoInnerJoinInputs` in Phase 4 files quals the way
that function does, or it inherits the same obligation node by node.

## 6. The fixture

`internal/optimizer/upper_target_oj_contract_test.go`, five tests over one
shape:

```
Aggregate[ GROUP BY c.name, sum(o.amount) ]     <- upper target reads the NULLABLE side
  Filter[ o.amount > 10 ]                       <- residual qual on the NULLABLE side
    Join LEFT                                   <- the link
      SeqScan c   srcIdx 1 -> [id, name]        (preserved)
      SeqScan o   srcIdx 2 -> [cust, amount]    (nullable)
```

merged layout `[0 c.id, 1 c.name, 2 o.cust, 3 o.amount]`.

1. `TestC10cOracleRefusesNullableSideUpperInput` — the oracle arm. For
   each expression the Aggregate reads from its input row, and for the
   residual qual, attribute it with `qualSrcRelSet` and ask
   `delayedAboveOJ` against `planJoinDelaySJI(join)`. Nullable-side
   readers must delay; the preserved-side group key must not. This is the
   arm that is sensitive to the guard itself.
2. `TestC10cAggregateInputTargetNarrowingCrossesLeftLink` — the C-15
   tripwire. `stampAggregateInputTarget(agg, nil)` produces keep
   `[1, 3]` = `(c.name, o.amount)`, a strict narrowing of the 4-column
   input; column 3 is attributed to the nullable side and the oracle
   refuses it. The test then asserts the stamp was **inert**: same
   `agg.Child` pointer, same input width, same join-input widths, same
   residual predicate. An applying cut that narrows this shape without
   consulting the oracle changes one of those and the test goes red with
   a message naming this document.
3. `TestC10cPreservedOnlyUpperTargetIsPermitted` — non-vacuity control.
   Same shape, Aggregate reading only `c.name`: keep `[1]`, the oracle
   permits the narrowing. Proves the §6.2 verdict is derived from the
   shape, not constant.
4. `TestC10cNullableSideQualNeverDescendsThroughAggregateArm` — the
   consumer arm, driven through `pushSingleSideQualsIntoInnerJoinInputs`
   so the `*Aggregate` arm (123-125) is on the path. The nullable-side
   qual stays in the residual and nothing is planted below the link.
   (Honest note, inherited from C-02b's own review: this verdict is
   *over-determined* — `joinRestrictionSides` declines the nullable side
   before the oracle is consulted — so this test pins placement, not the
   guard.)
5. `TestC10cPreservedSideQualMovesThroughAggregateArm` — the
   guard-sensitive consumer arm. A preserved-side qual above the same
   LEFT link must **move** (C-02d), which requires a positive
   `delayedAboveOJ` verdict; a broken guard degrades the move to a copy
   and the residual survives, which the test catches.

**Negative controls run (2026-09-05), all three reverted afterwards; the
suite is green at HEAD.**

| control | edit | red |
|---|---|---|
| 1 — guard inverted | `delayedAboveOJ`'s `case parser.JoinLeft` reads `sj.SynLefthand` as nullable instead of `sj.SynRighthand` | tests 1, 2, 3, 5 |
| 2 — guard dropped | `delayedAboveOJ` returns `false` unconditionally (what "the applying cut forgot to ask" looks like from the oracle side) | tests 1, 2 |
| 3 — C-15's cut simulated | `stampAggregateInputTarget` inserts a narrowing `*Project` over `agg.Child` after stamping, with no delay consultation | test 2 (the tripwire) |

Control 3's message, verbatim, is the one a future C-15 author will read:

> `stampAggregateInputTarget REPLACED the Aggregate's input
> (*optimizer.Project) on a shape whose input target reaches the NULLABLE
> side of a LEFT join. If this is C-15's applying cut: it must consult
> delayedAboveOJ over the crossed links before narrowing, and refuse
> here.`
>
> `Aggregate input width 4 -> 2: an upper input target was narrowed
> across a LEFT link without a delay proof.`

Control 1 is the informative one for the *shape* of the guard: it turns
tests 1, 2 and 3 red in **both** directions at once (the nullable column
stops delaying AND the preserved column starts delaying), and turns test
5 red because an over-refusing guard silently demotes every C-02d move
back to a duplicate. Control 2 shows the asymmetry the fixture is built
around: dropping the guard is invisible to the consumer tests (4 and 5),
because `joinRestrictionSides` over-determines their verdicts — only the
oracle arm and the tripwire see it. That asymmetry is exactly why this
item is a fixture and not a code change.

**What the fixture cannot do.** It calls `stampAggregateInputTarget`
directly. If C-15 leaves that function inert and instead applies its cut
in a *new* function called from `planner.go`, test 2 will not see it.
That is why §5's table names the re-assert site per item rather than
relying on the tripwire alone, and why C-15's own gate stays a
values-diff on both suites.

## 7. Out of scope (stated, not forgotten)

- FULL-link placement, `pseudoconstant` quals and volatile-function
  pushing remain out of scope exactly as C-02 §5 left them.
- The derived-table boundary (C-10d) interacts with this contract:
  `reduceOuterJoins` is per-scope, so a link inside a derived table is
  demoted — or not — without reference to the enclosing predicate, and an
  upper rel that sits above a foreign planning scope inherits an SJI list
  it did not build. C-10d owns that decision; this document only records
  that the guard must be re-derivable on the far side of it.
- No code in `internal/optimizer/` changes under this item. It is a
  contract plus a fixture, by construction.
