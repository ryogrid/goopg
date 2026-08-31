# 0125-0006 — Set-operation chains re-associate right when branches are parenthesised

Status: **LANDED 2026-07-29**
Milestone: M0125 (`docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`)
Discovered by: M0124-0001 chunk 11 (deferral-ledger row 2026-07-28)
Area: `internal/parser` (AST + set-op parsing), `internal/planner` (set-op flattening)

## 1. The defect

SQL and PostgreSQL associate equal-precedence set operators **left to right**.
goopg did so only while the branches were bare. As soon as a branch was
parenthesised, the chain re-associated to the right:

```sql
-- A={1,2,3}  B={2}  C={3}
(A) EXCEPT (B) EXCEPT (C)
```

PostgreSQL 18.3 computes `(A EXCEPT B) EXCEPT C` = `{1}`.
goopg computed `A EXCEPT (B EXCEPT C)` = `{1,3}`.

This is a **wrong-answer defect, not a performance one**, and the first one the
TPC-DS round-2 programme found by *value* rather than by row count. TPC-DS Q87
is exactly this shape wrapped in a derived table:

```sql
select count(*) from ( (…store_sales…) except (…catalog_sales…) except (…web_sales…) ) cool_cust;
```

It returns **one row** either way, so the SF0.5 oracle, the nightly row anchors
and the sweep harness's own comparison were all structurally blind to it.

Measured on the SF=1 clusters, same data dir, both binaries:

| binary | Q87 | rows |
|---|---|---|
| pre-fix (`6c5c48ae`) | `47218` | 1 |
| post-fix | **`47049`** | 1 |
| PostgreSQL 18.3 (65438) | `47049` | 1 |

UNION-only and INTERSECT-only chains were unaffected **only** because those
operators are associative. Their passing was never coverage.

## 2. Root cause

`parseParenthesisedSelectStmt` (`internal/parser/select.go:981`) marks the
statement it just closed:

```go
innerSel.Parenthesized = true          // :1005 — set at the ')'
if setOp, present, _ := p.parseSetOpClause(); present {
    …                                  // :1007-1039 — then absorbs what FOLLOWS the ')'
}
```

The absorbed operator was written **outside** those parentheses, but it is
appended to the very same chain, so the node ends up carrying both
`Parenthesized == true` and an operator the parentheses never covered.

The planner's left-associativity flattening loop
(`internal/planner/planner.go:696`, pre-fix) trusted the flag:

```go
if rightStmt.Parenthesized {
    break // explicit grouping: stop flattening, treat as atomic
}
```

so it stopped at `B` and planned `A EXCEPT (B EXCEPT C)`.

`Parenthesized`'s own doc comment (`internal/parser/ast.go:861`) said "the whole
compound was explicitly wrapped in parentheses" — the flag was overloaded
against its own contract.

**PostgreSQL cannot express this bug at all.** `select_with_parens` is a *leaf*
operand in `gram.y`, so `transformSetOperationStmt`
(`postgres/src/backend/parser/analyze.c`) always receives an already left-deep
tree. goopg's set-op AST is a linked list (`SelectStmt.SetOp.Right`), and a
linked list can only mark a group by flagging the head of a sub-chain.

## 3. Why a boolean cannot be repaired in place

The obvious fix — "clear `Parenthesized` on the absorbing node" — is wrong,
and the probe that proves it was run against PG before any code changed:

```sql
-- X={3} A={2,3} B={3} C={9}
X UNION (A EXCEPT B) UNION C
```

PostgreSQL: `({3} ∪ ({2,3} ∖ {3})) ∪ {9}` = `{2,3,9}`.
Flag cleared ⇒ fully left-deep: `((({3} ∪ {2,3}) ∖ {3}) ∪ {9})` = `{2,9}`.

The parentheses wrap a **prefix** of the resulting chain — two branches here,
one in the `(A) EXCEPT (B) EXCEPT (C)` case. All-or-nothing is not one of the
available answers, so the boundary has to be recorded as a **count**.

## 4. The fix

### 4.1 Parser — record where the `')'` actually stood

New field, `internal/parser/ast.go`:

```go
// 0  — the parentheses covered this node's ENTIRE SetOp chain
// n>0 — only the first n branches (n-1 SetOp links) were inside them
ParenBranches int
```

