# M0134-0034: ON CONFLICT arbiter inference must use exact-set matching

## Status
CONTAINED slice of the `insert_conflict.sql` sizing (M0134-0034). Parked after
landing — see `.ralph/fix_plan.md` Current Priority banner for the park note
and the remaining REFACTOR-tier/standalone buckets (EXPLAIN plan shape for
ON CONFLICT, GiST exclusion-constraint enforcement, `excluded` whole-row
reference in WHERE, attached-partition local-index arbiter lookup).

## Problem

`resolveArbiterIndex` (`internal/optimizer/planner.go:10691-10806`) infers
which unique/exclusion index a bare `ON CONFLICT (cols...)` target refers to.
The current implementation (added under the mistaken belief that PG uses
"liberal/subset" matching — see the stale comment at planner.go:10731-10736)
accepts an index as a match whenever **every index column is covered by the
target's column set**, even if the target names extra columns the index
doesn't have. It does the same "any expression present" leniency for
expression-index columns: it never actually compares the expression AST, it
just checks that the target supplied *some* expression slot.

This is a genuine correctness bug, not cosmetic: goopg silently accepts and
*executes* `ON CONFLICT` clauses that real PG rejects with 42P10, updating
rows through the wrong (or a coincidentally-also-matching) arbiter index.

## PG oracle

`postgres/src/backend/optimizer/util/plancat.c`, `infer_arbiter_indexes`:
- Plain-column matching (`plancat.c:883-885`):
  ```c
  if (!bms_equal(indexedAttrs, inferAttrs))
      goto next;
  ```
  This is **set equality**, not subset/superset. `indexedAttrs` is the set of
  plain (non-expression) index columns; `inferAttrs` is the set of plain
  columns named in the `ON CONFLICT` target. Extra columns in either
  direction disqualify the index.
