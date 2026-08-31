# M0125-0017 — the parenthesised head branch had nowhere to put its own ORDER BY / LIMIT

status: accepted
date: 2026-07-29
area: parser, planner (set operations)
related: [0125-0006](0125-0006-setop-chain-associativity.md),
[0125-0016](0125-0016-setop-operator-precedence.md),
[0097-0024](../../0050-0099/0097-0024-setops-union-intersect-except.md),
[0097-0024b](../../0050-0099/0097-0024b-setop-trailing-orderby-binding.md)

## 1. The defect

```sql
(SELECT x FROM a ORDER BY 1 LIMIT 2) UNION ALL (SELECT x FROM h)
```

PostgreSQL 18.3 returns `{1, 2, 9}`. goopg returned `{1, 2}` — **the entire
`UNION ALL` branch vanished**, because the head branch's `LIMIT 2` was applied
to the union instead of to the branch the user parenthesised.

This is the third member of the class 0125-0006 opened: a **wrong answer with a
plausible row count**. Two rows came back, the query did not error, and every
row-count gate in the project (the SF0.5 oracle, the nightly anchors, the sweep
harness) is structurally blind to it. It is arguably the worst of the three,
because the missing rows are a whole *branch* rather than a regrouping.

## 2. Why PostgreSQL has no equivalent problem

In `postgres/src/backend/parser/gram.y` the sort and limit clauses hang off
`select_no_parens`, and `select_with_parens` wraps one:

```
select_no_parens: select_clause opt_sort_clause … opt_select_limit …
select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'
```

`select_with_parens` is a **leaf operand** of the set-operation productions, so a
sort or limit written inside the parentheses is simply a field on that branch's
own `SelectStmt` node, structurally distinct from the compound's. PG never has
to decide who owns the clause; the tree already says.

## 3. Why goopg did

goopg stores a set-op chain as a **linked list** — `SelectStmt.SetOp.Right` —
rather than a tree. The head branch and the whole compound are therefore the
*same node*, sharing one `OrderBy` / `Limit` / `Offset` slot. Whoever writes it
last wins, and the planner has no way to tell from the fields alone whether the
clause was written inside the parentheses or after them.

M0097-0044 had already met this for the case where the parenthesised content was
itself a compound, and answered it with `InnerSegmentCount`: "the sort/limit
applies to the result of the first *n* segments". But that field uses **0 as its
unset sentinel**, so it could not express the commonest boundary of all — the
parentheses closing after the **first** branch, i.e. after *zero* set-op
segments. `parseParenthesisedSelectStmt` set it only in the `innerSel.SetOp !=
nil` arm; the single-branch arm had no way to say anything at all, and the
planner read the resulting 0 as "no boundary".

The same shape as 0125-0006: the linked-list AST needs an explicit carrier for
what a tree would have encoded structurally.

## 4. The fix

### 4.1 Parser — `SelectStmt.InnerSortLimit` (`internal/parser/ast.go`)

A single bit that makes `InnerSegmentCount`'s zero meaningful: *the ORDER BY /
LIMIT / OFFSET currently on this node were written INSIDE the parentheses, and
cover the first `InnerSegmentCount` segments — possibly none.*

`parseParenthesisedSelectStmt` (`internal/parser/select.go`) sets it in three
places, and the ordering of the function's blocks is what makes it sound:

1. **Single-branch arm** (`innerSel.SetOp == nil`, alongside `ParenBranches = 1`,
   M0125-0006's carrier). Anything in the slots at that moment was parsed before
   the `')'` — the lift-from-right and trailing-clause blocks have not run yet —
   so it belongs to the parenthesised branch. Boundary at segment 0.
2. **Compound arm**, alongside `InnerSegmentCount = innerSegCount`, now guarded
   by `!innerSel.InnerSortLimit`: a nested paren level may already have claimed
   these clauses for a boundary further left, and this level's `')'` encloses
   that boundary but must not **widen** it. Without the guard,
   `((A ORDER BY 1 LIMIT 2) UNION ALL C) EXCEPT D` would relabel the head
   branch's limit as covering `A UNION ALL C`.
3. **Lift-from-right block**, now skipped entirely when `InnerSortLimit` is set:
   those slots are spoken for. Leaving the clauses on the right branch is
   exactly right when that branch is parenthesised — PG keeps each branch's own
   limit in `(A LIMIT 2) UNION ALL (C ORDER BY 1 LIMIT 1)`, which goopg now
   answers correctly as a side effect.
4. **Trailing-clause block**: a clause written after the `')'` belongs to the
   whole chain and collides with a head-branch boundary for the one available
   slot. The two cannot coexist in this AST, so the outer clause wins and
   `InnerSortLimit` is cleared — the pre-M0125-0017 behaviour, deliberately kept
   rather than silently applying outer text to an inner operand. Restricted to
   `InnerSegmentCount == 0` so M0097-0044's boundary is byte-for-byte unchanged.
   Deferred with a ledger row (2026-07-29).

### 4.2 Planner — consume the boundary, don't consume the AST (`internal/planner/planner.go`)

```go
headSortLimit := s.InnerSortLimit && s.InnerSegmentCount == 0
```

