# 0125-0016 — The set-operation fold has no operator precedence

Status: **LANDED 2026-07-29**
Milestone: M0125 (`docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`)
Discovered by: M0125-0006 (deferral-ledger row 2026-07-29)
Area: `internal/planner` (set-op flattening/fold)
Companion: `0125-0006-setop-chain-associativity.md` (same fold, the other half of
the same class)

## 1. The defect

PostgreSQL's grammar declares set-operator precedence in
`postgres/src/backend/parser/gram.y:825-826`:

```
%left		UNION EXCEPT
%left		INTERSECT
```

The later declaration binds tighter, so **INTERSECT groups before UNION and
EXCEPT**, and each level is left-associative. `transformSetOperationStmt`
(`postgres/src/backend/parser/analyze.c`) therefore always receives a tree in
which INTERSECT runs are already grouped.

goopg flattens a set-op chain into a list of `(operator, operand)` segments and
folded that list **left-deep regardless of the operator**. With no parentheses
anywhere:

```sql
-- A={1,3}  B={2,3}  C={3}
SELECT x FROM A UNION SELECT x FROM B INTERSECT SELECT x FROM C
```

| | grouping | result |
|---|---|---|
| PostgreSQL 18.3 | `A UNION (B INTERSECT C)` | `{1,3}` |
| goopg (pre-fix) | `(A UNION B) INTERSECT C` | `{3}` |

This is the same blind-spot class as M0125-0006: a **wrong answer with a
plausible row count**. No row-count gate — the SF0.5 oracle, the nightly
anchors, the sweep comparator — can see it.

M0125-0006 landed a *local* precedence guard, `setOpBindsTighter`, consulted
only when deciding whether to cut a chain at a `)`. That made the
**parenthesised** spelling `(A) UNION (B) INTERSECT (C)` correct, because
flattening declines to cut when the operator after the `)` binds tighter. The
**bare** spelling stayed wrong, and was recorded as this task rather than folded
into that change (M0125-0006's own tests pin the parenthesised half, so the two
halves are independently verifiable).

## 2. Why it survived so long

Before M0125-0006 the parenthesised spelling was right *by accident* — the fold
stopped early at `if rightStmt.Parenthesized { break }`, which happened to
produce the tighter group. So every hand-written probe that used parentheses
(the natural way to write a mixed chain) agreed with PostgreSQL, and the bare
spelling is rare in generated SQL. TPC-DS never hits it: Q8, Q14 and Q38 are the
only queries containing INTERSECT, and each of their chains is homogeneous
(INTERSECT-only or UNION-only) inside its own subquery. Homogeneous chains are
unaffected because UNION and INTERSECT are associative.

## 3. The fix

`planSelect`'s fold (`internal/planner/planner.go`) becomes a two-level
precedence climb over the already-flattened segment list. Three closures replace
the single left-deep loop:

- `planSegment(i)` — plans segment `i`'s operand alone (the existing
  cut/plan/restore dance, unchanged, lifted verbatim out of the loop body).
- `applySetOp(acc, right, i)` — column-count check, type unification, and the
  `SetOp` node. `acc` is now **whatever the fold has accumulated on that
  operator's left**, which is the required re-base: `wrapSetOpBranchWithCasts`
  unifies types against the accumulated left operand, and with grouping that is
  no longer always the leftmost branch.
- `foldSetOpRange(acc, lo, hi)` — folds `segments[lo:hi)`:
  1. a leading INTERSECT run attaches directly to `acc` (nothing looser sits to
     its left inside the range);
  2. thereafter, each UNION/EXCEPT operand first absorbs the maximal INTERSECT
     run that follows it, and only then is joined to `acc`.

Left-associativity within each level is preserved by folding strictly
left-to-right at both levels.

### 3.1 The `InnerSegmentCount` barrier

`SelectStmt.InnerSegmentCount` (M0097-0044) marks a parenthesised head compound
that carries its own `ORDER BY`/`LIMIT`/`OFFSET`, e.g.
`(A UNION B ORDER BY 1) INTERSECT C`. The sort/limit applies to the first
`InnerSegmentCount` segments, not to the outer result.

