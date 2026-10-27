# M0147-0053 — hold an outer join's preserved-side ON conjunct at the join

Status: landed 2026-10-27.

Parent: M0147-0052, the recon of joins that bypass the PG-shaped search
(`analysis/m0147/m0147-0052/`).

## Problem

`outerOnQualsOK` (`internal/optimizer/joinsearchseam.go`) declined any LEFT or RIGHT link whose ON clause carried a
conjunct that references only the join's PRESERVED side:

```sql
SELECT … FROM o LEFT JOIN i ON i.k = o.k AND o.id > 0
```

The seam saw no safe destination for it. The conjunct must not become a scan restriction on `o`, which would drop rows
that should be null-extended, and it must not sit in the Filter above the tree, which would drop the null-extended
rows. So the whole statement fell out of the search: 45 decline events in one regress run. Such statements were planned
by the syntactic path, where the legacy NLI post-pass was the only thing giving them an index-probe nested loop.

## What PG does

`distribute_qual_to_rels` (`postgres/src/backend/optimizer/plan/initsplan.c`) does not push an outer join's ON qual
down (`is_pushed_down` is false). It gives the qual the outer join's scope as its `required_relids`. The qual
therefore lands in that join's restrictlist and nowhere lower. When the join is a nested loop it prints as
`Join Filter:`. Its selectivity is that of a one-relation clause (`clause_selectivity`, `NumRelids == 1`).

## Change

- `outerOnQualsOK` admits a preserved-side-only conjunct. A constant conjunct, which has no relids, still declines.
  So does a scope containing a zero-width leaf, since its scope could not name every leaf.
- The seam routes such conjuncts into `ojHeld []ojHeldQual` instead of `conjuncts`. `conjuncts` feeds
  `partitionConjunctsForJoinPlanningScoped`, which would have pushed them into the preserved scan. Each held qual
  carries the link's scope as one representative column per leaf (`linkScopeCols`).
- `joinlistProblem.ojHeldQuals` carries them into `searchOneProblem`. There `restrictInfoList.appendOJHeld`
  re-expresses the scope in the problem's item coordinates. It adds a restrict info whose `relids` is the scope, and
  flags it `ojHeld`, only in the problem whose items separate the join's two sides; exactly one problem qualifies.
  `clausesFor` then applies it at the outer join and nowhere lower.
- `joinClauseSelectivityExtUncached` prices an `ojHeld` clause as a restriction (`orConjunctSelectivity`), as PG does.

## Effect

- **Witness.** The witness statement now goes through the search and plans exactly as PG 18.3 does, checked on a
  scratch PG with the same data and stats: `Nested Loop Left Join`, `Join Filter: (o.id > 0)`, `Seq Scan` on `o`,
  and a parameterised index probe with `Index Cond: (k = o.k)`. Costs are 722 for goopg against 734 for PG.
- **Corpora.** No TPC-H or TPC-DS plan moved (fire-set `fires=none`, census 22/22). The regress suite's 81
  mismatching cases are unchanged; their outputs are identical before and after (87 091 diff lines against the
  expected output both times).

## Tests

- `TestOuterOnQualsOKAdmitsPreservedSideConjunct` checks that a preserved-side conjunct is admitted, and that a
  constant and a zero-width scope still decline.
- `TestAppendOJHeldOwnsOnlyTheSplittingProblem` checks the scope, the flag, and single-problem ownership.
- `TestSeamRightLinkOnQualDestinations` covered the preserved-side half of the old decline. It now asserts the
  correct placement: the statement is searched, there is no residual and no leaf filter on `c`, and `c1 > 5` is a
  qual of the searched outer join.
- `internal/executor` `TestOuterJoinPreservedSideOnQual` covers LEFT and the mirrored RIGHT form. Every preserved row
  is kept (100), 39 match, and the conjunct is the outer join's `Join Filter`.
- `TestParallelNLIJointypeIdentity`, the fused-NLI parallel pin, is retired. Its last case was this witness, which
  now builds the decomposed lateral nested loop. That family is pinned by
  `parallel_lateral_left_probe_identity_test.go`, and the shared fixture stays.

## Not done (ledgered)

A constant outer-join ON conjunct (`… LEFT JOIN p ON p.k = t.k AND 1 = 0`) still declines. PG treats it as a
pseudoconstant join qual of that outer join.

## Gates

See the M0147-0053 entry in `.ralph/fix_plan.md`.
