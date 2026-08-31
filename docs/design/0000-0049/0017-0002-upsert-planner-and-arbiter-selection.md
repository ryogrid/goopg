# 0017-0002 — UPSERT Planner and Arbiter Selection

**Status:** accepted (Stage A planner slice)
**Milestone:** [0017 — UPSERT Support](../../milestones/0017-upsert-on-conflict-do-update.md)
**Spans seam:** Planner Insert node, conflict-arbiter resolution,
DO UPDATE expression resolution.
**Cross-links:**
[0017-0001](0017-0001-on-conflict-parser-ast-and-analysis.md)
(parser + analyzer slices),
[root-0011](../../root/root-0011-planner.md) (planner scaffolding).

## Context

Step 1 (parser) gives us a parsed `*parser.OnConflictClause`.
Step 2 (analyzer) validates it against the catalog and the
`excluded` pseudo-table scope. This slice produces the
**executable** planner-side state: the resolved unique index that
arbitrates conflict detection plus expression-resolved
`UpdateSet`/`UpdateWhere` ready for the runtime evaluator.

The runtime side (M0017-0003) consumes this state to detect
conflicts at INSERT time and apply the DO UPDATE branch.

## Plan-node shape

```go
type Insert struct {
    pos         int
    Table       *catalog.Table
    Source      Node
    ColumnIndex []int
    OnConflict  *OnConflictPlan      // ← new in M0017-0002
}

type OnConflictAction int

const (
    OnConflictActionNothing OnConflictAction = iota
    OnConflictActionUpdate
)

type OnConflictPlan struct {
    Action         OnConflictAction
    ArbiterIndex   *catalog.Index    // nil for the no-target DO NOTHING shape
    ArbiterColumns []int             // tbl.Columns ordinals matching ArbiterIndex.Columns
    UpdateSet      []Expr            // parallel to tbl.Columns; nil = leave alone
    UpdateWhere    Expr              // optional predicate
}
```

`Insert.OnConflict` is nil for every pre-M0017 INSERT, so existing
tests stay byte-for-byte unchanged.

## Arbiter selection

`resolveArbiterIndex(target, tbl, cat)` walks `cat.IndexesOnTable(tbl)`
looking for a unique index whose column **set** (case-insensitive,
order-insensitive) equals the user-supplied target column set:

```go
for _, idx := range cat.IndexesOnTable(tbl) {
    if !idx.Unique { continue }
    if len(idx.Columns) != len(target.Columns) { continue }
    // set equality on lower-cased names
    if matches(idx.Columns, wantedSet) {
        return idx, ordinals(idx.Columns), nil
    }
}
return nil, nil, &PlanError{Code: "42P10", ...}
```

Diagnostics:

- `42P10` "no unique or exclusion constraint matching the ON
  CONFLICT specification on relation X" — the canonical upstream
  error code (`invalid_column_reference`). Surfaces when no unique
  index covers the target.
- `42P10` on duplicate column names in the target list.
- `42601` "ON CONFLICT target requires at least one column" —
  defensive (the parser already accepts `(col)` minimum, but
  catches a future grammar relaxation).
- `0A000` on the constraint-name target form — Stage B reserved.

Multi-column targets work as long as a unique index covers the
exact set. `(a, b)` matches an index on `(a, b)` and an index on
`(b, a)` — the user's column **list** is treated as a set per
upstream semantics. The first matching unique index wins; the
catalog's iteration order is stable, so the choice is
deterministic.

`ArbiterColumns` is the per-target-column ordinal list in
`tbl.Columns` order matching `ArbiterIndex.Columns` (catalog
column order). The executor uses these ordinals to extract the
conflict key from the inserted-row tuple without re-doing a name
lookup.

## DO UPDATE expression resolution

The crux of the slice is letting `excluded.col` references in
`SET … = excluded.col` and `WHERE … excluded.col` resolve to the
inserted row, not the existing one. The planner's `rangeBinding`
gained a `qualifiedOnly bool` mode mirroring the analyzer-side
`scopeRel.qualifiedOnly` from M0017-0001 step 2:

- Hidden from the unqualified column-resolution loop.
- In the qualified arm, matches **only by alias** (never via the
  underlying table's catalog name).

Without that dual-restriction, registering `excluded` as a
synthetic binding pointing at the same `*catalog.Table` would
(a) make every bare `col` ambiguous between target and excluded,
and (b) make `<target>.col` ambiguous because both bindings share
`b.table.Name`.

The planner builds a 2-binding scope:

```go
n := len(tbl.Columns)
bindings := []rangeBinding{
    {table: tbl, alias: primaryAlias, offset: 0},
    {table: tbl, alias: "excluded", offset: n, qualifiedOnly: true},
}
mergedSchema := tableSchema(tbl) || tableSchema(tbl)  // 2N wide
ctx := newResolveContext(bindings, mergedSchema)
```

`UpdateSet` is built parallel to `tbl.Columns`: each target
column's assignment slot holds the resolved Expr (or nil for
unchanged columns). `UpdateWhere`, when present, resolves under
the same context.

Resolved `*ColumnRef` nodes carry `Index` values that the runtime
evaluator looks up in a 2N-wide tuple: indices `0..N-1` reference
the existing tuple's columns, indices `N..2N-1` reference the
inserted-but-conflicting tuple's columns. The executor will arrange
this layout when it lands the runtime path.

## Executor gate

Until M0017-0003 lands the runtime, the executor's `*planner.Insert`
build-path rejects when `p.OnConflict != nil`:

```go
case *planner.Insert:
    if p.OnConflict != nil {
        return nil, fmt.Errorf("ON CONFLICT execution is not supported in v0 …")
    }
    …
```

This is a deliberate two-step gate: the planner produces a
fully-formed plan (so misuses surface specific catalog / arbiter
errors, not a generic "ON CONFLICT not supported" curtain), and
the executor refuses to silently drop the clause.

## Tests

`internal/planner/with_test.go`:

- `TestPlanInsertOnConflictNoMatchingArbiter` — `42P10` when no
  unique index covers the target. Uses pgbench's default schema
  (no index on aid).
- `TestPlanInsertOnConflictWithUniqueIndex` — installs a unique
  index on `aid`, plans the canonical UPSERT, and asserts:
  ArbiterIndex points at the new index, ArbiterColumns is `[0]`
  (aid is column ordinal 0), `UpdateSet[2]` (abalance) is non-nil,
  UpdateWhere is nil.
- `TestPlanInsertOnConflictDoNothingNoTarget` — bare
  `ON CONFLICT DO NOTHING` plans without arbiter resolution
  (ArbiterIndex stays nil; runtime checks all unique indexes).

Full `go test ./...` green.

## Out of scope

- Runtime conflict detection (index probe at INSERT time) —
  M0017-0003.
- DO UPDATE apply path (read existing tuple, evaluate
  UpdateSet/UpdateWhere, write back) — M0017-0003.
- Locking semantics under concurrent writers — M0017-0003.
- `ON CONFLICT ON CONSTRAINT name` target form — M0017 Stage B.
- Observability counters — M0017-0004.