That boundary is also a **hard precedence barrier**. The user's parentheses
grouped those segments explicitly, so an INTERSECT run written after the `)` may
not absorb the branch to its left. Without the barrier the example above would
become `A UNION (B INTERSECT C)` — the parenthesised UNION would silently
dissolve.

Folding each side of the barrier with a separate `foldSetOpRange` call gives
exactly that, and it preserves the invariant the sort/limit placement depends
on: after the first range is folded, `left` **is** the inner compound's value,
so `wrapSetOpSortLimit` still has the right node to wrap. This is why the fix is
expressed as a range fold rather than as a pre-pass over the segment list.

`ParenBranches` (M0125-0006) needs no analogous treatment: it is consumed during
*flattening*, before any segment exists, and `setOpBindsTighter` already declines
the cut when the trailing operator binds tighter — so a partially-parenthesised
operand never reaches the fold in a shape the grouping pass could mis-read.

## 4. Acceptance — by VALUE

`internal/executor/setop_precedence_test.go`, 17 cases, every `want` captured
from PostgreSQL 18.3 (port 65438) running the identical statement. Fixture and
helpers are shared with M0125-0006's suite.

- `TestSetOpBareChainPrecedence` (13) — bare chains only, in both precedence
  directions: INTERSECT to the right of a looser operator (wrong before), and
  INTERSECT to the left (where precedence and a left-deep fold agree, so these
  are controls). Covers `ALL` variants on both the loose and the tight operator,
  a two-link INTERSECT run, a run in the middle of a longer chain, and the
  UNION/EXCEPT tie.
- `TestSetOpPrecedenceStopsAtParenBoundary` (4) — the `InnerSegmentCount`
  barrier with `ORDER BY` and with `ORDER BY … LIMIT`; agreement between the
  bare and parenthesised spellings of the same chain; and explicit parentheses
  overriding precedence in the other direction
  (`A INTERSECT (B UNION C)`).

**The gate was proved to fail before being trusted.** Copied verbatim into a
worktree at pre-fix `70e1ca61`: **8 subtests FAIL**, all four controls PASS, and
`TestSetOpPrecedenceStopsAtParenBoundary` passes in full — confirming its four
cases are genuine non-regression pins and that the suite discriminates the
defect, not the diff.

M0125-0006's matrix (`internal/executor/setop_paren_assoc_test.go`, 17 executor
cases + 9 parser AST pins) is the non-regression pin for the parenthesised half
and passes unchanged.

## 5. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS (Q12=2, Q13=35) |
| TPC-DS SF0.5, set-op subset (Q8 Q14 Q23 Q38 Q49 Q87) | Q23/Q38/Q49/Q87 PASS, checksums **byte-identical** to the HEAD sweep `sweep-20260729-123114.txt`; Q8 ERROR and Q14 TIMEOUT are pre-existing and unchanged |
| pgbench smoke | PASS (pre-commit hook) |

The **full 99-query SF0.5 gate was not run**: its guard refuses while the
nightly CI batch is live (`FORCE=1` would be legitimate here — this gate is
accepted by row count and checksum, not by timing — but the sweep's Q5 peaks at
~21 GB RSS and the wedged nightly server already holds 7.5 GB of an 18 GB
budget). The subset above is the discriminating part: TPC-DS's only INTERSECT
chains are Q8/Q14/Q38's, and §2 explains why homogeneous chains cannot be
affected. Recorded in `.ralph/working_set.md` for the next quiet-host loop.

## 6. What is still missing

Nothing in the precedence model itself — `%left UNION EXCEPT` / `%left
INTERSECT` is the whole of PostgreSQL's set-operator precedence, and both levels
are now folded left-associatively.

Sibling gaps in the same area remain open and are tracked separately:

- **M0125-0017** — `ORDER BY`/`LIMIT` inside a parenthesised *single-branch*
  first operand is hoisted to the whole result and drops branches.
- **M0125-0018** — IN-list and EXISTS operand parsers reject a parenthesised
  set-op chain.
