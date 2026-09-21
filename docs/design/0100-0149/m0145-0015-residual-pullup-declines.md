# Naming the residual pull-up declines (M0145-0015)

Status: **CLOSED measured-no-gap, 2026-09-21.** The census was extended as the
task's step 1 asks; the result is that every remaining pull-up decline is one
PostgreSQL makes too, and both fallback mechanisms the task told me to look for
already exist in goopg. Nothing was built, because the pullable subset is zero.

Task: `.ralph/fix_plan.md` M0145-0015. Kind: impl. Parent: M0145-0003.

## Step 1 — the census now reports the clause POSITION

`notePullupDecline` used to report only the sublink's Go type, so
`SubqueryExpr`, `ExistsExpr` and `InExpr` were the whole answer. That cannot
distinguish a miss from a correct decline, because in PostgreSQL the **position
decides reachability, not the kind**: `pull_up_sublinks_qual_recurse` recurses
through AND and through a NOT wrapper
(`postgres/src/backend/optimizer/prep/prepjointree.c:789-845`) and then

```c
	/* Stop if not an AND */
	return node;
```

at `:877`. OR arguments are never recursed. So an `ExistsExpr` under an OR is a
decline upstream makes as well, while an `ExistsExpr` at conjunct top level
would be a real gap.

`sublinkConjunctSite` now reports `<kind>@<position>` with position one of
`top`, `not`, `or`, `scalar`. The walk is path-sensitive on purpose — the
OUTERMOST non-AND wrapper is what stops upstream's recursion, so an OR seen
above the sublink outranks a NOT seen below it — and an AND below the conjunct
root stays `top`, because `splitAnd` would have separated it had the caller
split deeper.

## Step 2 — the measurement

TPC-DS SF0.25, knob arm, `GOOPG_NLI_CENSUS=1`, all 99 queries:

```
PULLUPCENSUS decline=(pulled)                          54
PULLUPCENSUS decline=any-body-leaf-(*optimizer.CTEScan) 30   <- M0145-0013's class
PULLUPCENSUS decline=SubqueryExpr@scalar               29
PULLUPCENSUS decline=ExistsExpr@or                      4
PULLUPCENSUS decline=InExpr@or                          2
```

**No conjunct is at `@top` or `@not`.** Taking the three residual classes in
turn:

- `SubqueryExpr@scalar` (29) — an EXPR sublink inside a scalar expression.
  `pull_up_sublinks_qual_recurse` converts `ANY_SUBLINK` and `EXISTS_SUBLINK`
  only; an EXPR sublink is never a jointree citizen in PG either.
- `ExistsExpr@or` (4) and `InExpr@or` (2) — under an OR argument, which the
  `/* Stop if not an AND */` arm above declines to recurse into.

So the subset PG reaches and goopg misses is **empty**, which is the task's own
"If the pullable subset is zero, record that and close as measured-no-gap" arm.

## Step 3 — the fallback the task told me to check for already exists, twice

The task's caution was that upstream's `convert_EXISTS_to_ANY`
(`plan/subselect.c:1717`) does not make a buried EXISTS a jointree citizen — it
rewrites it as a *hashable ANY subplan* — and that goopg should be checked for
an equivalent before anything is built. Both halves of that equivalent are
present and live:

- `internal/optimizer/exists_to_any.go` — goopg's `convert_EXISTS_to_ANY`,
  default ON (`GOOPG_EXISTS_TO_ANY=off` is the escape).
- `internal/executor/subplan_hash.go` — the hashed probe for an uncorrelated
  ANY/NOT-IN subplan, default ON, reached from `evalInExpr`
  (`internal/executor/expr.go:10373`). It is PG's `buildSubPlanHash` /
  `ExecHashSubPlan` shape (`nodeSubplan.c:477` / `:101`), including the
  NULL-bit degeneration of the partial-match table.

There is therefore no missing PG-equivalent piece to file.

## What this task deliberately did not do

No pull-up gate was widened. Widening one to move an `@or` conjunct would make
goopg convert a sublink PostgreSQL leaves as a SubPlan — a divergence dressed
as progress, and exactly the kind the corpus value gates cannot see, since the
rows would still be right.

The census extension is the whole production change, and it is what lets the
next loop tell `@top` (a gap) from `@or` (parity) without re-deriving it.
