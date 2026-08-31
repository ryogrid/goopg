# M0134-0010 — NOT NULL-driven reduction of `IS NULL` / `IS NOT NULL` restriction quals

Status: accepted (2026-08-19)
Milestone task: `M0134-0010` (`postgres/src/test/regress/sql/predicate.sql`)
Scope of this doc: the **single-baserel restriction-clause** slice only.

## 1. Why

`predicate.sql` is 100% `EXPLAIN (COSTS OFF)` plan-shape assertions. A sizing run
(`scripts/pg-regress-runner.sh --verbose predicate`, artifact
`tmp/regress-diffs/predicate.diff`) found **18 of 22 EXPLAINs diverge, with zero
`ERROR` lines** — no hard blockers, but five *independent* root causes:

| # | root cause | diverging EXPLAINs |
|---|---|---|
| 1 | single-table restriction-clause NOT NULL qual reduction missing | ~4 |
| 2 | same reduction missing for join `ON`-clause quals | ~6 |
| 3 | outer-join nullability tracking (`varnullingrels`) absent | 0 today (goopg never reduces, so it never over-reduces) |
| 4 | `Sort` / `Materialize` node emission differs (unrelated, pre-existing) | ~7 |
| 5 | inheritance per-child constraint exclusion / qual pushdown | 4 |

Categories 4 and 5 are separable features far larger than this case; the case is
therefore **PARKED** and only category 1 — the smallest independently-shippable,
genuinely PG-faithful behavior — is implemented here. Category 1 also supplies the
proof primitive that category 2 will reuse.

## 2. The PostgreSQL 18.3 oracle

Call site: `postgres/src/backend/optimizer/plan/initsplan.c:2979
add_base_clause_to_rel()`, gated on `!rte->inh || rte->relkind ==
RELKIND_PARTITIONED_TABLE` — i.e. **plain base-rel quals only**. That gate is
exactly why this slice excludes inheritance (category 5).

- `restriction_is_always_true()` (`initsplan.c:3091`) → the clause is **dropped
  silently**; EXPLAIN shows no `Filter:` line at all.
- `restriction_is_always_false()` (`initsplan.c:3156`) → the clause is **replaced**
  with `makeBoolConst(false, false)`, which surfaces as a `Result` node with
  `One-Time Filter: false`.

Both delegate to `expr_is_nonnullable()` (`initsplan.c:3054`):

```c
if (!IsA(expr, Var)) return false;
if (!bms_is_empty(var->varnullingrels)) return false;   /* nullable by an outer join */
if (var->varattno < 0) return true;                     /* system column */
rel = find_base_rel(root, var->varno);
if (var->varattno > 0 && bms_is_member(var->varattno, rel->notnullattnums)) return true;
return false;
```

`RelOptInfo.notnullattnums` is built once per base rel in
`postgres/src/backend/optimizer/util/plancat.c:166-194` from **`attnotnull`** —
the column's NOT NULL flag, *not* a CHECK constraint.

Decision rules, verbatim from the oracle:

- `IS NOT NULL` is provably **TRUE** iff `nulltesttype == IS_NOT_NULL && !argisrow
  && expr_is_nonnullable(arg)`.
- `IS NULL` is provably **FALSE** under the identical conditions with
  `nulltesttype == IS_NULL`.
- Row-valued tests (`argisrow`, i.e. `(a,b) IS NULL`) are **never** folded.
- Only the `NullTest` node is recognised — no other spelling.
- **OR clauses** (`initsplan.c:3122-3143` and `3187-3212`) apply the same two
  helpers **recursively per arm**, with *asymmetric* quantifiers:
  - the whole OR is dropped if **ANY** arm is provably true;
  - the whole OR folds to FALSE only if **ALL** arms are provably false.
  PG deliberately does not prune individual disprovable arms otherwise. This is
  not part of `eval_const_expressions`.

