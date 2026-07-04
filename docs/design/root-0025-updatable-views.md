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

## Deferred

See `.ralph/deferral_ledger.md` for the formal entry. Summary:

1. **Column subset/reorder/rename.** PG's full per-column attribute map
   (a view exposing a renamed/reordered/subset of the base table's columns)
   is rejected here (`55000`) rather than rewritten. Needs a `colMap []int`
   threaded through `Set`/`Returning`/row-assembly instead of relying on
   `tbl.Columns == base.Columns` positional identity.
2. **View-of-view chaining.** A simple view defined `FROM` another simple
   view is rejected; PG recurses. Needs the CASCADED/LOCAL distinction to
   actually matter (currently moot, single-relation only).
3. **`UPDATE ... FROM` / `DELETE ... USING` a view.** Rejected
   unconditionally regardless of whether the base view would otherwise
   qualify. Needs the view qual threaded through the cross-product path
   (`FromPred`/`UsingPred`) instead of a plain `Filter`.
4. ~~**`updateViaIndex` residual-predicate gap**~~ — **RESOLVED** (see the
   "Follow-up" note above): `updateViaIndex` now evaluates `o.pred` on its
   initial scan, not just the EPQ recheck; the `planUpdate` workaround that
   skipped the index path for view-target UPDATEs is removed.
5. **CHECK OPTION on partition/inheritance-child-routed rows.** The runtime
   check is skipped for rows reached via `updateScanTables`'s partition/
   inheritance-child branch (their `captureCols` can be reordered relative
   to the base table `ViewCheckQual` was resolved against) — a CHECK OPTION
   view over a partitioned/inherited base table only enforces on the
   parent's own rows.
6. **Restart persistence.** Unaffected by this change — the pre-existing
   in-memory-only view/catalog limitation (`0003-0009-views.md`).

## Cross-references

- Views overview: [0003-0009-views.md](0003-0009-views.md).
- CHECK OPTION dump fidelity (predecessor): DU-002 slice 365 in
  `.ralph/deferral_ledger.md`.
