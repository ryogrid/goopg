# R42 — the qual-pushdown descent cannot cross a `Project` (K76)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN rev 2 — reviewed, five corrections adopted (§9).
Prerequisite for R41's implementation, which
was built, measured and reverted precisely because this gap turned an
eligibility gain into a qual-placement parity regression.*

## 1. The measurement (instrumented, traces reverted)

R41 admitted TPC-DS Q78's CTE bodies to the PG-shaped search. Values stayed
correct and the sweep passed, but each body's `date_dim` scan went from

```
Seq Scan on date_dim  (rows=149)   Filter: (d_year = 1998)
```

to `rows=73049` with no filter, and Q78 ran 14 s → 26 s. PG **does** have
that restriction, so this is a **qual-placement parity regression**, not
merely a timing one — which is why R41 was reverted rather than landed.

Two probes located it exactly (both reverted):

```
CTEPUSH conj=*BinaryOp body=*Project ok=false
CTEPUSH project remap ok=true nOut=51 nTargets=51 child=*optimizer.Join
CTEPUSH join type=0 pushable=true L=*optimizer.Join R=*optimizer.Project
```

The conjunct maps through every `Project`/`Aggregate` layer of the CTE body
fine, reaches the inner join, and picks the correct side — and then the
descent **stops, because that side is a `*Project`**.

**Ruled out by the same probes:** `joinRestrictionSides` refusing SEMI/ANTI
was the obvious hypothesis and is **not** the cause. No `pushable=false`
was ever traced; every join reported `pushable=true`. Recording this
because it is the third time this session an inferred cause was wrong and
an instrumented one was right.

## 2. Root cause

`pushConjunctTraced` (`inner_join_qual_pushdown.go:341`) has arms for
`*Filter` and `*Join`, then falls through to a terminal that accepts the
node only if `innerJoinPushEligibleInput` says so — `*CTEScan`,
`*MaterializedCTEScan`, or a base-relation leaf scan
(`:496-502`). A `*Project` is none of those, so the push declines.

This is **not** anti-specific and not new. It was simply *unreachable*:
these CTE bodies all declined the search, and the legacy tree it fell back
to has no `Project` between the join and the scan. The PG-shaped search's
boundary republishes binding order through exactly such a `Project`, so
every body the search admits meets it. R41 made the shape reachable; the
gap was always there.

## 3. The fix

Give `pushConjunctTraced` a `*Project` arm that remaps the conjunct into
the child's coordinate space and continues the descent — mirroring the arm
`pushConjunctIntoCTEBody` (`cte_inline_pushdown.go:188`) already has, and
reusing the **same** helper:

```go
case *Project:
    // Rev 2, review corrections 2 + 3: fail closed on the two shapes the
    // remap cannot judge, BEFORE calling it.
    if x.IsolatedScope || x.Child == nil || len(x.Output()) != len(x.Targets) {
        return n, false
    }
    mapped, ok := remapConjunctThroughProjection(c, x.Output(), x.Targets, x.Child.Output())
    if !ok {
        return n, false
    }
    // Rev 2, review correction 1: this round is PLACEMENT-ONLY.
    st.proven = false
    repl, ok := pushConjunctTraced(x.Child, mapped, st)
    if !ok {
        return n, false
    }
    x.Child = repl
    return x, true
```

This matches the `*Join` arm's own shape: remap, recurse, install the
replacement on success, return the node.

### `st.proven = false` — the review's most valuable correction

Rev 1 argued the arm should leave `st` untouched because a projection
crosses no outer join. That reasoning is *logically* right and
*operationally* backwards.

`proven` has exactly ONE consumer — `inner_join_qual_pushdown.go:167`,
`if tr.proven && !tr.planted { continue }` — which **deletes the conjunct
from the residual `Filter`**. Today a `Project` between a join and its
input makes the descent return `false`, so the conjunct is unconditionally
KEPT. An arm that returns `true` with `proven` still `true` would newly
**remove the qual from above** on a whole class of trees. That is a
residual-dropping MOVE, not the deeper placement this round is scoped to,
and its soundness would rest entirely on the remap being exact.

