# M0125-0020 — the set-op chain becomes a tree

**Status**: implemented 2026-07-29 (branch `tpcds-fix2`).
**Predecessors**: M0097-0042/-0044, M0125-0006, M0125-0016, M0125-0017.
**Retires**: `SelectStmt.ParenBranches`, `SelectStmt.InnerSegmentCount`,
`SelectStmt.InnerSortLimit`, `planner.parenBoundary`, and the paren-boundary
half of the `cutAt` machinery.

## 1. The defect

goopg stored a set-op chain as a **linked list**: `SelectStmt.SetOp.Right`,
with the chain's head doubling as its leftmost operand. One node therefore had
to be two things at once whenever the leftmost operand was parenthesised:

```
(A ORDER BY 1 LIMIT 2) UNION ALL (C) ORDER BY 1 DESC
 └────── operand A ──────┘                └ whole compound ┘
```

Both want the same node's `OrderBy` / `Limit` / `Offset` slot. There is exactly
one such slot, so one of the two clauses had to be dropped. Two PostgreSQL
behaviours were unreachable for that reason:

| shape | PG 18.3 | goopg at `8ce216dd` |
|---|---|---|
| `(A ORDER BY 1 LIMIT 2) UNION ALL (C) ORDER BY 1 DESC` | `9,2,1` | `1,2,9` (outer sort dropped) |
| `((A ORDER BY 1 LIMIT 2) UNION ALL C) LIMIT 1` | `1` | `1` — but only by accident; the inner `LIMIT 2` was overwritten |

The worktree probe at `8ce216dd` found the damage was wider than filed —
three further shapes returned **wrong rows**, not merely a wrong order:

| shape | PG 18.3 | goopg at `8ce216dd` |
|---|---|---|
| `((A ORDER BY 1 LIMIT 2) UNION ALL C) ORDER BY 1 DESC LIMIT 2` | `9,2` | `9,3` |
| `((A LIMIT 1) UNION ALL (G LIMIT 1)) ORDER BY 1 DESC` | `2,1` | `3` |
| `((A ORDER BY 1 LIMIT 2) UNION ALL C) OFFSET 1` | `2,9` | `2,3` |

`parseParenthesisedSelectStmt` also **absorbed a set-operator written after the
`')'` into the parenthesised query's own chain**, so `(A) EXCEPT (B) EXCEPT (C)`
parsed as `A → B → C` with `B` flagged `Parenthesized` even though the user's
parentheses around `B` had closed before the second `EXCEPT`.

Three annotations existed only to describe that overlap:

- `ParenBranches` (M0125-0006) — how many branches were really inside the parens;
- `InnerSegmentCount` (M0097-0044) — how many segments an inner sort/limit covers;
- `InnerSortLimit` (M0125-0017) — because `InnerSegmentCount`'s zero value doubles
  as its "unset" sentinel and so cannot express a *single* parenthesised branch.

Each was correct for the case that motivated it and none could express the
combination. The fix_plan item said a fourth annotation should be read as the
signal to convert; this is that conversion.

## 2. What PostgreSQL does

`select_with_parens` is a **leaf operand** of `select_clause` in
`postgres/src/backend/parser/gram.y`:

```
select_clause: simple_select | select_with_parens
select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'
select_no_parens: select_clause opt_sort_clause … opt_select_limit
```

So a set-operator, `ORDER BY` or `LIMIT` written after the `')'` **cannot**
attach into the parenthesised query — it attaches to a node above it, and
`transformSetOperationStmt` (`postgres/src/backend/parser/analyze.c`) always
receives an already-nested tree. PG needs none of the three annotations because
the structure carries the information.

## 3. The change

### 3.1 A grouping node (`internal/parser/`)

`SelectStmt` gains one field:

```go
// SetOpOperand makes this SelectStmt a GROUPING node: it stands for the
// parenthesised set-op operand `( SetOpOperand )` and has no Targets /
// From / Where of its own.
SetOpOperand *SelectStmt
```

`parseParenthesisedSelectStmt` now consumes `'(' … ')'` exactly as before and
then **looks at the next token**. If it is a set-operator or `ORDER` / `LIMIT` /
`OFFSET` (`trailingClauseFollowsParens`), the function builds a fresh grouping
node and hangs the trailing clauses on **that**; the parenthesised query keeps
its own chain and its own sort/limit untouched. If nothing follows, the
parenthesised query is returned unchanged with `Parenthesized = true`.

Token *consumption* is byte-for-byte identical to the old function — only where
the resulting clauses are **stored** changed. That is what let the whole
existing M0125-0006/-0016/-0017 matrix stay green unmodified.

`Parenthesized` now has a sharpened meaning: *everything* this node holds was
inside one pair of parentheses, i.e. nothing followed the `')'`. The parser's
lift-from-right-branch rule (`!right.Parenthesized`, M0097-0042) therefore works
unchanged on a grouping node — its clauses came from **outside** the parens, so
they are liftable, which is exactly right for `(A) UNION (B) UNION C ORDER BY 1`.