Expected shapes (`postgres/src/test/regress/expected/predicate.out`, table
`pred_tab(a int NOT NULL, b int, c int NOT NULL)`):

| SQL | PG plan | line |
|---|---|---|
| `t.a IS NOT NULL` | bare `Seq Scan on pred_tab t`, no `Filter:` | 16-21 |
| `t.b IS NOT NULL` | `Filter: (b IS NOT NULL)` kept | 24-30 |
| `t.a IS NULL` | `Result` / `One-Time Filter: false` | 34-40 |
| `t.a IS NOT NULL OR t.b = 1` | bare scan (ANY-arm-true) | 56-61 |
| `t.b IS NOT NULL OR t.a = 1` | `Filter: ((b IS NOT NULL) OR (a = 1))` kept | 65-71 |
| `t.a IS NULL OR t.c IS NULL` | `Result` / `One-Time Filter: false` (ALL-arms-false) | 75-81 |
| `t.b IS NULL OR t.c IS NULL` | `Filter:` kept | 85-90 |

## 3. goopg today

- **No NOT NULL-driven qual simplification exists.** `internal/optimizer/foldconst.go:21
  FoldConstants` (one call site, `planner.go:1739`) is pure literal folding with no
  `*IsNullExpr` case and no catalog access. `internal/optimizer/qual_canonical.go:66
  canonicalizeQual` (`planner.go:1113`) runs *pre-resolution* on raw `parser.Expr` and
  only hoists common AND-conjuncts out of an OR. Neither is a viable host.
- AST: `internal/optimizer/plan.go:283 IsNullExpr{ Operand Expr; Negated bool }` is the
  planned-layer node (`Negated == true` means `IS NOT NULL`). The parser-layer twin is
  `internal/parser/expr.go:242`. `internal/nodes/ir.go:159 NullTest` is the
  pg_node_tree/outfuncs mirror, **not** the live planner path — do not touch it.
- NOT NULL is available at plan time: `internal/catalog/catalog.go:102 Column.NotNull`,
  and scan nodes carry `*catalog.Table` directly (`plan.go:575 SeqScan.Table`, likewise
  `IndexScan.Table`). Existing planner precedent for the lookup idiom:
  `planner.go:6409-6411` (`notNullByName[c.Name] = c.NotNull`).
- **The `Result` / one-time-filter primitive already exists** —
  `internal/optimizer/plan.go:1265-1278 Result{ Targets; OneTimeFilter; Child }`
  (mirrors `nodeResult.c rs_checkqual`), executed at
  `internal/executor/operators.go:407-436` and rendered at
  `internal/executor/operators_explain.go:631` (`"One-Time Filter: "`) and `:1838`
  (node label `Result`). Its single existing producer is the constant-arg min/max
  rewrite at `planner.go:9211-9215` — a directly reusable template.

## 4. Design

A new file `internal/optimizer/notnull_qual_reduce.go` holding one pass, invoked
from the planner **after column resolution** and **only when the query's FROM is a
single base table** (goopg's stand-in for PG's `!rte->inh` base-rel gate).

```
// reduceNotNullQuals mirrors initsplan.c restriction_is_always_true/false.
// Returns the rewritten predicate and alwaysFalse.
reduceNotNullQuals(pred Expr, tbl *catalog.Table, srcIdx int) (Expr, bool)
```

- `exprIsNonNullable(e Expr, tbl, srcIdx) bool` — the `expr_is_nonnullable` twin:
  true only for a resolved column reference **of that same base table** whose
  `catalog.Column.NotNull` is set. Anything that is not a plain column ref of that
  rel returns false. (goopg has no `varnullingrels`; the single-baserel gate means
  no outer join can be in scope, so the nullability check is satisfied structurally
  — this is recorded as a deferral for the join-qual slice.)
