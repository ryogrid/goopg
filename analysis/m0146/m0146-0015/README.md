# M0146-0015 recon: the `subselect` nested EXISTS / NOT EXISTS hang

Recon of 2026-09-25. Query: `query.sql`, the upstream regress `subselect`
case at `expected/subselect.out:1146`. Tables were seeded as regress
`test_setup` + `create_index` do (`seed.sql`), on throwaway clusters.

## Bisect answer: the cutover introduced the hang

| build | plan (`plan-goopg-*.txt`) | runtime |
|---|---|---|
| pre-flip `ddb4eabd4^` (legacy default) | SubPlan 1: Index Scan on `tenk1_hundred`, `Index Cond: (hundred = b.hundred)`; SubPlan 2: Bitmap Index Scan, `Index Cond: (thousand = $0)` | 207 s, 0 rows (correct) |
| HEAD `f21c17cbc` (jointree pipeline) | SubPlan 1: Seq Scan on c, `Filter: (b.hundred = hundred AND NOT EXISTS …)`; SubPlan 2: full Index Only Scan, `Filter: ($1 = thousand)` | > 1 h (the reported hang) |
| PG 18.3 (`plan-pg183.txt`) | both sublinks pulled up: Nested Loop Semi Join over Hash Anti Join (a, d) | 4 ms |

The outer join produces about 100k (a, b) rows, and each runs SubPlan 1.
Before the flip, each call is roughly 100 index-probe rows, each with one
index probe for SubPlan 2: about 10^7 probes. At HEAD, each call scans
10,000 rows of c, and each of the ~100 matching rows runs SubPlan 2 as a
full scan of 10,000 index entries: about 10^11 row visits.

## Mechanism (HEAD)

A correlated outer reference (`OuterColumnRef`) is never accepted as an
index key by the jointree pipeline's index-path producers. Isolated with a
one-level scalar subquery, `select (select count(*) from tenk1 d where
a.thousand = d.thousand) from tenk1 a`, in both operand orders:

- **Pre-flip:** `Index Cond: (thousand = a.thousand)`.
- **PG 18.3:** `Index Cond: (thousand = a.thousand)`.
- **HEAD:** `Filter: (a.thousand = thousand)` over a full Index Only Scan,
  in either operand order.

`restrictionEqualityPrefix` (`internal/optimizer/pathindexrestrict.go`)
takes its key from `normalizeColumnConst`, and `restrictionKeyUsable`
repeats the check; both require `isConstExpr`, which admits only literals.
The range producer (`restrictionRangeOnColumn`) and the index-only producer
(`consumingIndexClauses`, `pathindexonly.go`) share that recogniser. PG's
`match_clause_to_indexcol` (`indxpath.c`) accepts any operand that does not
reference the index's own relation and contains no volatile function. An
outer-level Var is a `PARAM_EXEC` Param there, and so qualifies.

Bind parameters are unaffected: a generic plan of `unique1 = $1` still gets
`Index Cond: (unique1 = $1)` at HEAD.

## Pre-existing, separate gap

Neither goopg build pulls the two sublinks up into joins; PG does
(`pull_up_sublinks` → `convert_EXISTS_sublink_to_join`, recursing into the
pulled-up quals). That is why pre-flip still takes 207 s against PG's 4 ms.
It predates the cutover.

## Filed

- **M0146-0015a** (impl): index keys from outer references, the cutover
  regression.
- **M0146-0015b** (recon): nested EXISTS / NOT EXISTS pull-up, pre-existing.

## Side observation, not investigated

In `select * from tenk1 a where a.unique1 < 3 and exists (...)`, HEAD
seq-scans `a` where pre-flip used `Index Cond: (unique1 < 3)`. That may be a
cost election rather than a producer gap; it is recorded in M0146-0015a's
entry for its first step.
