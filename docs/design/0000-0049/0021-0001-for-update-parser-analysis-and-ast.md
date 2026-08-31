# 0021-0001 — SELECT … FOR UPDATE Parser, AST, and Analysis

**Status:** accepted (steps 1–2: parser + AST + analyzer)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
**Spans seam:** SQL parser surface, SelectStmt AST shape,
analyzer / planner / executor hook points (deferred).
**Cross-links:**
[root-0010](../../root/root-0010-parser.md) (parser scaffolding),
[0017-0001](0017-0001-on-conflict-parser-ast-and-analysis.md)
(parser-only step-1 precedent),
[0018-0001](0018-0001-explain-parser-options-and-ast.md)
(another parser-only step-1 precedent).

## Context

goopg parses `SELECT … [LIMIT N | OFFSET N | FETCH …]` cleanly,
but the v0 grammar doesn't yet recognise the trailing
`FOR { UPDATE | SHARE } …` row-locking clause. Modern application
stacks (Diesel, ActiveRecord, Hibernate, sqlx) emit
`SELECT … FOR UPDATE` for the read-modify-write pattern; lacking
parser support means these queries fail at the lex/parse layer
with an unhelpful syntax error instead of the targeted SQLSTATE
gating that subsequent analyzer / planner / executor stages can
produce.

This slice introduces the **parser surface and AST nodes** without
yet wiring the analyzer / planner / executor — mirroring the
M0016-0001 (WITH clause), M0017-0001 (ON CONFLICT), and
M0018-0001 (EXPLAIN options) step-1 pattern. Establishing the AST
shape in one well-tested commit lets later stages (validation,
plan-node lock metadata, runtime row-lock acquisition) land
incrementally with each step having a known landing point.

## Target upstream major

PostgreSQL 18.x. Mirrors `postgres/src/backend/parser/gram.y` and
`postgres/src/include/nodes/parsenodes.h`'s `LockingClause` shape.

## Grammar

```
select_stmt          ::= … [ORDER BY …] [LIMIT … | FETCH …]
                                 (locking_clause)*

locking_clause       ::= FOR lock_strength
                                 [ OF table_name [, …] ]
                                 [ NOWAIT | SKIP LOCKED ]

lock_strength        ::= UPDATE
                                 | SHARE
```

Stage A scope:

- `UPDATE` and `SHARE` strengths. `NO KEY UPDATE` and `KEY SHARE`
  (upstream extensions) stay deferred — the AST has room for
  them but the parser only accepts the two upstream-canonical
  strengths.
- Multiple locking clauses per SELECT, in source order. Upstream
  uses this for combined intents like
  `FOR UPDATE OF a NOWAIT FOR SHARE OF b`.
- Optional `OF table_name [, …]` list. The parser captures the
  raw identifiers; alias / table-name resolution against the
  FROM list is the analyzer's job (M0021-0001 step 2).
- Optional `NOWAIT` or `SKIP LOCKED` wait modifier. Parser
  accepts both; analyzer / executor will narrow the supported
  subset by stage (Stage A: blocking only; Stage B: NOWAIT /
  SKIP LOCKED).

Locking clauses come after `LIMIT/OFFSET/FETCH` in upstream's
grammar. ORMs emit `... LIMIT 10 FOR UPDATE`; tests pin the order.

## AST shape

```go
type LockStrength int

const (
    LockStrengthForUpdate LockStrength = iota + 1  // FOR UPDATE
    LockStrengthForShare                           // FOR SHARE
)

type LockWaitPolicy int

const (
    LockWaitBlock      LockWaitPolicy = iota   // default — wait
    LockWaitNoWait                              // NOWAIT — fail with 55P03
    LockWaitSkipLocked                          // SKIP LOCKED — drop row
)

type LockingClause struct {
    pos        int
    Strength   LockStrength
    Targets    []string         // OF list — empty = all FROM rels
    WaitPolicy LockWaitPolicy
}

type SelectStmt struct {
    …
    Locking []*LockingClause    // ← new in M0021-0001 step 1
}
```

`SelectStmt.Locking` is empty for every pre-M0021 SELECT — keeps
existing parser/planner/executor tests byte-for-byte unchanged.
`LockStrengthForUpdate` is intentionally `iota+1` so the zero
value `LockStrength(0)` is reserved for "uninitialised /
shouldn't-happen" — mirrors the precedent
`OnConflictNone` set on `OnConflictAction` in M0017-0001.

## New keywords

```go
KwShare  Keyword = "share"
KwOf     Keyword = "of"
KwNowait Keyword = "nowait"
KwSkip   Keyword = "skip"
KwLocked Keyword = "locked"
```

`KwFor` and `KwUpdate` already exist from earlier milestones; no
new entries required for them.

## Planner gate

`planSelect` rejects any SelectStmt carrying a non-empty
`Locking` slice with SQLSTATE `0A000` "row-level locking clauses
are not supported in v0 planner". This is the same two-step gate
pattern from M0017-0001 / M0017-0002: parse the surface so
diagnostics surface specific feature names, refuse to silently
produce a plan that drops the locking intent. M0021-0002
promotes the planner to row-lock metadata + executor wiring.

## Tests

`internal/parser/locking_test.go`:

- `TestParseSelectForUpdateBasic` — bare `FOR UPDATE` with
  default WaitBlock policy.