### 3.2 The planner (`internal/planner/planner.go`)

- `parenBoundary` is **deleted**. A parenthesised operand can no longer share a
  chain with the operators around it, so there is no boundary to compute.
- The flatten loop's `Parenthesized` branch collapses to one case: a
  parenthesised operand that is itself a compound (`A UNION (B EXCEPT C)`) ends
  the chain and is planned atomically. Everything else cuts at `rightStmt`.
- `headSortLimit` / `innerBoundary` / `sortLimitConsumed` are gone: `s`'s own
  sort/limit **always** belongs to the whole chain and is always applied by the
  single trailing `wrapSetOpSortLimit`. A clause written inside a parenthesised
  branch is not on `s` at all — it is one level down, below a grouping node.
- A new terminal branch plans a grouping node: plan `SetOpOperand`, then wrap it
  in the node's own (post-`')'`) sort/limit.
- `setOpBindsTighter` loses its paren-boundary caller and is now used by
  `foldSetOpRange` alone, where the INTERSECT-binds-tighter precedence climb
  (M0125-0016) handles `A UNION (B) INTERSECT (C)` uniformly — the case the
  deleted branch used to special-case.

### 3.3 Consumers that name a query's output columns

A grouping node has **no target list**, so every consumer that derives column
names from the chain head had to descend past it. PostgreSQL names a set
operation's columns after its **leftmost branch**, which is exactly what the new
`analyzer.setOpLeftmostBranch` walk reaches:

| site | why |
|---|---|
| `analyzer.analyzeSelectWithParent` | analyzes the operand instead of an empty SELECT |
| `analyzer.synthesizeSubqueryTable` | derived table `((…) UNION …) q` |
| `analyzer.registerAnalyzedCTE` | `WITH w AS ((…) UNION …)` |
| `analyzer.analyzeRecursiveCTE` | the recursive anchor is the leftmost branch |

Without these, the two shapes report `42703 column "x" does not exist` — which
is how the pre-existing `TestSetOpParenChainInSubqueryContexts` caught them.

Structural walkers gained a `SetOpOperand` recursion so a grouping node does not
hide its branch from them: `selectRefsViewName`, `walkSelectPKDeps`,
`collectSelectTableRefs` and the routine-dependency walker (all
`internal/executor/operators_ddl.go`), `validateNoAggregatesInRecursiveMember`
and `recRefWalker.walkSelect` (`internal/planner/with.go`), and
`catalog.renameColumnInSelect`. Two "is this a simple single-relation SELECT?"
gates reject a grouping node outright: `planner.viewAutoUpdatableChain` and
`pgnodes.rejectUnsupportedClauses`.

## 4. Acceptance

Accepted **by value**, against PostgreSQL 18.3 on port 65438.

- `internal/executor/setop_tree_test.go` — 13 new by-value subtests. **Proved to
  fail 5 subtests at `8ce216dd`** (in a throwaway worktree) with every control
  green; three of those five were wrong-row failures, not ordering failures.
- 27-statement `psql` matrix, goopg (throwaway cluster on :5533) vs PG 18.3 —
  **byte-identical**. Covers the two filed shapes, the M0125-0006 associativity
  matrix, the M0125-0016 precedence matrix, the M0125-0017 head-branch matrix,
  derived tables, CTEs, `IN` operands, scalar subqueries and an INSERT source.
- Existing matrices unchanged and green: `setop_paren_assoc_test.go`,
  `setop_precedence_test.go`, `setop_head_branch_sortlimit_test.go` (executor),
  `setop_head_sortlimit_test.go` (planner).
- The parser structure tests were rewritten to pin the tree instead of the
  retired counters (`internal/parser/setop_paren_assoc_test.go`).

Gates: `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2 rows, Q13=35 rows); TPC-DS SF0.5 **subset probe** over the ten set-op
bearing queries — PASS=6 (all checksum-verified), MISMATCH=0, ERROR=0, with
Q5/Q14/Q54 timing out exactly as in the `sweep-20260729-123114` baseline.
**Q87 and Q23 — the two TPC-DS queries whose parse shape this change actually
alters — returned checksums identical to that baseline** (`b363a9287bdd0920`,
`00f53003bda23764`). pgbench smoke via the commit hook.

## 5. Deferred

- `CREATE VIEW v AS (SELECT …) UNION …` is a **pre-existing** parser gap,
  unrelated to this change and identical at `8ce216dd`: the view body accepts
  only a leading `SELECT` / `VALUES` / `WITH` keyword, never a `'('`
  (`internal/parser/ddl.go:2536`). PG accepts it. Ledger row 2026-07-29.
- `catalog.renameColumnInSelect` still does not walk a set-op's **right**
  branch, so `ALTER TABLE … RENAME COLUMN` misses column references there. Also
  pre-existing; this change only kept the *left* branch working by descending
  through the grouping node. Ledger row 2026-07-29.
- The full 99-query SF0.5 gate was not run (subset probe only). The host became
  quiet during this loop for the first time in six loops, so a following loop
  can and should discharge it.
