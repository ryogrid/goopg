# Auto-updatable views + WITH CHECK OPTION enforcement

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                                |
| Date       | 2026-07-04                                              |
| Milestone  | M0119-0004 (DU-002 slice 365 follow-up)                 |
| Refines    | [0003-0009-views.md](0003-0009-views.md)                |
| Supersedes | —                                                        |

## Problem

DU-002 slice 365 (2026-06-30) made `WITH [CASCADED\|LOCAL] CHECK OPTION`
round-trip through `pg_dump`, but left the clause unenforced at runtime — no
`44000` anywhere in the executor. The working assumption (recorded in the
deferral ledger and in `0003-0009-views.md`'s "What's NOT supported" section)
was that INSERT/UPDATE/DELETE against a view produced a **read-only planner
error**, so CHECK OPTION enforcement would need an INSERT/UPDATE rewrite
mechanism built from scratch.

That assumption was stale. Empirically (this loop), `planInsert`/
`planUpdate`/`planDelete` never checked `catalog.Table.View` at all:

```sql
CREATE TABLE t (id int primary key, val int);
INSERT INTO t VALUES (1,10),(2,20),(3,30);
CREATE VIEW v AS SELECT id, val FROM t WHERE val > 15;
INSERT INTO v VALUES (4, 5);   -- reports "INSERT 0 1" (success!)
SELECT * FROM t;               -- row (4,5) is NOT there
UPDATE v SET val = 1 WHERE id = 2;  -- reports "UPDATE 0" (silently touches nothing)
```

The INSERT silently wrote a row keyed on the **view's own OID** — storage no
SELECT ever reads back, because every reference to a view substitutes its
defining query (`planScanRangeVar`, `0003-0009-views.md`) rather than
scanning the view's own (nonexistent) heap. The UPDATE similarly scanned the
view's phantom storage and matched zero rows. Both statements reported
success while silently doing nothing — worse than an error, and worse than
the design doc's own description of the gap.

## Decision: restricted auto-updatable rewrite

PostgreSQL's real rule (`rewriteHandler.c`'s `view_is_auto_updatable` /
`rewrite_targetlist`) auto-rewrites INSERT/UPDATE/DELETE against a view onto
its single base relation when the view has no joins/aggregation/set-ops/
limiting, using a **per-column attribute map** that tolerates the view
exposing a renamed/reordered/subset of the base table's columns. Anything
outside that (multi-relation, `INSTEAD OF` triggers absent) is rejected with
`error_view_not_updatable` — `55000`
(`ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE`), *not* `42809` as a stale code
comment in `internal/planner/copy.go` might suggest (that 42809 is COPY's own
separate relkind check, `copyfrom.c`/`copyto.c` — unaffected by this change).

goopg has no `INSTEAD OF` trigger/rule mechanism at all (`catalog.TriggerInsteadOf`
exists as metadata but nothing ever fires it — confirmed by search), so this
change implements a **deliberately narrower** subset of PG's rule:
`viewAutoUpdatableBase` (`internal/planner/view_dml.go`) accepts a view only
when its defining query is:

- Exactly one base relation in `FROM`, no joins, no subquery/table-function
  range var, and that relation is itself a real heap table (`View == nil`,
  `!Virtual`, `!IsMatView`) — no view-of-view chaining.
- No `DISTINCT`/`GROUP BY`/`HAVING`/`LIMIT`/`OFFSET`/set-op/locking-clause/
  `VALUES`-source/`WITH`.
