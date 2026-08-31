# root-0036 — `SELECT DISTINCT` silently discarded its own `ORDER BY` direction

Status: accepted
Date: 2026-07-28
Milestone: M-NIGHTLY (nightly regress triage, item (e) — `regress/select_distinct`)
Area: planner (`internal/planner/planner.go`)

## 1. Symptom

`TestPort_RegressSuite/select_distinct` reports `deferred: output mismatch;
normalization rules need extension`. The whole divergence is one query and one
block of 20 rows, exactly reversed:

```sql
SELECT DISTINCT p.age FROM person* p ORDER BY age using >;
```

| PG 18.3 | goopg (HEAD) |
|---|---|
| `98 88 78 68 60 58 50 48 40 38 34 30 28 25 24 23 20 19 18 8` | `8 18 19 20 23 24 25 28 30 34 38 40 48 50 58 60 68 78 88 98` |

The `USING >` in the failing query is a red herring, and so is the `person*`
inheritance scan. Reduced on a throwaway cluster, the minimal reproduction has
neither:

```sql
CREATE TABLE person (name text, age int);
INSERT INTO person VALUES ('a',8),('b',18),('c',98),('d',18),('e',50);

SELECT DISTINCT   age FROM person   ORDER BY age DESC;   -- 98 50 18 8   ✅
SELECT DISTINCT p.age FROM person p ORDER BY age DESC;   --  8 18 50 98  ❌
```

A plain `SELECT DISTINCT <qualified column> … ORDER BY … DESC` answers in
**ascending** order. This is a wrong-answer bug in one of SQL's most ordinary
shapes, and nothing in the query is exotic — the only thing that has to be true
is that the select-list entry is written with a qualifier.

## 2. Why the qualifier is what matters

goopg does not plan `SELECT DISTINCT` the way PostgreSQL does. Upstream builds
the distinct clause **out of the sort clause** (`transformDistinctClause`,
`postgres/src/backend/parser/parse_clause.c`) and then puts a `Unique` node over
a path already sorted by those same pathkeys, so the dedup and the ordering are
the same operation and there is only ever one sort.

goopg instead has `distinctOp` re-sort its input **ascending** so duplicates
become adjacent, and then re-applies the query's `ORDER BY` on top of the
`Distinct` node. That second Sort is the only thing carrying the user's
direction, and it was added conditionally (planner.go, M0097-0046):

```go
for _, sb := range s.OrderBy {
    expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
    e, err := resolveExpr(expr, outerCtx)
    if err != nil {
        outerKeys = nil   // "Key not resolvable in Distinct output — skip outer sort."
        break
    }
    …
}
```

Two facts collide there:

1. `resolveOrderBySubstitution` (planner.go) rewrites a bare `ORDER BY` name
   into the matching target's **own expression**. For `SELECT DISTINCT p.age …
   ORDER BY age`, the target has no explicit `AS`, so the derived-output-name
   arm fires and `age` becomes the target expression `p.age` — a *qualified*
   `parser.ColumnRef`.
2. `outerCtx` is `newResolveContext(nil, distinctOut)` — a **schema-only**
   context with no range bindings — and `SchemaColumn` (`internal/planner/plan.go`)
   carries only `Name`, `Type` and `SourceTableIdx`. There is no table name in a
   plan node's output schema at all.

So the qualified reference cannot resolve, `err != nil`, the outer Sort is
dropped, and `distinctOp`'s internal ascending sort silently becomes the answer.
The failure is total and silent: no error, no warning, just the wrong order.

The same mechanism swallows every *computed* target for the identical reason
(`SELECT DISTINCT p.age+1 … ORDER BY 1` substitutes to `p.age+1`, which also
cannot resolve against a bindings-free context), and a **star** target for a
different one: `resolveOrderBySubstitution` deliberately refuses to substitute a
`StarExpr`, leaving a bare `IntegerConst` that the outer arm never had a
positional path for.

Measured surface before the fix (throwaway cluster on :5533, all `want` values
captured from the PG 18.3 reference on :65438):

| shape | pre-fix |
|---|---|
| `SELECT DISTINCT age FROM person ORDER BY age DESC` | ✅ correct |
| `SELECT DISTINCT p.age FROM person p ORDER BY age DESC` | ❌ ascending |
| `SELECT DISTINCT p.age FROM person p ORDER BY 1 DESC` | ❌ ascending |
| `SELECT DISTINCT p.age AS a FROM person p ORDER BY a DESC` | ❌ ascending |
| `SELECT DISTINCT p.age+1 FROM person p ORDER BY 1 DESC` | ❌ ascending |
| `SELECT DISTINCT * FROM person ORDER BY 2 DESC` | ❌ ascending |
| `SELECT DISTINCT p.age, p.name FROM person p ORDER BY age DESC, name ASC` | ❌ ascending |
| `SELECT DISTINCT p.age FROM person p ORDER BY age using >` | ❌ ascending |

Only the first shape — an unqualified, uncomputed, non-star target — worked,
which is why this survived so long: it is the shape most hand-written test SQL
uses, and the shape upstream's regress suite happens to avoid in the one case
that catches it.

## 3. Fix

Keep the existing design (internal ascending dedup + outer re-sort) and make the
outer key resolution actually succeed, by resolving each key the way the *target
list* was resolved and then mapping the result back to its select-list position.
Three arms, tried in order:

1. **Positional.** `ORDER BY <n>` is a 1-based reference into the DISTINCT
   output, resolved against `distinctOut` **before** any substitution — so a
   star target list works too. Mirrors the identical `IntegerConst` arm already
   present on the normal (non-DISTINCT) ORDER BY path.
2. **Schema-only** `resolveExpr` against `outerCtx` — the pre-existing arm,
   unchanged, which still handles every bare-name key.
3. **Select-list position.** New `distinctSortKeyOutputIndex` resolves the
   substituted expression against whichever surface built the targets
   (`resolveExprAfterWindow` / `resolveExprAfterAggregate` / `resolveExpr` on the
   pre-projection `ctx`), then structurally matches the resulting planner `Expr`
   against `proj.Targets` with `exprEqual`. `proj.Targets` are indexed in
   output-schema order, so the match index *is* the Distinct output column.

Arm 3 is sound precisely because of the PG rule quoted above: upstream's
`transformDistinctClause` rejects any `SELECT DISTINCT` whose `ORDER BY` key is
absent from the select list with SQLSTATE 42P10, so a key that legally reaches
this code is guaranteed to occupy a select-list position. A `-1` therefore means
"did not resolve at all", not "legal but unmatched", and the pre-existing
`outerKeys = nil` bail-out is kept for that case.

This mirrors what the neighbouring `DISTINCT ON` arm has always done — resolve
against the inner context, then `findExprInSchema(re, outSchema, proj)` to get an
output index — so the two DISTINCT forms now agree on how a sort key is located
instead of one using bindings and the other not.

`ORDER BY … USING <op>` needs no separate handling: the parser already folds the
operator into the same `SortBy.Desc` flag (`sortUsingIsDesc`, select.go), so it
rides the identical planner path. That folding is itself a divergence from PG —
see §5.

## 4. Verification

- **New, non-vacuous** `TestDistinctHonoursOrderByDirection`
  (`internal/executor/distinct_orderby_direction_test.go`): 10 subtests over all
  four ways a sort key reaches the Distinct output (bare name, qualified name,
  output alias, 1-based position) plus computed and star targets, both
  directions, and both `USING >` and `USING <`. Every `want` is PostgreSQL 18.3's
  answer, captured from the reference cluster on :65438 — not goopg's. Negative
  control: with the planner hunk stashed, **7 of the 10 subtests fail** with the
  exact reversed rows. It lives in `internal/executor` because that package's
  in-process `newDDLFixture`/`runDDL`/`runQueryWithErr` harness needs no server
  and therefore runs inside the mandatory units gate.
- `TestPort_RegressSuite/select_distinct` flips **SKIP (deferred: output
  mismatch) → PASS**. Note this case *does* reproduce in isolation, unlike the
  `errors`/`portals_p2`/`select` cases triaged alongside it, which is what made
  a 1.6 s repro loop possible instead of a 670 s suite prefix.
- `go test ./internal/planner/ ./internal/executor/` PASS;
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
  `go test ./internal/server/` PASS (run explicitly — the units gate does not
  cover that package, the root-0035 lesson).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).
- TPC-DS: instead of the ~1 h SF0.5 sweep, the change was scoped by inspection to
  the queries it can reach. Only 5 of the 100 TPC-DS queries contain `SELECT
  DISTINCT`, and only **Q41** has one at a level that also carries `ORDER BY`
  (the other four are scalar subqueries with no `ORDER BY`, so the
  `len(s.OrderBy) > 0` guard makes them provably untouched). Q41, Q6 and Q87 were
  run against the SF0.5 cluster and match both the git-tracked oracle and the
  recorded 2026-07-27 baseline exactly (Q41 1 row/8 s, Q6 37 rows/27 s, Q87 1
  row/19 s); Q54 timed out and killed the capped server, matching its recorded
  `TIMEOUT` baseline. A Sort node cannot change a row count, and the SF0.5 gate
  compares row counts only, so the full sweep is structurally blind to this bug
  class in either direction — see §5.

## 5. Deferred (ledger rows, 2026-07-28)

- **`SELECT DISTINCT` does not adopt the query's sort operators.** PG builds the
  distinct clause from the sort clause (`transformDistinctClause`) and dedups
  with *those* operators; goopg dedups with a fixed ascending sort inside
  `distinctOp` and repairs the order afterwards with a second Sort. The rows
  agree for every ordinary operator, but the two sorts are redundant work, and a
  `USING` operator that is not a btree `<`/`>` strategy of the column's default
  opclass would dedup on the wrong equality.
- **`ORDER BY … USING <op>` infers direction from the operator's *name*.**
  `sortUsingIsDesc` (`internal/parser/select.go`) special-cases `>`, `>=`, `~>~`,
  `~>=~` and any name suffixed `gt`/`greater`. PG instead resolves the operator
  to a btree opclass strategy (`get_ordering_op_properties`,
  `postgres/src/backend/utils/cache/lsyscache.c`), so a user-defined descending
  operator whose name matches none of those patterns silently sorts ascending.
- **The SF0.5 regression gate cannot see ordering bugs.** It compares row counts
  only, so this entire class — wrong order, right rows — is invisible to it.
  Same blind spot `scripts/tpch-spotcheck.sh` has, and the reason this bug had to
  be found through the regress suite.
