# M0134-0017 — `hash_index.sql`: proving a partial index's predicate from the query quals

Status: accepted · Date: 2026-08-20 · Milestone: M0134-0017

## The case

`hash_index.sql` is a `failed` regress row. Sized at HEAD it is **19 diff lines
in a single hunk, zero `^+ERROR`, zero row mismatches** — every `CREATE INDEX`,
`COPY`, `ANALYZE`, `SELECT`, `UPDATE` and `DELETE` in the file already matches
PG 18.3 byte for byte. The one divergence is a plan shape:

```
 EXPLAIN (COSTS OFF) SELECT * FROM hash_i4_heap WHERE seqno = 9999;
-Index Scan using hash_i4_partial_index on hash_i4_heap    <- PG 18.3
-  Index Cond: (seqno = 9999)
+Seq Scan on hash_i4_heap                                  <- goopg
+  Filter: (seqno = 9999)
```

The index is `CREATE INDEX hash_i4_partial_index ON hash_i4_heap USING hash
(seqno) WHERE seqno = 9999` (`postgres/src/test/regress/sql/hash_index.sql:56`);
expected output at `postgres/src/test/regress/expected/hash_index.out:134-137`.
Note this rides goopg's btree substrate — a `USING hash` index is stored with
`Method=="btree"` and `DeclaredHash=true` — so the *btree* selector is the code
actually exercised.

## Root cause: a deliberate blanket decline

goopg declines **every** partial index for scan selection:
`findBTreeIndexForColumn` (`internal/optimizer/planner.go:9510`) does
`if idx.HasPredicate { continue }`.

That guard is not an oversight; it is the 2026-08-07 fix recorded in the
deferral ledger (row `M0127 S7 gate / AI-20260806-232940-001,-002`). Before it,
goopg used partial indexes **without** proving the predicate, so `onek2 WHERE
unique1 = 50` took `Index Only Scan using onek2_u1_prtl` — predicate `unique1 <
20 OR unique1 > 980` — and returned 0 rows where 1 exists. Declining everything
traded a wrong-answer bug for a missed-optimization bug, and the ledger row
explicitly deferred the real fix: **the prover itself.**

PG's shape is `check_index_predicates`
(`postgres/src/backend/optimizer/path/indxpath.c:3943`), which sets
`index->predOK` from `predicate_implied_by(index->indpred, clauselist, false)`
(`:4048`); the prover lives in
`postgres/src/backend/optimizer/util/predtest.c`, with the leaf clause-vs-clause
step in `operator_predicate_proof`.

## Why this slice is contained (and why the first sizing pass said otherwise)

A full `predicate_implied_by` port — CNF/DNF normalization, the
btree-strategy-number operator implication tables, `refute` mode — is genuinely
REFACTOR-tier, which is what the ledger deferred and what the first sizing pass
re-affirmed. This design does **not** port that. It ports the single leaf case:

> both the index predicate and a query restriction clause are `Var <op> Const`,
> over the same column, with the same operator and an equal constant datum.

Everything else returns `false`, i.e. **exactly today's behavior**. The change is
therefore sound by construction: it can only convert declines into acceptances
for a case where the index predicate is literally the query's own qual, so the
index provably contains every row the scan may return. The 2026-08-07
wrong-answer class (`unique1 = 50` vs predicate `unique1 < 20 OR unique1 > 980`)
is an `OR` of range clauses, refused by shape, and gets a named non-regression
test.

The second reason this is worth doing now: the flip is **not cost-gated**. For a
simple single-relation SELECT, `planSelect` (`planner.go:1119-1122`) calls
`planIndexScanFromWhere` and adopts its node directly, with no `addPath` /
`setCheapest` / cost comparison — a legacy rewrite path, not the Path search. And
`hash_i4_heap` has no other index on `seqno` (`hash_i4_index` is on `random`,
`hash_index.sql:47`). So proving the predicate is both necessary and sufficient
to make the case green.

## The change

1. New helper in the optimizer, the narrow `operator_predicate_proof`
   specialization: given the index's resolved predicate expression and the
   query's restriction clause, return true only for the `Var op Const` ==
   `Var op Const` match. It reuses goopg's existing constant primitives —
   `toLiteralValue` and `litCompare` (`internal/optimizer/foldconst.go:419-431`,
   `:563`), already the plan-time `Const op Const` comparator — rather than
   inventing a second datum-equality notion. Both clause orderings
   (`col op const` and `const op col`) are handled; every other shape refuses.
2. `findBTreeIndexForColumn` takes the query clause and, for
   `idx.HasPredicate`, accepts the index only when the helper proves the
   predicate (resolving `idx.Predicate` through the existing
   `ResolveIndexPredicate`, `planner.go:75`, as `pathbitmap.go:120` already
   does). Its other callers — `rewriteMinMaxAggregates`, `tryRangeIndexScan`,
   `absorbConjunctsIntoSubtree` — have no literal clause in hand and pass
   nothing, keeping their current decline.

## Scope boundary (ledgered, not silently dropped)

The other `HasPredicate` decline sites are **not** touched: they lack the
query's restriction clause in scope, or the case does not apply.
`findCompositePrefixIndexForColumn` (`planner.go:9604`) already has the clause
map (`collectEqualityConjuncts`) and is the cheapest follow-up;
`addOneOrderedIndexPath` (`pathindexordered.go:128`) and
`groupagg_indexorder.go:132` are ordering-only builders that need the rel's
restriction list plumbed in; `pickIndexCoveringAllLeadingColumns`
(`nl_index_join.go:991`) binds per-row outer values, not plan-time constants,
which is a different and not-obviously-sound problem. PG proves the predicate
once per index in `check_index_predicates` and every path builder then reads
`predOK` — adopting that single-point-of-truth shape is the real end state.

Scan-side correctness needs nothing extra: goopg filters rows into a partial
index at build time (`internal/executor/operators_ddl.go:13499,13672`, mirrored
at REINDEX `operators_reindex.go:241-242,415-416`), matching PG, so a
proven-implied index needs no residual `Filter`. One asymmetry noted for the
ledger: no `HasPredicate` check was found in the per-row INSERT/UPDATE index
maintenance path, which would make a partial index a superset of its defined
contents — harmless for this equality case (the `Index Cond` still filters) but
not PG-faithful.