The remap has two fail-OPEN seams, which is what makes that unacceptable
here: `remapConjunctThroughProjection:281` skips the self-side name check
when the ref is unnamed (and unnamed refs demonstrably occur —
`innerJoinPushTarget:440-446` records the same), and it never checks
`len(Output()) == len(Targets)`. Correction 3 closes the second; the first
is why the move stays off.

So R42 sets `proven = false` and is deliberately **copy-only**: the qual is
planted below AND kept above. That is exactly what §4's prediction and §5's
success criterion measure. Enabling the move is a separate round with its
own proof. The cost is nothing measurable; the benefit is that the one
channel that could silently drop rows stays shut while a wide surface is
swept.

### Why the descent below a `Project` is shape-safe (review's stronger reason)

Rev 1 said "a `Project` never null-extends". True but weak. The stronger
statement: only `*Filter`, `*Join` and `innerJoinPushEligibleInput`
terminals are accepted below, so a `Project` over an `Aggregate`, `Limit`,
`WindowAgg`, `SetOp`, `Sort` or `ProjectSet` still declines at the terminal
gate (`:476`). Set-returning target lists are a separate node
(`*ProjectSet`), which this walk has no arm for. That closes the
"child with different row multiplicity" hazard outright rather than by
argument.

### What the remap already guarantees

`remapConjunctThroughProjection` (`cte_inline_pushdown.go:265`) is
fail-closed in three ways this round relies on and does not re-judge:

- vetoes `*OuterColumnRef` and `*FuncCall` outright;
- requires every referenced target to be a plain `*ColumnRef` — a computed
  or volatile projection target declines, so a conjunct is never pushed
  through an expression that changes its meaning;
- name-checks the reference on both the self and child schemas, so a
  positional slip declines instead of silently reading a neighbour column.

It also ends in `cloneExprRefs`, which is what supplies the expression
freshness §7 turns out to depend on.

## 4. Blast radius — the reason this is its own round

`pushConjunctIntoSubtree{,Traced}` has **two** production callers, and the
arm serves both:

- `cte_inline_pushdown.go:240,252` — the CTE-body path (K76's case);
- `inner_join_qual_pushdown.go:161` (`pushSingleSideQualsIntoInnerJoinInputs`)
  and `:739` — the general single-side inner-join pushdown, which runs for
  **every** statement.

So this can move quals — and therefore costs, and therefore plan choices —
on any searched tree that has a `Project` between a join and its input,
across both corpora. That is a much wider surface than R41's, which
touched only leaf numbering for SEMI/ANTI. It is also the direction PG
goes (restrictions reach baserel level), so movement here is expected to
*improve* qual-placement parity — but "expected" is a prediction to be
measured, not assumed.

**Prediction, recorded before implementing** (`METHODOLOGY.md` §2): this
round on its own does NOT change the decline census and is NOT expected to
produce a match. Its success criterion is narrower and must be judged on
its own terms: **the `qual-placement` parity category must not worsen on
either corpus, and plan-shape movement must be explainable**. The match
count is expected to stay 0/99 and 1/22.

## 5. How this round is verified

Because the surface is wide, the plan-shape channel is the primary reading,
not an afterthought:

1. Suites + `RALPH_PRECOMMIT_SCOPE=units`.
2. Unit tests pinning the new arm directly — four cases, three of which
   are declines, because the fail-closed properties are the ones worth
   proving rather than assuming:
   a. a conjunct pushed through `Project{Join{scan, scan}}` reaches the
      correct leaf AND is still present above (copy semantics, `proven`
      false);
   b. a projection target that is a computed expression, not a bare
      `ColumnRef`, DECLINES;
   c. an `IsolatedScope` Project DECLINES (review correction 2);
   d. a Project whose `Output()`/`Targets` lengths disagree DECLINES
      (review correction 3).
3. TPC-H values digest byte-identical; TPC-H plan STRUCTURE diffed and
   every change explained (structure was identical through R40 and R41, so
   any movement here is this round's and must be justified query by query).
4. TPC-DS SF0.5 sweep `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
   plus the sweep's own plan-diff channel read query by query.
5. Parity re-measure BOTH corpora, reporting the `qual-placement` category
   explicitly alongside the totals, plus `shape-delta.sh`.

Held uncommitted until the sweep is clean — the discipline R27 §4a adopted
after a row-dropping bug in this area.

## 6. Then, and only then: R41

With K76 closed, R41's implementation (fully specified in
`r41-anti-leaf-coordinates/DESIGN.md` §4, already built and verified to
pass every gate once) is re-applied. In that order the eligibility gain
(`leaf-count` 3 → 0, declines 8 → 5) arrives **without** the qual-placement
regression that forced its revert. Q78 still will not match — PG places
`date_dim` below the anti join, which needs Option B's 3-leaf search — and
the R41 report must keep saying so.

## 7. "Copies by default" — resolved (rev 2)

Rev 1 asked whether `x.Child = repl` was consistent with the pass's
"copies by default" property. The review resolved it: **"copy" refers to
the CONJUNCT being duplicated** (kept in the residual `Filter` AND planted
below), never to copying plan nodes. Every arm already mutates the node it
was handed in place — `:351`, `:383`, `:466/468`, and
`deriveConstAcrossJoinEquality` at `:731-736`. There is no
`copyPlanSubtree`/`cloneNode` anywhere on this path.

The real prerequisite is **expression freshness**, and the proposed arm
satisfies it for free: `remapConjunctThroughProjection` ends in
`cloneExprRefs`, which returns a new tree, so the conjunct planted below
never aliases the residual's `c`. (The `*Join` arm gets the same property
from `shiftConjunctForInput`'s deep copy, whose comment says the copy "is
essential: c stays in the residual Filter".) Had the arm passed `c`
through unchanged — as the `*Filter` arm does — it would have depended on
an ancestor having cloned, which holds from the `:161`/`:739` entries but
NOT from `cte_inline_pushdown.go:240/252`, where the entry is a `*Filter`
and `c` is the caller's own expression. That is the one aliasing trap
here, and this arm avoids it.

## 8. Pre-existing wart this arm widens (filed, not fixed)

`deriveConstAcrossJoinEquality` (`:413`, planting at `:739`) writes into
the tree BEFORE the recursion, and neither the `*Join` arm's
`return n, false` (`:463`) nor the caller undoes it. When the descent then
fails, the caller takes the `kept = append(kept, c)` branch WITHOUT
`notePushedBelow`, so a derived sibling copy sits below un-priced. A
`*Project` arm makes deep-then-fail descents more common, so it widens the
exposure without creating it. Recorded here and in the deferral ledger
rather than bundled: fixing it means unwinding a partial mutation, which
is its own change.

## 9. Review record

Adversarial subagent review, full HEAD re-derivation. It VERIFIED the arm
inventory and terminal gate, the `*Join` arm's shape, all three
`remapConjunctThroughProjection` safety properties, `Project`'s
row-preserving semantics down to the executor (`operators.go:363-406`), and
the caller set.

It corrected rev 1 on five points, all adopted: (1) `st.proven` is not
neutral — leaving it untouched would newly enable residual DELETION, so
this round sets it false and is copy-only; (2) refuse `IsolatedScope`
Projects, citing the verbatim precedent at `upper_narrow_chain.go:373-379`
— rev 1 never mentioned them, and today they are contained only by
*accident* (a view-rename Project declines because the view and body column
names differ, which fails as soon as they match); (3) refuse a
`Output()`/`Targets` length mismatch, mirroring
`cte_inline_pushdown.go:207-209`; (4) "copies by default" means conjuncts,
not nodes (§7); (5) add the two new decline cases to the test list.

It also confirmed no new exposure from lateral joins
(`joinRestrictionSides:271` refuses them) or from shared CTE/sublink bodies
(the descent stops at `*CTEScan`, and `scopeVeto` keeps the walk out of
expression `Plan` fields).