- A target list that is a **bare, unrenamed, in-order passthrough of every
  column of the base relation** — either a single unqualified `SELECT *`, or
  an explicit column list naming each base column once in catalog order with
  no alias (or an alias identical to the column's own name), and no
  `CREATE VIEW name (col_list) AS ...` rename list.

This means `CREATE VIEW v AS SELECT * FROM t WHERE ...` and
`CREATE VIEW v AS SELECT id, val FROM t WHERE ...` (when `id, val` is exactly
`t`'s column list, in order) are auto-updatable; a view that subsets,
reorders, or renames columns (`CREATE VIEW v(xid) AS SELECT id FROM t`) is
NOT — it gets the `55000` rejection even though real PostgreSQL would allow
it. This is a conscious, documented scope cut: because the base table's
column layout is *guaranteed identical* to the view's own `catalog.Table.Columns`
in this subset, the rewrite can reuse the view's already-resolved
`Set`/`Where`/`Returning` expressions unchanged after swapping the target
`*catalog.Table` pointer — no per-column attribute-map plumbing, no risk of
misaligning a renamed/reordered column against the base table's physical
layout. Widening to PG's full rule is future work (see "Deferred" below).

## Implementation

`internal/planner/view_dml.go`:
- `viewAutoUpdatableBase(tbl, cat) (base, ok)` — the eligibility check above.
- `viewQualOnBase(tbl, base, cat) (Expr, error)` — resolves the view's own
  `WHERE` (if any) against the base relation, using the **view definition's
  own** FROM-clause alias (not whatever alias the outer DML statement uses)
  so the qual's column references resolve independently of the caller's SQL.
- `viewNotUpdatableError(pos, name, cmd)` — PG's exact wording/hint per
  command (`error_view_not_updatable`, `rewriteHandler.c`), SQLSTATE `55000`.

`planInsert`/`planUpdate`/`planDelete` (`internal/planner/planner.go`), right
after the initial `cat.LookupTable` of the DML target: if `tbl.View != nil`,
resolve `base` via `viewAutoUpdatableBase`; reject with `55000` on failure;
otherwise reassign `tbl = base` and continue unmodified — every later line in
these functions (`colIndex` derivation, `ON CONFLICT` arbiter resolution,
`RETURNING`, `SET` assignment) already operates on whatever `*catalog.Table`
`tbl` points to, so no other code needed to change.

Two additional things thread through the rewrite:

1. **View qual as a row filter (UPDATE/DELETE).** PostgreSQL only lets
   UPDATE/DELETE through a view touch rows the view's own `WHERE` would
   include. The resolved qual is AND'd into the WHERE-derived predicate (or
   used standalone when there's no user WHERE) and applied as a `Filter`
   over the base-table scan. `UPDATE ... FROM`/`DELETE ... USING` against a
   view is rejected unconditionally rather than threading the qual through
   the cross-product path (out of scope, ledgered).

2. **CHECK OPTION as a per-row post-check (INSERT/UPDATE only — PG never
   checks it on DELETE).** When `tbl.CheckOption != ""`, the same resolved
   qual is stashed on the plan node (`Insert.ViewCheckQual`/`ViewCheckName`,
   `Update.ViewCheckQual`/`ViewCheckName`) and evaluated by the executor
   (`checkViewCheckOption`, `internal/executor/operators_fk.go`, sibling to
   the existing `checkConstraints` CHECK-constraint enforcer) against the
   finalized row — the just-built INSERT row, or the post-`SET` UPDATE row.
   NULL or FALSE raises `44000` ("new row violates check option for view
   %s"), matching `execMain.c`'s `WCO_VIEW_CHECK` exactly (verified against
   `postgres/src/include/utils/errcodes.h`'s
   `ERRCODE_WITH_CHECK_OPTION_VIOLATION`). Because a single-base-relation
   view can't have an underlying view to cascade into, `CASCADED` and
   `LOCAL` are semantically identical in this restricted subset — no
   separate cascade-walk needed.

### A latent bug this surfaced: `updateViaIndex` drops residual predicates

Wrapping an index-eligible UPDATE's plan in an *additional* `Filter` for the
view qual (`Filter{Child: IndexScan, Predicate: viewQual}`) initially passed
`go build` and even the first manual check, but a regression test
(`TestUpdatableViewWhereQualRestrictsUpdateDeleteTargets`) caught it: an
`UPDATE v SET val=999 WHERE id=1` against a row NOT visible through `v`'s own
qual still updated it. Root cause: `updateOp.updateViaIndex`
(`internal/executor/operators_storage.go`) drives its **initial** B-tree
range-scan purely off the index's own equality key (`ix.Key`) and never
evaluates `o.pred` on that pass — `o.pred` is only consulted later, during an
EPQ recheck after a concurrent modification. `extractScan`'s
`Filter`-wrapping-`IndexScan` combining logic (which correctly folds the
extra predicate into `o.pred`) is therefore silently ineffective for the
*uncontended* case, which is the common one. This is a pre-existing latent
gap independent of this feature (any future planner change that wraps an
`IndexScan` result in a residual `Filter` for UPDATE would hit it too).

**Follow-up (same date, root-cause fix landed):** the workaround (`planUpdate`
skipping the index fast-path whenever `viewQual != nil`) has been replaced by
fixing `updateViaIndex` itself — it now evaluates `o.pred` against each
decoded row immediately after the HOT-chain follow, before building the SET
row, skipping (not erroring on) a non-matching or NULL result exactly like
`scanMatching`'s per-row predicate check. `planUpdate` now takes the index
path unconditionally for `WHERE <indexed-col> = ...` shapes regardless of
`viewQual`, folding the view qual into the same `Filter` layer `extractScan`
already merges with the index's synthesised equality predicate — recovering
the O(log n) index probe for view-target UPDATEs that this loop's original
workaround gave up. `planDelete`'s equivalent `Filter`-wrap never needed a
workaround in the first place: `deleteOp.Next` always drives its scan through
`scanMatching` with the full `o.pred`, regardless of whether the plan carries
an `IndexScan` node — DELETE has no index-driven fast path to bypass.
New regression test `TestUpdatableViewWhereQualEnforcedThroughIndexPath`
(`internal/executor/view_dml_test.go`) asserts both that the planner actually
chooses `Update{Child: Filter{Child: IndexScan}}` for this shape (so the test
can't silently pass via an unrelated fallback path) and that the view qual is
still enforced through it. Gates: `go build ./...`; `go test -race
./internal/executor/...` (touches concurrent UPDATE/EPQ code); full
`./internal/executor/... ./internal/planner/... ./internal/parser/...`;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33). This closes deferred item 4
below.

## Verification

`internal/executor/view_dml_test.go`:
- `TestUpdatableViewInsertUpdateDeleteRewriteToBase` — INSERT/UPDATE/DELETE
  through a plain `SELECT * FROM t` view actually mutate `t`.
- `TestUpdatableViewWhereQualRestrictsUpdateDeleteTargets` — a row outside
  the view's own qual is untouched by UPDATE/DELETE through the view (this
  is the test that caught the `updateViaIndex` bug above).
- `TestInsertViewCheckOptionViolation` / `TestUpdateViewCheckOptionViolation`
  — CHECK OPTION rejects a violating row (`44000`) without writing it, and
  accepts a satisfying one.
- `TestNonUpdatableViewDMLRejected` — an aggregate view and a
  renamed-column view are rejected (`55000`) at plan time for all three DML
  commands, with the base table left untouched — regression coverage for
  the pre-fix silent-corruption behavior across the *rejected* subset too.

Also manually verified end-to-end against a running `goopg` server with
upstream `psql` 18.3 (matching SQLSTATEs via `\set VERBOSITY verbose`).

Gates run: `go build ./...`; `go test ./internal/planner/... ./internal/executor/... ./internal/parser/... ./internal/catalog/... ./internal/server/...`;
`scripts/tpch-spotcheck.sh` (Q12=2/Q13=33); pgbench smoke via the pre-commit
hook.

## Follow-up: view-of-view chaining (closes deferred item 2)

`viewAutoUpdatableChain` (`internal/planner/view_dml.go`, renamed from
`viewAutoUpdatableBase`) now recurses when a simple auto-updatable view's
single `FROM` relation is itself a simple auto-updatable view, walking down
to the ultimate real base relation — PostgreSQL's `rewriteHandler.c` does the
same recursive walk. This required one correction to the original eligibility
check: `catalog.InMemory.CreateView` sets `Table.Virtual = true` on *every*
view (it has no physical heap storage), not just goopg's system-catalog
virtual relations — so the pre-existing single-level check's `b.Virtual`
guard, which happened to also exclude views (since the separate `b.View !=
nil` check already did that job), had to become `b.Virtual && b.View == nil`
to admit a view as a valid intermediate relation while still excluding actual
system catalogs (`pg_class` etc., `Virtual` with no `View`).

At the time this landed, every level's target list was still required to be
an unrenamed, in-order full-column-list passthrough, so column names and
ordinals were identical at every level all the way down to `base` and a chain
level's own `WHERE` resolved directly against `base`. The "Follow-up: column
subset/reorder/rename" section below replaced that restriction with a
per-level column map, and `viewQualOnBase` now resolves each level's own
`WHERE` against whatever its *immediate* `FROM` relation exposes (translated
onto `base`'s physical layout via `viewProxyTable` when that immediate `FROM`
is itself a renaming/reordering/subsetting view), not unconditionally against
`base`.

Two combination rules, both implemented in the new `viewChainQuals`:

- **Row-visibility qual (UPDATE/DELETE), unconditional.** A chained view only
  ever exposes rows visible through *every* level's own `WHERE` — the AND of
  all levels' quals restricts which rows UPDATE/DELETE can touch, independent
  of CHECK OPTION (mirrors PG: each level's SELECT filters its FROM in turn).
- **CHECK OPTION (INSERT/UPDATE only), per rewriteHandler.c's CASCADED/LOCAL
  propagation (~line 3791-3843).** A level is checked if it declares its own
  `CHECK OPTION` (either mode) OR a `CASCADED` check option from an outer
  level has propagated down to it; once triggered, `CASCADED` propagation
  continues through every remaining inner level regardless of that level's
  own setting. A `LOCAL` check option, by contrast, checks only levels that
  themselves declare `CHECK OPTION` — it does not force an unchecked inner
  view to be checked. `checked`'s per-level quals are AND'd together, same as
  `all`. `viewCheckName` remains the outermost (DML-targeted) view's name —
  goopg's `checkViewCheckOption` reports one combined violation rather than
  PostgreSQL's per-level `WithCheckOption.relname`, a minor error-text fidelity
  gap left as-is (out of scope — no behavior difference for the row itself).

New tests in `internal/executor/view_dml_test.go`:
`TestChainedViewInsertUpdateDeleteRewriteToBase` (chain rewrite + combined
row-visibility qual for INSERT/UPDATE/DELETE),
`TestChainedViewCheckOptionCascadeReachesInnerView` (default/explicit
CASCADED forces the inner view's own qual even without its own CHECK
OPTION), `TestChainedViewCheckOptionLocalDoesNotForceInnerCheck` (LOCAL does
not), `TestChainedViewCheckOptionInnerEnforcedRegardlessOfOuter` (an inner
view's own CHECK OPTION is enforced even when the outer view has none),
`TestChainedViewInnerNotAutoUpdatableRejectsWholeChain` (a non-updatable
inner view — e.g. one that aggregates — rejects the whole chain `55000`
rather than silently rewriting past it).

Gates: `go build ./...`; `go test ./internal/planner/... ./internal/executor/... ./internal/catalog/... ./internal/parser/...`;
`scripts/tpch-spotcheck.sh`; pgbench smoke via the pre-commit hook.

## Follow-up: column subset/reorder/rename (closes deferred item 1)

`viewColumnMap` (`internal/planner/view_dml.go`, replacing
`viewTargetsPassthrough`) relaxes the eligibility check from "target list is
an unrenamed, in-order, full-column-list passthrough" to "every target-list
entry is a bare column reference to a column of the `FROM` relation" —
matching PostgreSQL's `view_col_is_auto_updatable` (a view column is
updatable iff its expression is a plain `Var` over the base relation; goopg
additionally requires *every* column to qualify, since it has no
per-column-expression rewrite machinery). Within that restriction, a subset
of the base relation's columns, a reordering, and/or a rename are all now
accepted: the function returns a `colMap []int` (target-list ordinal → `b`'s
column ordinal) instead of a bool. This also let the standalone
`len(tbl.ViewColumnAliases) > 0` gate be deleted outright — an explicit
`CREATE VIEW v (a, b) AS SELECT x, y FROM t` column-name list and a per-target
`SELECT x AS a, y AS b` alias are just two spellings of the same rename
(`execCreateView` already folds either into `catalog.Table.Columns[i].Name`
at `CREATE VIEW` time), so there is no longer a reason to treat them
differently from the eligibility check's point of view.

`viewAutoUpdatableChain` composes each level's own `colMap` down through the
chain (a level's `ownMap[i]` gives its immediate `FROM`'s ordinal; composing
with that `FROM`'s own already-composed map — when it is itself a view —
yields the ordinal in the ultimate `base`), returning `colMaps [][]int`
parallel to `chain`. Two new helpers turn a `colMap` into something
`resolveExpr` can use directly, with no separate query-rewrite pass:

- `viewColumnNames(tbl)` — a view's own output column names, in order
  (already resolved by `CreateView` regardless of which rename spelling was
  used).
- `viewProxyTable(base, names, colMap)` — a synthetic `*catalog.Table` with
  the *exact same physical column count/order/ordinals as `base`* (so a row
  scanned from `base` indexes identically), but with the column at ordinal
  `colMap[i]` renamed to `names[i]`. A `base` column not covered by `colMap`
  (the view selects a strict subset) is left with an empty `Name`, which can
  never match a real `parser.ColumnRef` — hiding it from resolution exactly
  as PostgreSQL does (a view's row type only has its own target-list
  columns).

Passing this proxy — instead of `base` itself — to `singleBindingContext`
lets `planInsert`/`planUpdate`/`planDelete` resolve a DML statement's column
list, `SET`, `WHERE`, and `RETURNING` exactly as before, with no new
resolution code path: `cat.LookupColumn`/`resolveExpr` find columns by name
against the proxy and get back the correct **base** ordinal via each
`catalog.Column`'s `Ordinal` field, which the proxy preserves unchanged. The
plan node's own `Table` field (the real scan/mutation target at execution
time) is always the true `base`, never the proxy — the proxy exists purely
to give planning-time name resolution the view's vocabulary. The same
technique closes the loose end the chaining follow-up left open: each chain
level's own `WHERE` (`viewQualOnBase`) now resolves against a proxy shaped
like its *immediate* `FROM`'s exposure (built from that level's own composed
`colMap`) instead of unconditionally against `base`, so a chain that mixes
renaming at different levels still resolves each level's stored qual
correctly.