The head boundary is handled by **not** doing something: the save/clear block
before `planSelect(s, cat)` skips clearing `OrderBy`/`Limit`/`Offset`, so the
recursive `planSelect` applies them as an ordinary SELECT's own sort and limit,
below the `SetOp`. That is simpler than the `InnerSegmentCount` path *and more
faithful*: `select_with_parens` is a leaf in PG, so its ORDER BY may reference a
non-output expression. `(SELECT x FROM a ORDER BY x*-1 LIMIT 2) UNION ALL …` is
legal in PG and unanswerable by any fix that re-resolves the sort key against
the set-op output columns.

No change is needed to `foldSetOpRange`. A precedence barrier at segment 0 is
vacuous — `foldSetOpRange(left, 0, 0)` is the identity — so the existing single
full-range fold already gives the right grouping, including M0125-0016's
INTERSECT run absorption (`(A ORDER BY 1 LIMIT 2) UNION h INTERSECT g`
= `{1,2} UNION ({9} INTERSECT {2,3})` = `{1,2}`).

The final `wrapSetOpSortLimit` is skipped via a new local `sortLimitConsumed`.
That local **replaces** the six lines the `InnerSegmentCount` path used to blank
`savedOrderBy`/`savedLimit`/`savedOffset` and `s.OrderBy`/`s.Limit`/`s.Offset`
to stop the outer wrapper re-applying the inner sort. Those assignments were
never restored, so `Plan()` **consumed the SelectStmt**: a second `Plan()` of the
same AST — which the plan cache can ask for — produced an unlimited plan. Fixed
here because the same lines are being rewritten;
`TestPlanSetOpBoundaryIsReplannable/inner_compound_boundary` fails at pre-fix
HEAD with `Plan #1 consumed the AST: OrderBy=[] Limit=<nil>`.

## 5. Acceptance — BY VALUE

Row counts cannot see this defect, so every case is asserted by value against
PostgreSQL 18.3 (the read-only oracle on port 65438) running the identical
statement.

| file | what it pins |
|---|---|
| `internal/executor/setop_head_branch_sortlimit_test.go` | 18 cases: `{ORDER BY, LIMIT, both}` × `{parenthesised, bare}` right branch, OFFSET, non-output sort key, redundant parens, the boundary as a precedence barrier (EXCEPT and an INTERSECT run), both branches carrying their own limit, plus a separate ordering assertion (`ORDER BY 1 DESC LIMIT 2` → `3,2,9`) that a sorted-multiset comparison could not see |
| `internal/planner/setop_head_sortlimit_test.go` | the two structural invariants: the `Limit` lands **below** the `SetOp` and not above it, and `Plan()` leaves the AST replannable (both boundary kinds) |
| `internal/parser/setop_paren_assoc_test.go` | `InnerSortLimit` / `InnerSegmentCount` for all seven shapes, including the nested-widening and trailing-collision rules |

**Proved to fail at pre-fix HEAD** (`9e77…`/`19d844b4`): 8 of the executor
subtests, both planner invariants (`no Limit under the SetOp's left input`;
`Plan #1 consumed the AST`). Every control passed there — the outer
`ORDER BY 1 LIMIT 2`, M0097-0044's inner-compound boundary, and the plain
parenthesised EXCEPT chain — so the change is pinned as non-regressing on the
two boundaries that already worked.

## 6. Benchmark reachability

**TPC-DS cannot reach this defect at all.** A reflection walk over all 99 SF0.5
query files, parsing each and inspecting every `SelectStmt`, found **zero** nodes
with `InnerSortLimit` set (3 files fail to parse for unrelated pre-existing
reasons: query36, query70, query86). TPC-H uses no set operations. The defect is
reachable only from hand-written SQL, which is precisely why it survived to
2026-07-29.

Gates run: units suite, `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35), and the
SF0.5 set-op subset (Q23 Q38 Q49 Q87) — all four checksum-PASS against the
git-tracked oracle and byte-identical to the HEAD sweep `sweep-20260729-123114`.
The full 99-query gate was again not run beside the wedged nightly batch; see
the working set.

## 7. What is still missing (deferral ledger, 2026-07-29)

1. **An outer ORDER BY cannot coexist with a head-branch boundary.**
   `(A ORDER BY 1 LIMIT 2) UNION ALL (C) ORDER BY 1 DESC` returns PG's three
   rows but in an unspecified order, because the one `OrderBy` slot belongs to
   the head. Pre-fix HEAD returned two rows — a lost row is strictly worse than
   a lost ordering, so this is an improvement, not a new gap; but it is not PG.
2. **A trailing clause after the `')'` discards the head branch's own.**
   `((A ORDER BY 1 LIMIT 2) UNION ALL C) LIMIT 1` gives the outer `LIMIT 1` the
   slot and loses the inner `LIMIT 2`.

Both have the same root cause and the same real fix: the head branch needs its
**own `SelectStmt`**, i.e. the set-op chain must become a tree the way
`transformSetOperationStmt` sees one, instead of a linked list with per-node
boundary annotations. `ParenBranches` (0125-0006), `InnerSegmentCount`
(0097-0044) and now `InnerSortLimit` are three separate patches around the same
missing structure; a fourth should be read as a signal to do the conversion.