`parseParenthesisedSelectStmt` now:

1. **resets** `ParenBranches = 0` at the closing `')'`, immediately after
   setting `Parenthesized`. This is load-bearing for nested parentheses:
   `((B) EXCEPT (C))` has an inner level that already recorded
   `ParenBranches = 1`, but the *outer* `')'` genuinely does cover that
   `EXCEPT`. Without the reset, an explicitly grouped operand would be
   re-flattened — the `nested_parens_reset_boundary` case.
2. sets `ParenBranches = 1` when the parenthesised content was a single branch
   and an operator follows the `')'`;
3. sets `ParenBranches = innerSegCount + 1` when the content was already a
   compound. `innerSegCount` was already computed there for
   `InnerSegmentCount`; it is the number of links inside the parentheses.

### 4.2 Planner — cut the chain at the boundary instead of breaking

`setOpSegment` gains `cutAt *parser.SelectStmt`: the node whose `SetOp` must be
detached while that segment is planned, so `planSelect(stmt)` sees exactly the
operand. `nil` means "plan with the chain intact" — a true leaf, or a fully
parenthesised compound that really is one atomic operand.

`parenBoundary(g)` turns `ParenBranches` into that node, returning `nil` when
the parentheses covered everything (the pre-fix behaviour, unchanged). The
save/clear/restore passes are now keyed on `cutAt` rather than on "every segment
but the last", which is the entire mechanical change; the plan-cache
restore discipline (`savedSetOps`) is preserved exactly.

### 4.3 Precedence guard at the boundary

Cutting correctly exposed a *second*, pre-existing defect. `gram.y:825-826`
declares

```
%left		UNION EXCEPT
%left		INTERSECT
```

so INTERSECT binds tighter. goopg's fold is precedence-blind, so
`A UNION B INTERSECT C` — **with no parentheses at all** — is already planned
`(A UNION B) INTERSECT C` and already diverges from PG (`{3}` vs `{1,3}`;
verified identical on pre-fix HEAD, so this is not a new defect). Before this
change the parenthesised spelling `(A) UNION (B) INTERSECT (C)` got the right
answer *by accident*, because flattening stopped early.

`setOpBindsTighter` therefore declines the cut when the operator after the `')'`
binds tighter than the one that introduced the operand, leaving the operand
atomic so the recursive `planSelect` builds the tighter group. That is not a
patch over the accident — it is locally correct precedence, and it is what keeps
this change non-regressing. The bare-chain gap remains and is filed as
**M0125-0016**.

## 5. Verification

All expected values are PostgreSQL 18.3's, taken from the live oracle on 65438
before any code changed.

**Unit tests.**
`internal/executor/setop_paren_assoc_test.go` — 17 by-value cases: the three
confirmed-wrong forms, the bare-first-branch variant, both compound-operand
cases (including the one that refutes flag-clearing), explicit right grouping,
nested/triple parens, a four-way chain, both precedence directions, and the two
associative-operator controls. Plus the derived-table (Q87 shape), CTE and
scalar-subquery contexts, per Hard-won Rule #2.

`internal/parser/setop_paren_assoc_test.go` — pins `ParenBranches` per chain
node for nine shapes, the "nothing follows the `')'` ⇒ 0" invariant the planner
depends on, and the INSERT-source use of `Parenthesized`.

**The gate was proved to fail before being trusted.** Copied verbatim into a
worktree at `6c5c48ae` and run there: **10 subtests FAIL**, while every
non-regression pin (`intersect_binds_tighter_than_union`,
`fully_parenthesised_right_operand_stays_atomic`, `nested_parens_reset_boundary`,
`compound_operand_grouping_preserved`, both controls) **passes** — i.e. the
suite discriminates the defect rather than the diff.

**SQL-level differential.** Four probe suites (30 statements) diffed
goopg-vs-PG. Every associativity divergence closed; the surviving diffs were
each confirmed *identical on pre-fix HEAD*, so none is a regression:

| shape | goopg | PG | status |
|---|---|---|---|
| `A UNION B INTERSECT C` (no parens) | `{3}` | `{1,3}` | pre-existing → **M0125-0016** |
| `(A ORDER BY 1 LIMIT 2) UNION ALL (C)` | `{1,2}` | `{1,2,9}` | pre-existing → **M0125-0017** |
| `x IN ((A) EXCEPT (B) …)` | syntax error | `{1}` | pre-existing → **M0125-0018** |
| `EXISTS ( (A) EXCEPT (B) … )` | syntax error | `t` | pre-existing → **M0125-0018** |
| `string_agg(x, ',' ORDER BY x)` | unordered | ordered | pre-existing → **M0125-0019** |

**Gates.**

- Units suite: PASS.
- `scripts/tpch-spotcheck.sh`: PASS (Q12 = 2 rows, Q13 = 35 rows).
- **TPC-DS SF0.5 gate: `PASS=74 CKMISMATCH=4` → `PASS=75 CKMISMATCH=3`, with
  `Q87 CKMISMATCH → PASS` the ONLY per-query change across all 99 queries**
  (normalised diff against the immediately preceding complete sweep at
  `6c5c48ae`; `MISMATCH=1 ERROR=2 TIMEOUT=14 SKIP=4` all unchanged, every other
  checksum byte-identical). Q87's goopg checksum moved `0500e34c344ea78d` →
  `b363a9287bdd0920`, which *is* the oracle's — so the SF0.5 gate does detect
  this defect by checksum, though not by row count. Same shape as M0125-0011's
  discharge.
- `make plan-diff LABEL=tpcds-round2-head`: **unrunnable — that baseline does not
  exist**, being M0124-0002's deliverable, which is still blocked on a quiet
  host. Diffing the newest available label (`r5-default`) is meaningless: it was
  captured with statistics loaded and any fresh server is S-cold, so all 22
  queries differ on `(stats)`/`rows=` alone (this is M0125-0003's subject).
  Substituted a **same-state pre/post A/B** — snapshots captured from the same
  data dir, both S-cold, with the pre-fix binary rebuilt from `6c5c48ae` — which
  is a stronger control for this change than any stored baseline:
  **22/22 TPC-H plans byte-identical** (`analysis/m0125-0006/m0125-0006-{pre,post}.txt`).
  This is expected by construction: TPC-H contains **zero** set operators, and
  the plan-snapshot harness covers only TPC-H Q1–Q22.
- pgbench smoke: via the pre-commit hook (mandatory on every commit).

Only five TPC-DS queries can reach the changed path at all (a `)` immediately
before a set operator): Q8, Q14, Q23, Q49, Q87 — and Q8's is the closing paren of
an `IN` list, not a parenthesised branch. Q8/Q14/Q16/Q23/Q47/Q49 are unchanged in
the sweep.

## 6. What this does NOT fix

Recorded as deferral-ledger rows the same day; each is a real PostgreSQL
behaviour goopg still lacks, not a scoping convenience:

- **M0125-0016** — the fold has no operator precedence, so any *bare* mixed
  chain with INTERSECT is planned left-deep. Resume at the fold in
  `planSelect` (`planner.go`): group maximal INTERSECT runs before the
  UNION/EXCEPT left-fold. Note `wrapSetOpBranchWithCasts` unifies types against
  the *accumulated* left, and `InnerSegmentCount`'s sort placement is defined on
  the flat segment index — both need re-basing on the grouped tree.
- **M0125-0017** — `ORDER BY`/`LIMIT` inside a parenthesised **first** branch is
  hoisted to the whole set-op result. `InnerSegmentCount == 0` is the "unset"
  sentinel, so a single-branch inner group cannot be expressed by it; the new
  `ParenBranches` is the natural carrier.
- **M0125-0018** — the IN-list and EXISTS parsers reject a parenthesised set-op
  chain as an operand.
- **M0125-0019** — `string_agg(…, … ORDER BY …)`: the aggregate's own ORDER BY
  is ignored.

## 7. Files

| file | change |
|---|---|
| `internal/parser/ast.go` | `SelectStmt.ParenBranches` |
| `internal/parser/select.go` | reset at `')'`; record the boundary on absorb |
| `internal/planner/planner.go` | `setOpSegment.cutAt`, `parenBoundary`, `setOpBindsTighter`, `cutAt`-keyed save/restore |
| `internal/parser/setop_paren_assoc_test.go` | AST-shape pins (new) |
| `internal/executor/setop_paren_assoc_test.go` | by-value acceptance matrix (new) |
