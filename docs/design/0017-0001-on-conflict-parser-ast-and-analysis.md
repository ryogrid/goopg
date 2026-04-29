# 0017-0001 — ON CONFLICT Parser, AST, and Analysis

**Status:** accepted (steps 1–2 + Stage B constraint-name target)
**Milestone:** [0017 — UPSERT Support](../milestones/0017-upsert-on-conflict-do-update.md)
**Spans seam:** SQL parser surface, INSERT AST shape, analyzer hook
points (deferred).
**Cross-links:**
[root-0010](root-0010-parser.md) (parser scaffolding),
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md) (the
WITH-clause precedent for parser-only step-1 slices),
[0018-0001](0018-0001-explain-parser-options-and-ast.md) (the EXPLAIN
parser-only precedent).

## Context

goopg's parser supports `INSERT INTO target [(cols)] VALUES (rows)
[RETURNING …]` but does not yet recognise `ON CONFLICT …`. Modern
ORMs (sqlx, Diesel, SQLAlchemy 2.0, ActiveRecord) emit upserts as
their default idempotent-write path; lacking parser support means
every such query fails at the lex/parse layer with a generic
syntax error rather than the targeted SQLSTATE feature-gating that
later analyzer/planner stages can produce.

This slice introduces the upstream-compatible **parser surface and
AST nodes** without yet wiring the analyzer / planner / executor
— mirroring the M0016-0001 (WITH clause) and M0018-0001 (EXPLAIN
options) step-1 pattern. Establishing the AST shape in one
well-tested commit lets subsequent stages (analyzer name
resolution → planner conflict-arbiter selection → executor
concurrency path) land incrementally with each step having a
known landing point in the AST.

## Target upstream major

PostgreSQL 18.x. Mirrors `postgres/src/backend/parser/gram.y` and
`postgres/src/include/nodes/parsenodes.h`'s `OnConflictClause`
shape.

## Grammar

The parser accepts every upstream `INSERT … ON CONFLICT …` shape
this milestone targets, including the constraint-name target form
that Stage A would otherwise reject:

```
insert_stmt        ::= [WITH …] INSERT INTO target [(cols)]
                       VALUES (vals) [, …]
                       [on_conflict_clause]
                       [RETURNING target_list]

on_conflict_clause ::= ON CONFLICT [conflict_target] conflict_action

conflict_target    ::= '(' col_name [, col_name …] ')'
                     | ON CONSTRAINT constraint_name

conflict_action    ::= DO NOTHING
                     | DO UPDATE SET assign_list [WHERE expr]
```

Three target forms × two action types = six valid combinations;
all six parse cleanly. Stage A in M0017 narrows the supported
analyzer subset to `(cols) DO UPDATE SET …`; Stage B promotes
the constraint-name target and `excluded.col` references in
`SET` / `WHERE`.

`excluded.col` is not a parser-level concern — it surfaces as a
normal `*ColumnRef{Table: "excluded", Column: <name>}` because
`excluded` is intentionally **not** a keyword. Upstream uses the
same convention: `excluded` is a special pseudo-table that the
analyzer's name resolver injects into the conflict-resolution
scope. Promoting it to a keyword would shadow legitimate column
references named `excluded` in pre-UPSERT clusters.

## AST shape

```go
// InsertStmt grew an OnConflict field. Pre-M0017 callers see nil.
type InsertStmt struct {
    pos        int
    With       *WithClause
    Target     RangeVar
    Columns    []string
    Rows       [][]Expr
    OnConflict *OnConflictClause   // nil when no ON CONFLICT
    Returning  []ResTarget
}

type OnConflictAction int

const (
    OnConflictNone    OnConflictAction = iota // zero value reserved
    OnConflictNothing                         // DO NOTHING
    OnConflictUpdate                          // DO UPDATE SET …
)

type OnConflictTarget struct {
    pos        int
    Columns    []string // populated for `(col [, col …])` form
    Constraint string   // populated for `ON CONSTRAINT name` form
}