- Applied to each top-level AND conjunct:
  - conjunct provably TRUE → drop it;
  - conjunct provably FALSE → the whole predicate is FALSE (short-circuit);
  - OR conjunct → PG's asymmetric rule: ANY arm true ⇒ drop the conjunct; ALL arms
    false ⇒ predicate is FALSE; otherwise keep the conjunct **unmodified** (no
    per-arm pruning).
- Row-valued operands are never folded (PG's `argisrow` guard). If goopg's
  `IsNullExpr` cannot represent a row-valued operand, that is asserted in a test
  rather than assumed.
- Outcomes:
  - all conjuncts dropped → the scan gets **no** `Filter` (bare `Seq Scan`);
  - predicate is FALSE → wrap in `Result{OneTimeFilter: BooleanConst{false}}`,
    reusing the `planner.go:9211-9215` template verbatim;
  - otherwise → `Filter` over the surviving conjuncts.

### Blast radius / safety

The pass is a no-op unless (a) the query has exactly one base table, and (b) a
top-level conjunct is an `IsNullExpr` over a NOT NULL column of that table. Both
transforms are *semantics-preserving by construction*: dropping a tautology and
folding a contradiction cannot change any result row. Any query with a join, a
subquery source, or inheritance takes the untouched path.

## 5. What this does NOT do (deferred — see `.ralph/deferral_ledger.md`, 2026-08-19)

1. Join `ON`-clause quals (`initsplan.c` distributes these too) — category 2, ~6 EXPLAINs.
2. Outer-join nullability (`Var.varnullingrels`) — goopg has no equivalent. Required
   *before* category 2 can ship safely, since reducing a join qual on a Var made
   nullable by an outer join is a **wrong answer**, not just a plan-shape diff.
3. Inheritance per-child constraint exclusion / qual pushdown — category 5.
4. `Sort` / `Materialize` node emission parity — category 4, unrelated pre-existing.
5. System-column non-nullability (`var->varattno < 0` ⇒ always non-nullable).

`predicate.sql` therefore stays `not-tried` in
`docs/test-port/postgres-oracle-target-inventory.csv` — **no `make regen-testport`
this loop.**

## 5b. Also landed (found while implementing, outside §4's original scope)

Two defects surfaced only once the FALSE case rendered, and both were fixed rather
than deferred because each is a *byte-level PG-parity* bug in a path this slice
newly exercises:

1. **Childless `Result`.** PG's always-false plan is a `Result` with **no** child
   (`predicate.out` lines 34-40 / 75-81 show `(2 rows)` and no `->` line); the first
   round attached the now-unreachable scan. `Result.Child` is now `nil` for this
   case. Verified safe before the change: `internal/executor/operators.go:407-436`
   sets `qualFailed` in `Open` and returns before touching `o.child`; `Next`/`Close`
   gate on it; `executor.go Build()` already had a `p.Child == nil` case (the
   pre-existing min/max childless-Result path); `operators_explain.go planChildren`
   already returns nil for a nil child; and `Result.Output()` reads the explicitly
   set `schema` field, so the row description is unaffected.
2. **`One-Time Filter: (false)` → `false`.** `operators_explain.go` unconditionally
   parenthesized the one-time filter expression. Real PG never parenthesizes a bare
   literal there but does parenthesize compound expressions (e.g.
   `(100 IS NOT NULL)`). The wrap is now conditional on the expression not being a
   bare literal constant. Blast radius checked: exactly one render site in goopg
   source and zero goopg-tracked golden files reference the string; the only other
   production one-time-filter producer (`SELECT max(100) FROM t`) is covered by the
   existing suite and a new compound-expression test guards the paren behavior.

## 6. Review notes carried forward

- Category 3 is the trap: goopg passes the "must NOT reduce" cases today only
  because it never reduces anything. The moment category 2 lands without
  nullability tracking, those cases flip from accidentally-right to actively-wrong.
  Do not ship the join-qual variant before the nullability primitive.
- The OR quantifiers are asymmetric (ANY-true vs ALL-false). Swapping one side is
  the expected bug and is covered by dedicated tests.
