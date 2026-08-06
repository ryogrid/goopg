# M0125-0051 — a data-modifying WITH is legal only on the statement being run

Status: implemented 2026-08-06
Area: `internal/analyzer`
Predecessor: [M0125-0050](0125-0050-cte-declaration-identity.md) (filed this item
from its `MaterializedCTEs` sibling audit)

## The defect

goopg **executed** a data-modifying CTE written at any nesting depth. Captured
at `2af216ba`:

```sql
CREATE TABLE dm(a int);
SELECT v FROM (WITH x AS (INSERT INTO dm VALUES (1) RETURNING a) SELECT a AS v FROM x) s;
--  v
-- ---
--  1        ← goopg: the row was inserted AND returned
-- ERROR:  0A000: WITH clause containing a data-modifying statement
--         must be at the top level      ← PG 18.3
```

PostgreSQL rejects it in `analyzeCTE`
(`postgres/src/backend/parser/parse_cte.c:330-337`):

```c
	/*
	 * We disallow data-modifying WITH except at the top level of a query,
	 * because it's not clear when such a modification should be executed.
	 */
	if (query->commandType != CMD_SELECT &&
		pstate->parentParseState != NULL)
		ereport(ERROR,
				(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
				 errmsg("WITH clause containing a data-modifying statement must be at the top level"),
				 parser_errposition(pstate, cte->location)));
```

Note the SQLSTATE: **`0A000` (feature_not_supported)**, verified against a live
PG 18.3 with `\set VERBOSITY verbose`. The ledger row that filed this item said
42P19; that was wrong, and 42P19 belongs to the neighbouring *recursive* rule
("recursive query must not contain data-modifying statements"), which goopg
already raises in `analyzeRecursiveCTE`.

Why it matters beyond the error text: the accepted form has no defined
execution time. goopg ran the INSERT while opening a derived table, i.e. once
per *scan* of that subquery, with the outer statement's CTE write-fence and
snapshot restore (`cteDMLPrefixOp.Open`) applied only at the level that declared
it. This is also what made the second `MaterializedCTEs` finding reachable: two
same-named DML CTEs can only exist in one statement if one of them is nested,
so enforcing the rule closes that aliasing by construction (see "What this
leaves" below).

## The fix: one flag, threaded from the statement entry point

PG's test is `pstate->parentParseState != NULL` — a property of the *parse
level*, not of the query text. goopg's analyzer has no parse-state chain, but it
has the same shape: `analyzeSelect` is the only entry that runs on a statement,
everything else recurses through `analyzeSelectWithParent`. So:

- `analyzeWith` gains a `stmtRoot bool` and raises `0A000` on a `cte.DMLBody`
  when it is false.
- `analyzeSelectStmt(s, cat, parent, stmtRoot)` is the new internal form.
  `analyzeSelectWithParent` becomes the `stmtRoot=false` wrapper — every one of
  its 15 call sites analyzes a nested query (derived table, sublink, CTE body,
  set-op arm), so the wrapper's constant is the correct answer for all of them.
- `analyzeSelect` passes `outerScope == nil`. The planner sets `outerScope`
  before re-entering the analyzer for a correlated subquery
  (`analyzer.SetOuterScope`), so a re-entrant analysis is never a statement
  root — the same reason PG's re-entry through `parse_sub_analyze` is never one.
- `analyzeInsert` / `analyzeUpdate` / `analyzeDelete` pass `outerScope == nil`
  for the same reason: `WITH x AS (INSERT …) INSERT INTO …` is legal.

Because `planner.Plan` calls `analyzer.Analyze` on every statement
(`internal/planner/planner.go:55`), one check covers the simple protocol,
PREPARE/EXECUTE, and every other planning entry — verified end to end on a live
server for both protocols.

### The two edges that decide the flag's placement

Both were captured from PG 18.3 before being encoded:

1. **A parenthesised whole statement is still top level.**
   `(WITH x AS (INSERT … RETURNING a) SELECT a FROM x)` is accepted by PG:
   `select_with_parens` adds no parse-state level. goopg reaches it through
   `analyzeSelectStmt`'s `SetOpOperand`-indirection branch, so `stmtRoot` is
   carried through that hop rather than reset.
2. **A parenthesised set-op ARM is not.**
   `(WITH x AS (INSERT … RETURNING a) SELECT a FROM x) UNION ALL SELECT 99` is
   rejected by PG. goopg reaches the left arm through the *same* indirection
   branch, one recursion later — so the set-op branch passes
   `stmtRoot && s.SetOpOperand == nil`, which is the only place the two edges
   are distinguishable. A WITH written *before* the chain
   (`WITH x AS (INSERT …) SELECT … UNION ALL SELECT …`) belongs to the statement
   and keeps `stmtRoot`; PG accepts it.

## Verification

`internal/analyzer/dml_cte_toplevel_test.go` pins six rejected shapes (derived
table, CTE body, set-op right arm, parenthesised set-op left arm, nested
UPDATE, nested DELETE) with code *and* message, and eight accepted ones — five
legal top-level forms plus two plain nested CTEs and a parenthesised statement,
so the check cannot start rejecting ordinary nesting.

End-to-end against PG 18.3 (TPC-H reference cluster) with the same script on
both engines: the four executable nested shapes now error with the identical
message and SQLSTATE, the top-level forms still run, and the resulting table
contents match PG row for row.

## What this leaves (ledger rows, same date)

- **`ctx.MaterializedCTEs` is still name-keyed** where `CTERowCache` moved to
  `DeclKey` in -0050. With the top-level rule enforced, a *second* DML CTE
  declaration of the same name now requires a nested WITH and is rejected before
  execution, so the aliasing is unreachable rather than fixed — the two paths
  disagree, which is exactly the sibling-divergence pattern that has cost this
  project before.
- **A scalar-sublink `WITH` does not parse at all** (`SELECT (WITH x AS (…)
  SELECT …)` → 42601, and `EXISTS (WITH …)` likewise). PG accepts the plain form
  and raises `0A000` for the data-modifying one; goopg raises a syntax error for
  both, so the rule is *reached* but the error is wrong there.
- **`WITH x AS (INSERT …) DELETE FROM t WHERE …`**: goopg's outer DML sees the
  CTE's rows. PG's outer statement runs on the statement snapshot, so the DELETE
  matches nothing (0 rows, row still present); goopg deletes it. The CTE
  write-fence is applied to an outer SELECT but not to an outer DML.
- **`WITH …` is not a statement PL/pgSQL accepts** (`unsupported PL/pgSQL
  statement`), for DML and plain CTEs alike.
- **`WITH … (SELECT …)`** — a parenthesised body after the WITH list — is a
  goopg syntax error; PG accepts it.