type OnConflictClause struct {
    pos         int
    Target      *OnConflictTarget // nil for the no-target shape
    Action      OnConflictAction
    UpdateSet   []UpdateAssign    // populated when Action == OnConflictUpdate
    UpdateWhere Expr              // optional; nil when no WHERE follows SET
}
```

Three nil/empty discriminators encode the parser-accepted forms:

| Form                                          | Target           | Action            | UpdateSet | UpdateWhere |
| --------------------------------------------- | ---------------- | ----------------- | --------- | ----------- |
| `ON CONFLICT DO NOTHING`                      | nil              | OnConflictNothing | nil       | nil         |
| `ON CONFLICT (cols) DO NOTHING`               | {Columns}        | OnConflictNothing | nil       | nil         |
| `ON CONFLICT ON CONSTRAINT name DO NOTHING`   | {Constraint}     | OnConflictNothing | nil       | nil         |
| `ON CONFLICT (cols) DO UPDATE SET …`          | {Columns}        | OnConflictUpdate  | [...]     | nil         |
| `ON CONFLICT (cols) DO UPDATE SET … WHERE …`  | {Columns}        | OnConflictUpdate  | [...]     | non-nil     |
| `ON CONFLICT ON CONSTRAINT name DO UPDATE …`  | {Constraint}     | OnConflictUpdate  | [...]     | nil/non-nil |

`OnConflictNone` is the zero value of `OnConflictAction`, reserved
for "no clause". Combined with `InsertStmt.OnConflict==nil`, this
guards against a stray zero-value `OnConflictClause` accidentally
being treated as a valid action — defensive, since Go's zero-value
semantics make accidental misconstruction easy.

## New keywords

```go
KwConflict Keyword = "conflict"
KwDo       Keyword = "do"
KwNothing  Keyword = "nothing"
```

`UPDATE`, `SET`, `WHERE`, `ON`, and `CONSTRAINT` are already
keywords from earlier milestones — no new entries needed.
`excluded` stays an unreserved identifier (see Grammar above).

## Disambiguation

The optional conflict target is disambiguated by the next token
after `CONFLICT`:

- `(` → column-list form: parse `parseColumnNameList()` then expect `)`.
- `ON` (keyword) → constraint-name form: consume `ON`, expect `CONSTRAINT`, parse identifier.
- Anything else → no target; the next token must be `DO`.

This is the upstream gram.y rule order — note that `ON CONFLICT ON
CONSTRAINT` is the canonical two-`ON` shape PostgreSQL accepts.

## Tests

`internal/parser/on_conflict_test.go`:

- `TestParseInsertOnConflictNoTargetDoNothing` — pins the no-target
  branch (`Target` stays nil, `Action == OnConflictNothing`).
- `TestParseInsertOnConflictColumnTargetDoNothing` — column-list
  target combined with DO NOTHING.
- `TestParseInsertOnConflictDoUpdate` — full Stage A surface:
  column-list target + DO UPDATE SET with multiple assignments,
  including an `excluded.col` reference. Pins that the reference
  parses as a `*ColumnRef{Table:"excluded", Column:"b"}` so the
  analyzer slice has a stable input.
- `TestParseInsertOnConflictDoUpdateWithWhere` — pins the optional
  `UpdateWhere` field.
- `TestParseInsertOnConflictConstraintTarget` — `ON CONFLICT ON
  CONSTRAINT name` shape. Stage B-only at the analyzer level, but
  the parser already accepts it so the AST shape is stable across
  stages.
- `TestParseInsertOnConflictWithReturning` — composes ON CONFLICT
  with RETURNING in the upstream-mandated order.
- `TestParseInsertOnConflictCTE` — composes the WITH wrapper with
  ON CONFLICT (M0016 + M0017 surface combine without parser
  interaction).
- `TestParseInsertOnConflictRejectsBadAction` — diagnostic guard:
  `DO REPLACE` / `DO INSERT` etc. error at parse time.
- `TestParseInsertOnConflictRejectsMissingDo` — bare `(a) NOTHING`
  (without `DO`) errors.
- `TestParseInsertWithoutOnConflictUnchanged` — rollout guardrail:
  an INSERT without ON CONFLICT must continue to produce a nil
  `OnConflict` field — no spurious empty clauses for callers that
  haven't migrated.

## Step 2 — analyzer wiring (landed 2026-04-29)

Step 2 attaches the parsed `OnConflictClause` to catalog metadata
and the `excluded` pseudo-table scope so subsequent stages
(planner conflict-arbiter selection, executor concurrency path)
can rely on a fully-validated AST.

### scopeRel grew a `qualifiedOnly` mode

```go
type scopeRel struct {
    table         *catalog.Table
    alias         string
    qualifiedOnly bool   // ← new
}
```

A `qualifiedOnly` rel is hidden from the unqualified column-resolution
loop in `resolveColumnRefTypeAt` and reachable on the qualified path
**only by alias** (never by the underlying table's catalog name).
Without this dual-restriction, registering `excluded` as a synthetic
rel pointing at the same `*catalog.Table` would (a) make every bare
`col` ambiguous between target and excluded, and (b) make
`<target>.col` ambiguous because both rels share `rel.table.Name`.
The new branch in the qualified arm of `resolveColumnRefTypeAt`
restricts qualifiedOnly matches to alias-only:

```go
if rel.qualifiedOnly {
    if rel.alias != "" && strings.EqualFold(x.Table, rel.alias) &&
        (x.Schema == "" || strings.EqualFold(x.Schema, rel.table.Schema)) {
        matches = append(matches, rel)
    }
    continue
}
```

### analyzeOnConflict

`analyzeInsert` calls a new `analyzeOnConflict(oc, tbl, cat,
targetAlias)` helper after the existing INSERT validation. The
helper:

1. **Stage B reject (`ON CONSTRAINT`):** the parser accepts the
   shape so the AST is stable across stages; the analyzer is the
   gate. Surfaces SQLSTATE `0A000` "Stage B" message.
2. **`ON CONFLICT DO UPDATE` requires a target:** mirrors upstream's
   "ON CONFLICT DO UPDATE requires inference specification or
   constraint name" rule (SQLSTATE `42601`). Without a target there's
   no arbiter to drive resolution.
3. **Conflict-target column validation:** for the `(cols)` form,
   each named column must exist on the target table (SQLSTATE
   `42703`). The planner picks the actual unique-arbiter index in
   M0017-0002 — the analyzer just enforces existence here.
4. **DO UPDATE scope build:** two rels:
   - target table aliased as `s.Target.Alias` (or the table name
     when no alias). Bare refs and qualified-by-alias refs resolve
     here.
   - target table aliased as `excluded`, `qualifiedOnly: true`.
     Reachable only via `excluded.col`.
5. **SET assignment validation:** mirrors `analyzeUpdate` —
   target column existence (`42703`), RHS type compatibility
   (`42804`).
6. **Optional WHERE:** analyzed under the same scope, must return
   boolean (`42804`).

### Planner gate

`planInsert` rejects any `InsertStmt` carrying a non-nil
`OnConflict` with SQLSTATE `0A000` "ON CONFLICT is not supported in
v0 planner". This is a second-line gate so an executable plan
never silently drops the clause when a caller bypasses the
analyzer (some test paths do). M0017-0002 promotes the planner to
arbiter selection + plan-node generation.

### Tests

`internal/analyzer/on_conflict_test.go`:

- `TestAnalyzeOnConflictDoNothingNoTarget` — simplest accepted
  shape.
- `TestAnalyzeOnConflictDoNothingWithTarget` — column-list target
  + DO NOTHING; pins the target-column-existence check.
- `TestAnalyzeOnConflictDoUpdateBasic` — bare refs resolve to
  target, `excluded.col` resolves via the pseudo-table scope.
- `TestAnalyzeOnConflictDoUpdateMixedRefs` — `abalance + excluded.abalance`;
  pins the qualifiedOnly rule (without it the bare ref would be
  ambiguous).
- `TestAnalyzeOnConflictDoUpdateWithWhere` — optional predicate +
  qualified ref via the target's table name on both sides
  (`pgbench_accounts.abalance < excluded.abalance`); pins the
  qualified-arm restriction that prevents
  `pgbench_accounts.abalance` from matching the excluded rel.
- `TestAnalyzeOnConflictRejectsConstraintTarget` — `0A000` Stage B
  diagnostic.
- `TestAnalyzeOnConflictRejectsUpdateWithoutTarget` — `42601`.
- `TestAnalyzeOnConflictTargetUnknownColumn` — `42703` on
  conflict-target columns.
- `TestAnalyzeOnConflictUpdateSetUnknownColumn` — `42703` on SET
  columns.
- `TestAnalyzeOnConflictUpdateExcludedUnknownColumn` — `42703`
  via the excluded pseudo-table.
- `TestAnalyzeOnConflictUpdateTypeMismatch` — `42804` on RHS type.
- `TestAnalyzeOnConflictUpdateWhereNonBoolean` — `42804` on WHERE.

`internal/planner/with_test.go`:

- `TestPlanInsertOnConflictRejected` — second-line `0A000` gate.

Full `go test ./...` green.

## Stage B — constraint-name target (landed 2026-04-30)

Stage B promotes the `ON CONFLICT ON CONSTRAINT name` target form
from Stage A's hard-reject (`0A000`) to a fully-supported path.
The parser already accepted the shape from step 1 — only the
analyzer + planner gates change.

### Analyzer

The Stage B reject in `analyzeOnConflict` is replaced with catalog
validation. For `Target.Constraint != ""`:

```go
idx, ok := cat.LookupIndex(parser.ObjectName{Name: target.Constraint})
if !ok       { → 42704 "constraint X for table Y does not exist" }
if idx.Table != tbl { → 42704 "constraint X does not belong to Y" }
if !idx.Unique     { → 42P10 "constraint X is not a unique constraint" }
```

Mirrors upstream's three diagnostics (parsenodes.h /
parse_clause.c `transformOnConflictClause`):
- 42704 (`undefined_object`) — unknown constraint name.
- 42704 — wrong table.
- 42P10 (`invalid_column_reference`) — non-unique index used as
  arbiter.

### Planner

`resolveArbiterIndex` gains a constraint-name branch executed
ahead of the column-set inference loop. Returns the named index
plus column ordinals matching `idx.Columns` so the executor's
existing `ArbiterColumns` handling works without change. The
analyzer-side rejects are mirrored as second-line `PlanError`
checks for paths that bypass the analyzer.

### Executor

No new code path — the upsertOp consumes `ArbiterIndex` /
`ArbiterColumns` regardless of how the planner resolved them.
The same conflict-detect-and-apply machinery handles both
target forms.

### Tests

- analyzer: `TestAnalyzeOnConflictAcceptsConstraintTarget`,
  `TestAnalyzeOnConflictRejectsUnknownConstraint` (replaces the
  old Stage B reject test), `TestAnalyzeOnConflictRejectsNonUniqueConstraint`,
  `TestAnalyzeOnConflictRejectsConstraintOnDifferentTable`.
- planner: `TestPlanInsertOnConflictByConstraintName` — pins
  ArbiterIndex pointer and ArbiterColumns ordinals.
- executor: `TestUpsertConflictByConstraintName` — full
  end-to-end UPSERT against the constraint-name target form.

Full `go test ./...` green.

## Out of scope

- Planner conflict-arbiter selection — M0017-0002.
- Executor concurrency / locking integration — M0017-0003.
- Observability counters — M0017-0004.
- `INSERT … SELECT` (orthogonal — pre-existing limitation).
- MERGE statement support (out of M0017 entirely; see
  `milestones/0017-upsert-on-conflict-do-update.md` Out of Scope).