- `TestParseSelectForShare` — read-intent variant.
- `TestParseSelectForUpdateOf` — multi-target OF list, source
  order preserved.
- `TestParseSelectForUpdateNoWait` — NOWAIT modifier.
- `TestParseSelectForUpdateSkipLocked` — two-keyword SKIP LOCKED
  modifier.
- `TestParseSelectForUpdateMultiClause` — two clauses with
  different strengths and wait policies, collected in source
  order.
- `TestParseSelectForUpdateAfterLimit` — locking clause comes
  after LIMIT/ORDER BY (matches upstream and ORM emission).
- `TestParseSelectForRejectsBadStrength` — `FOR READ` errors.
- `TestParseSelectForUpdateRequiresLocked` — `SKIP` without
  `LOCKED` errors.
- `TestParseSelectWithoutLockingUnchanged` — rollout guardrail.

Full `go test ./...` green.

## Step 2 — analyzer wiring (landed 2026-04-30)

`analyzeLockingClauses(s, ctx)` runs at the tail of
`analyzeSelectWithParent` when `len(s.Locking) > 0`. Mirrors
upstream's `transformLockingClause` / `preprocess_rowmarks`
rejection set:

- **Must have FROM**: locking is meaningless without rows from a
  relation. `SELECT 1 FOR UPDATE` errors with SQLSTATE `0A000`
  ("FOR UPDATE/SHARE is not allowed in this context").
- **No GROUP BY / HAVING**: aggregation produces grouped rows
  that don't map back to individual storage tuples. Errors with
  SQLSTATE `0A000`.
- **`OF` target resolution**: each name in the locking clause's
  Targets list must match a FROM-clause range variable — by
  alias when one is set, otherwise by table name (matches
  upstream's alias-shadows-table rule for column references).
  Mismatch surfaces SQLSTATE `42P01` (the canonical
  "relation not in FROM" diagnostic).

`lockingTargetMatches(name, rels)` is a small helper that walks
the analyzer's `scopeRel` slice, skipping `qualifiedOnly` rels
(none present in the SELECT scope today; future-proofing for
cases where the merged-row trick from M0017 might leak into
SELECT analysis).

Wait-policy modifiers (NOWAIT, SKIP LOCKED) are accepted by the
analyzer for AST stability across stages — the planner /
executor narrow the supported runtime subset later.

### Tests

`internal/analyzer/locking_test.go`:

- `TestAnalyzeForUpdateBasic` — happy-path FROM + FOR UPDATE.
- `TestAnalyzeForUpdateOfAlias` — `OF a` resolves through alias.
- `TestAnalyzeForUpdateOfTableNameWithoutAlias` — bare table name
  works when no alias is set.
- `TestAnalyzeForUpdateRequiresFromClause` — `0A000`.
- `TestAnalyzeForUpdateRejectsGroupBy` — `0A000`.
- `TestAnalyzeForUpdateRejectsHaving` — `0A000`.
- `TestAnalyzeForUpdateUnknownTarget` — `42P01`.
- `TestAnalyzeForUpdateRejectsTableNameWhenAliased` — `42P01`
  (alias-shadows-table guarantee).
- `TestAnalyzeForShareAccepted` — read-intent variant.
- `TestAnalyzeForUpdateMultiClauseAccepted` — multi-clause shape
  validates each clause's targets independently against the
  same FROM list.

Full `go test ./...` green.

**2026-07-08 update (M0021-0002 unimplemented_feat.json entry):**
aggregate-functions-in-target detection landed. `analyzeLockingClauses`
gained a target-list scan (`targetHasBareAggregate` / local
`isAnalyzerAggregateName`, mirroring `parser.exprContainsAggregateCall`
/ `planner.isAggregateFunc`'s standard-aggregate name set — each
package keeps its own small local copy rather than sharing one, the
existing convention here) that rejects `SELECT count(*) FROM t FOR
UPDATE` with `0A000 "FOR UPDATE is not allowed with aggregate
functions"`, matching upstream's `CheckSelectLocking`'s `qry->hasAggs`
branch (`postgres/src/backend/parser/analyze.c`) both in error code and
message text (confirmed against `expected/portals.out`'s `SELECT
MIN(f1) FROM uctest FOR UPDATE` case). The walk mirrors
`parser.exprContainsAggregateCall`'s exact case set (FuncCall/BinaryOp/
UnaryOp/CastExpr/IsNullExpr/IsBoolExpr/IndirectionStar) so a bare
aggregate is caught whether it's the whole target expression or nested
inside one (e.g. `sum(x) + 1`). New tests
`TestAnalyzeForUpdateRejectsBareAggregate` /
`TestAnalyzeForUpdateRejectsAggregateInExpr` in `locking_test.go`
(confirmed non-vacuous via `git stash`). Full `go test ./...` green;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
failed, all 3 workloads).

## Out of scope

- Locking inside subqueries / CTEs — upstream forbids this, the
  analyzer recurses into subquery analysis but doesn't yet plumb
  the "outer locking context" needed to detect inner-query
  conflicts.
- Planner row-lock metadata + plan-node propagation —
  M0021-0002.
- Executor row-lock acquisition + blocking semantics —
  M0021-0002 (Stage A, blocking only).
- NOWAIT / SKIP LOCKED runtime — M0021-0003 (Stage B).
- Deadlock + observability — M0021-0004.
- `FOR NO KEY UPDATE` / `FOR KEY SHARE` strengths — out of M0021
  entirely.