For `INSERT` with no explicit column list, the source row's column *i* maps
onto `outerColMap[i]` (the view's own ordinal order) rather than `base`'s
physical order — `INSERT INTO v VALUES (...)` must supply values in the
view's own column order, matching PostgreSQL's `transformInsertStmt`.

Known residual, deliberately out of scope for this pass: `INSERT ... ON
CONFLICT` arbiter-target and `DO UPDATE SET` resolution
(`resolveArbiterIndex`/`planOnConflict`) still resolve against `base`
directly, not through the proxy — targeting a renamed view column in `ON
CONFLICT (...)` or `DO UPDATE SET ...` fails `42703` rather than resolving.
This is a safe failure (a clear error, not silent corruption), and combining
`ON CONFLICT` with a renaming view is a narrow enough case that plumbing the
proxy through arbiter-index resolution was left for a dedicated pass — see
`.ralph/deferral_ledger.md`.

New tests in `internal/executor/view_dml_test.go`:
`TestUpdatableViewColumnSubsetReorderRename` (rename via per-target alias,
rename via explicit `CREATE VIEW` column list, column reorder with a
column-list-free `INSERT`, column subset with both a successful `INSERT`
against the exposed column and a `42703` rejection referencing the hidden
one). `TestNonUpdatableViewDMLRejected`'s renamed-column case (`vren`) was
replaced with an expression-column case (`vexpr`) — renaming a plain column
reference is no longer rejected, but a target-list entry that is an
expression (not a bare column reference) still is.

Gates: `go build ./...`; `go vet ./internal/planner/... ./internal/executor/...`;
`go test ./internal/planner/... ./internal/executor/... ./internal/catalog/... ./internal/parser/... ./internal/server/...`;
`scripts/tpch-spotcheck.sh`; pgbench smoke via the pre-commit hook.

## Follow-up: `UPDATE ... FROM` / `DELETE ... USING` a view (closes deferred item 3)

`planUpdate`/`planDelete` no longer special-case `len(s.From) > 0` /
`len(s.Using) > 0` to an unconditional `viewNotUpdatableError` before even
computing `viewAutoUpdatableChain` — the chain/`colMap`/`viewQual`/
`resolveTbl` computation now runs unconditionally when the target is a view,
and the FROM/USING cross-product branches reuse the same `resolveScope`
(`resolveTbl` when the target was a view, else `tbl`) that the FROM/USING-free
branches already used for `SET`/`WHERE`/`RETURNING` resolution. The view's own
qual (`viewQual`, the AND of every chain level's `WHERE`, translated onto
`base` by `viewChainQuals`) is ANDed into `FromPred`/`UsingPred` so the
cross-product still only matches rows the view itself would expose — the same
restriction the FROM/USING-free path already applies via a `Filter` wrapping
the plain scan. `WITH CHECK OPTION` is likewise threaded through: the `Update`
plan node built in the FROM branch now carries `ViewCheckQual`/`ViewCheckName`
(previously only set in the FROM-free branch), and the executor's
`updateWithFrom` (`internal/executor/operators_storage.go`) gained a
`checkViewCheckOption` call at the same point the pending row's `parentNewRow`
is finalized — gated to `fst.tbl == o.plan.Table` (the exact base relation),
mirroring the identical restriction the FROM-free path's inheritance/partition
branch already has (see deferred item 5 below: a CHECK OPTION view over a
partitioned/inherited base still only enforces on the parent's own rows, not
child-routed ones — unchanged, not fixed by this pass). `DELETE ... USING`
needs no CHECK OPTION wiring — PostgreSQL never enforces it on DELETE.

**A pre-existing, previously-untested bug this surfaced and fixed in the same
pass:** `viewProxyTable`'s synthetic table keeps `base`'s own `Name` (needed
so ordinal-keyed lookups elsewhere — e.g. partition routing — keep working),
not the view's. Every DML form substituting `resolveTbl` in place of a view
(`INSERT`/`UPDATE`/`DELETE`, FROM/USING or not) built its resolve-context
binding with `s.Target.Alias` as the alias — which is empty whenever the
statement gives the view no explicit `AS` alias. With no alias and a proxy
table named after `base`, an unaliased qualified reference to the view by its
*own* name (`UPDATE v SET x = 1 WHERE v.id = 1`, no `AS`) failed to resolve
(`42703`) — the qualifier `v` matched neither the binding's empty alias nor
the proxy's borrowed `base` name. This was latent and untested before this
loop because no existing view-DML test qualified a column reference with the
view's own name; `UPDATE ... FROM` / `DELETE ... USING` needed it to
disambiguate the target from the FROM/USING relation(s) in the same query and
so exposed it immediately. Fixed by a new `viewResolveAlias(explicit,
viewName string) string` helper (`internal/planner/view_dml.go`): returns
`explicit` when the statement supplied one, otherwise the view's own
pre-rewrite name. Applied at all four `resolveTbl`-driven binding sites
(`planInsert`'s `RETURNING` context, `planUpdate`'s FROM and FROM-free
contexts, `planDelete`'s USING and USING-free contexts) — `INSERT`'s `ON
CONFLICT` resolution is unaffected (documented residual, item 1's "Known
residual" note: it already resolves directly against `base`, not through a
proxy, so it was never subject to this bug either).

New test `TestUpdatableViewUpdateFromDeleteUsing`
(`internal/executor/view_dml_test.go`): `UPDATE ... FROM` and `DELETE ...
USING` through a renamed-column, `WHERE`-qualified view rewrite onto `base`
and leave rows outside the view's own qual untouched even when the FROM/USING
table would otherwise match them; `WITH CHECK OPTION` still rejects (`44000`)
an `UPDATE ... FROM` that would produce a row outside the view's qual; an
aggregation view (outside the auto-updatable subset) still rejects both forms
`55000`.

Gates: `go build ./...`; `go vet ./internal/planner/... ./internal/executor/...`;
`go test ./internal/planner/... ./internal/executor/... ./internal/catalog/... ./internal/parser/... ./internal/server/...`;
`scripts/tpch-spotcheck.sh`; pgbench smoke via the pre-commit hook.

## Follow-up: CHECK OPTION on partition/inheritance-child-routed rows (closes deferred item 5)

Both remaining `WITH CHECK OPTION` enforcement call sites gated the check to
the base/parent table's own rows: `updateScanTables`'s per-row callback
(`internal/executor/operators_storage.go`, the plain FROM-free `UPDATE` path)
required `scanTbl == tbl`, and `updateWithFrom`'s FROM cross-product branch
required `fst.tbl == o.plan.Table`. Both gates were unnecessarily
conservative — in both functions a `parentNewRow` (or, for the FROM-free
path's non-inheritance-child branch, `newRow` itself) is already computed in
the *base table's own column ordinal space* before either gate is checked:

- **True inheritance children** (`isInheritChild` / `fst.colMap != nil`):
  `parentNewRow`/`tgtRow` is explicitly built by evaluating `SET` in parent
  column space and only remapped to the child's (possibly reordered) layout
  afterward, via `buildInheritColMap`/`remapChildRowToParent`/
  `remapParentRowToChild` — the exact translation the old comments said was
  missing was, in fact, already sitting one variable away.
- **Partition children**: PostgreSQL requires a partition's columns to
  exactly mirror the partitioned table's layout (`ALTER TABLE` on a partition
  directly cannot add/drop/reorder columns independently of the parent — only
  plain multiple-inheritance children can do that), so a partition-routed
  row's `newRow` is already in the parent's ordinal space with no remap
  needed at all.

Fixed by lifting `parentNewRow` to always be populated (in
`updateScanTables`'s per-row callback, the non-`isInheritChild` branch now
sets `parentNewRow = newRow` since the ordinals already match) and checking
it unconditionally in both functions — CHECK OPTION now enforces on the
parent's own rows, partition-child rows, and inheritance-child rows alike.

New test `TestViewCheckOptionEnforcedOnPartitionAndInheritanceChildRows`
(`internal/executor/view_dml_test.go`) covers a partition-routed row via a
plain `UPDATE` and via `UPDATE ... FROM`, and an inheritance-child row via a
plain `UPDATE` — each confirms the rejected write leaves the child-routed row
untouched and a subsequent in-qual `UPDATE` still succeeds. (The
inheritance-child case deliberately avoids a bare `WHERE <pk> = ...`, which
would route through `updateViaIndex` instead — see the new discovery below.)

**New discovery, not fixed, project-wide and independent of views:**
`updateViaIndex` (`internal/executor/operators_storage.go`) only scans the
exact target table's own B-tree index — it has no partition/inheritance-child
fan-out at all, unlike `updateScanTables`/`updateWithFrom`. So `UPDATE parent
SET ... WHERE indexed_col = X` (no `ONLY`), whenever the planner's
`planIndexScanFromWhere` finds a usable index on `parent` itself, silently
skips any matching row that lives only in an inheritance child's own storage
— not just a CHECK OPTION gap, the actual write never happens. This is
orthogonal to the CHECK OPTION fix above: it reproduces for *any* UPDATE
(view or not) with an index-eligible WHERE clause over an inheritance
hierarchy, and is unaffected by partitioning (a partitioned parent has no
per-parent index of its own, so `planIndexScanFromWhere` never matches it,
always falling through to `updateScanTables`, which DOES fan out — this is
why the partition case in the new test above needed no such caveat while the
inheritance case did). See the deferral ledger for the resume point.

Gates: `go build ./...`; `go vet ./internal/planner/... ./internal/executor/...`;
`go test ./internal/planner/... ./internal/executor/... ./internal/catalog/... ./internal/parser/... ./internal/server/...`;
`scripts/tpch-spotcheck.sh`; pgbench smoke via the pre-commit hook.

## Deferred

See `.ralph/deferral_ledger.md` for the formal entry. Summary:

1. ~~**Column subset/reorder/rename.**~~ — **RESOLVED** (see the "Follow-up:
   column subset/reorder/rename" note above): `viewColumnMap` +
   `viewProxyTable` replace the `tbl.Columns == base.Columns` positional-
   identity requirement with a per-column ordinal map. `INSERT ... ON
   CONFLICT` against a renamed view column remains a narrower open residual
   (see that section).
2. ~~**View-of-view chaining.**~~ — **RESOLVED** (see the "Follow-up" note
   above): `viewAutoUpdatableChain` recurses through a chain of simple
   updatable views, and CASCADED/LOCAL CHECK OPTION propagation now actually
   matters (previously moot, single-relation only).
3. ~~**`UPDATE ... FROM` / `DELETE ... USING` a view.**~~ — **RESOLVED** (see
   the "Follow-up: `UPDATE ... FROM` / `DELETE ... USING` a view" note above):
   the view qual is now threaded through the cross-product path
   (`FromPred`/`UsingPred`), and `WITH CHECK OPTION` is enforced for
   `UPDATE ... FROM` (restricted to the base relation's own rows, same as the
   FROM-free path — see item 5).
4. ~~**`updateViaIndex` residual-predicate gap**~~ — **RESOLVED** (see the
   "Follow-up" note above): `updateViaIndex` now evaluates `o.pred` on its
   initial scan, not just the EPQ recheck; the `planUpdate` workaround that
   skipped the index path for view-target UPDATEs is removed.
5. ~~**CHECK OPTION on partition/inheritance-child-routed rows.**~~ —
   **RESOLVED** (see the "Follow-up: CHECK OPTION on partition/
   inheritance-child-routed rows" note above): both `updateScanTables` and
   `updateWithFrom` now check `parentNewRow`, always in the base table's
   column ordinal space regardless of which child the row was routed
   through.
6. **Restart persistence.** Unaffected by this change — the pre-existing
   in-memory-only view/catalog limitation (`0003-0009-views.md`).
7. **`updateViaIndex` has no partition/inheritance-child fan-out at all
   (new discovery, project-wide, not view-specific).** See the "New
   discovery" note above and the matching deferral ledger row — a
   project-wide gap, out of scope for a view-focused loop to fix at its
   root, root-0025 itself is otherwise fully closed by item 5's resolution
   plus items 1-4 above (only the narrow `ON CONFLICT`-against-renamed-view
   residual from item 1 remains open within this milestone's own scope).

## Cross-references

- Views overview: [0003-0009-views.md](0003-0009-views.md).
- CHECK OPTION dump fidelity (predecessor): DU-002 slice 365 in
  `.ralph/deferral_ledger.md`.