- Expression-column matching (`plancat.c:892-950`): each index expression
  element must find an `equal()`-matching entry among the target's
  expressions (real parse-tree node equality, order-sensitive is NOT
  required but each slot must be truly matched, not just "some expression is
  present").

The upstream doc comment goopg misquotes ("liberal in accepting inference
specifications") is about PG tolerating **duplicate entries within one
inference clause** (e.g. `ON CONFLICT (a, a)`), not about accepting an index
that doesn't exactly match the named columns.

## Fix (this slice)

In `resolveArbiterIndex`, `internal/optimizer/planner.go`:

1. **Plain columns — exact set equality.** Build the plain-column set from
   `idx.Columns` (skip `""` expression slots) the same way `plainWanted` is
   already built from `target.Columns`, then require the two sets to be
   equal (same size, same members) instead of "every index column present in
   wanted". Also count expression slots on both sides and require equal
   counts (mirrors `bms_equal` catching an index that has more/fewer plain
   columns than named, and matches the same-arity requirement PG applies to
   expression columns).

2. **Expression columns — real equality, not presence.** `target.Exprs` (see
   `internal/parser/ast.go` `OnConflictTarget.Exprs`, parallel to `Columns`)
   holds the parsed AST for each `""` column slot; `idx.ColExprs` (see
   `internal/catalog/catalog.go` `Index.ColExprs`, parallel to `Columns`)
   holds the same for the index's expression columns. Replace the
   "`exprCount == 0` ⇒ no match" placeholder with an actual per-slot
   structural-equality check between `idx.ColExprs[i]` and some element of
   `target.Exprs`, ignoring source position (mirror the ignore-position
   convention `exprEqual`/`exprIdentityKey` already use for the *resolved*
   `optimizer.Expr` tree at planner.go:14245-14267 — note these operate on a
   different, already-resolved `Expr` type than `parser.Expr`, so they
   cannot be called directly here; write a small position-insensitive
   structural comparator for `parser.Expr` scoped to the node kinds that can
   legally appear in an index expression: `*parser.FuncCall` (compare
   function name case-insensitively + recursively compare each argument) and
   `*parser.ColumnRef` (compare the referenced column name
   case-insensitively) at minimum — extend only as far as the regress
   fixtures in `postgres/src/test/regress/sql/insert_conflict.sql` actually
   exercise; do not build a generic AST-equality framework here, that is
   REFACTOR-tier and out of scope). A structurally-undecidable/unsupported
   node kind on either side must compare NOT-equal (never silently match) —
   same fail-safe convention as `exprEqual`'s comment at planner.go:14245-14249.
   `defaultExprToSQL` (`internal/executor/operators_ddl.go:5594`) is
   **not** reachable from `internal/optimizer` (executor imports optimizer,
   not the reverse) — do not attempt to reuse it or introduce an import that
   creates a cycle.

3. **42P10 error position.** `planner.go:10805`'s no-match return currently
   sets `Pos: target.Pos()`. PG's `ereport` for this case
   (`plancat.c:957-960`) carries no `errposition()`, matching the sibling
   `InitiallyDeferred` branch two dozen lines above
   (`planner.go:10713-10717`) which already documents this exact PG
   convention. Change to `Pos: 0` with the same comment style.

## Acceptance criteria

- `scripts/pg-regress-runner.sh --verbose insert_conflict` diff line count
  drops from the pre-fix baseline (539 lines / `^-ERROR`=9) — expect the 7
  lines tied to superset-matching (diff lines ~170, ~285, ~287, ~363, ~390,
  ~413 in the pre-fix diff) plus the `^` position lines tied to bucket C to
  disappear. Re-capture the new diff and confirm no *new* `^+ERROR`/`^-ERROR`
  lines appear (a regression would show up as a previously-passing statement
  now failing).
- A quick negative check: `INSERT ... ON CONFLICT (a) DO UPDATE ...` against
  a table with only a two-column unique index `(a,b)` must now raise 42P10
  (today it silently "matches" via the old leniency logic) — add or extend a
  targeted unit/executor test asserting this if the existing suite doesn't
  already cover it.
- No prior-passing regress case may start failing because of the
  tightened matching — grep
  `postgres/src/test/regress/sql/*.sql` for `on conflict` usages and spot-run
  the ones most likely to exercise multi-column or expression arbiters
  (`upsert.sql` if present, `insert_conflict.sql` itself) after the change.

## Deferred (this slice does not touch these — see `.ralph/deferral_ledger.md`)

- EXPLAIN output for `Insert ... ON CONFLICT` never surfaces
  `Conflict Resolution:` / `Conflict Arbiter Indexes:` / `Conflict Filter:`
  lines, or the correct child-node shape (`Result` vs goopg's
  `Values (1 rows)`) — REFACTOR-tier, needs ModifyTable/Insert plan-node +
  EXPLAIN-printer changes.
- `EXCLUDE USING gist` exclusion constraints are never physically enforced
  (known gap, see memory `goopg_gist_grid_cell_ssi` — GiST indexes are
  catalog-only) — a conflicting row inserts cleanly and a later
  `ON CONFLICT ON CONSTRAINT ... DO NOTHING` against the same (unbuilt) GiST
  index throws goopg's own "short read at block" error. REFACTOR-tier, own
  milestone.
- Whole-row `excluded` reference at WHERE-clause top level
  (`... where parted_conflict = (50, ...) and excluded = (50, ...)`) errors
  "column \"excluded\" does not exist" even though `excluded.*` works
  elsewhere in the same statement — likely an RTE-visibility gap in
  `internal/parser/analyzer/analyzer.go`'s `excluded` handling. Not sized;
  needs its own probe before it can be called CONTAINED.
- An attached-partition local unique index
  (`alter index ... attach partition ...`) is not recognized as a valid
  arbiter for `ON CONFLICT (a)` on the partition — possibly a
  `cat.IndexesOnTable`/attachment-visibility gap adjacent to but distinct
  from this slice's fix. Not sized.
