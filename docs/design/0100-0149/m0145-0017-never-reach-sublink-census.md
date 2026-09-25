# The never-reached sublink population (M0145-0017)

Status: **CLOSED measured-no-gap, 2026-09-21.** Step 1 (the census) is done and
it makes step 2 unnecessary: the one clause position where PostgreSQL's reach
exceeds goopg's holds **zero** sublinks on the corpus, on both arms.

Task: `.ralph/fix_plan.md` M0145-0017. Kind: impl. Parent: M0145-0003.

## Where PG's reach actually ends

The task frames the question as "PG's reach is wider than WHERE". It is — but
by exactly one clause, and the oracle bounds it precisely.
`pull_up_sublinks` (`postgres/src/backend/optimizer/prep/prepjointree.c:468`)
begins and ends at the jointree:

```c
	/* Begin recursion through the jointree */
	jtnode = pull_up_sublinks_jointree_recurse(root,
											   (Node *) root->parse->jointree,
											   &relids);
```

and it is called from exactly one site (`plan/planner.c:737`). A jointree
carries quals in two places: the `FromExpr`'s quals — the WHERE — and each
`JoinExpr`'s quals — an explicit join's ON clause. **Nothing else is a
candidate in PG either.** A sublink in a target list, in HAVING, in GROUP BY or
ORDER BY, or nested inside a scalar expression is never a pull-up candidate
upstream, so goopg keeping it a SubPlan is parity, not a miss.

goopg's `pullUpSublinksIntoJointree` walks the top-level WHERE only. The
difference between the two reaches is therefore **exactly the ON clauses**, and
that is the only population worth measuring.

## The instrument

`noteOnQualSublinks` reports each sublink-bearing conjunct of an explicit
join's ON clause, at the point the qual is resolved (`planJoinPredicate`), as

```
ONSUBLINK jointype=<type> site=<kind>@<position>
```

Two deliberate choices:

- **The join TYPE is recorded, not merely "in an ON clause."** PG applies a
  legality boundary in the `JoinExpr` arm of
  `pull_up_sublinks_jointree_recurse`: INNER passes both sides' rels as
  available, LEFT passes only the RHS, RIGHT only the LHS, and FULL passes
  nothing. A sublink pulled out of a null-preserved side is a wrong-answer
  class, not a missed optimisation, so a census that could not distinguish
  `jointype=inner` from `jointype=full` would answer the wrong question.
- **The site string is M0145-0015's `<kind>@<position>`, reused.** An EXISTS
  under an OR inside an ON clause is out of reach for exactly the reason it is
  out of reach inside a WHERE — upstream stops at non-AND clauses — so the two
  censuses should speak one vocabulary.

## The measurement

TPC-DS SF0.25, all 99 queries, both arms:

```
                                  knob arm   default arm
ONSUBLINK (any jointype, any site)       0             0
SUBLINKCENSUS route=jointree-pullup     46             -
SUBLINKCENSUS route=pinned-spine       567           398
```

A textual cross-check over the query corpus agrees: no file matches a join ON
clause containing a subquery.

## Adjudication

The never-reach population decomposes into **"correctly out of reach"
entirely, and "newly pulled" empty**:

- target list, HAVING, GROUP BY, ORDER BY, nested scalar contexts — never
  candidates in PG either, per the entry point above;
- ON clauses — the one position where PG reaches further, and the corpus holds
  none;
- WHERE-position residuals — already measured by M0145-0015 as `@or` and
  `@scalar` only, every one a decline upstream makes too.

So step 2 (extending the walk to ON quals) would be built against zero
witnesses. That is the task's own "If the census finds no PG-pullable class
outside WHERE, record that and close as measured-no-gap" arm.

The instrument stays in the tree rather than being reverted with the
investigation: it is one census line behind an existing env gate, and it fires
the moment a corpus acquires the shape. A measured zero that nothing watches
decays into an assumption.
