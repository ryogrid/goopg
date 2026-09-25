# M0142-0003f — adding the TPC-H FK set: 11/16 landed, the remaining 5 are blocked by an unindexed, uncancellable validation scan

Status: implemented (2026-09-16 — partial at close, 11/16 FK constraints;
the remaining 5 landed under M0142-0003i's 8-FK canonical-set reload).

## Task

Filed by M0142-0003e: add the canonical 8-constraint TPC-H FK set to goopg's
`:65433` bench cluster (currently zero FK constraints — see -0003e) so it
matches the PG 18.3 oracle at `:65432`, then re-measure Q9's `EXPLAIN`/
estimate-audit to see whether the `lineitem ⋈ partsupp` 2500x row-estimate
collapse goes away.

## Method and what happened

1. Confirmed the PG oracle's exact FK set via `pg_get_constraintdef` (8 rows,
   names/columns as recorded in -0003e) and confirmed all 8 are backed by
   `PRIMARY KEY` constraints (`contype='p'`), not bare unique indexes — PG's
   `pg_constraint` has both the 8 PKs and the 8 FKs.
2. Checked goopg's `:65433` cluster: `pg_constraint` was empty (0 rows), but
   the 8 PK-equivalent unique indexes already existed under PG's exact PK
   constraint names (`customer_pk`, `lineitem_pk`, …) — HammerDB's goopg-side
   loader created them as bare `CREATE UNIQUE INDEX`, not as registered
   constraints. goopg supports adopting an existing unique index as a PK
   without a rebuild: `ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING
   INDEX t_pk` (`internal/executor/operators_ddl.go:12207`
   `execAlterTableAddPrimaryKeyUsingIndex`, PG's
   `tablecmds.c:ATExecAddIndexConstraint`), so registering the 8 PKs this way
   was cheap (index reuse, no scan beyond the NOT-NULL-compatibility check).
3. Sent all 16 target DDL statements (8 `ADD CONSTRAINT ... PRIMARY KEY USING
   INDEX`, then 8 `ADD CONSTRAINT ... FOREIGN KEY`, ending `nation_region_fk`
   → `customer_nation_fk` → `supplier_nation_fk` → `partsupp_part_fk` → …) in
   one `psql` session wrapped in `BEGIN; … ROLLBACK;`, intending a dry run
   before touching the live shared cluster for real. The session hung past
   its 900s foreground timeout while on the 12th statement
   (`partsupp_part_fk`) and was moved to background; a background check
   showed it still running.
4. Killed the **client** (`kill -TERM` on the `psql`/wrapper-bash PIDs found
   via `ps aux`) rather than let it run unbounded on the shared cluster.
   **A fresh session then showed 11 rows in `pg_constraint`** — the 8 PKs and
   the 3 FKs with small parent tables (`nation_region_fk`: parent `region`,
   25 rows; `customer_nation_fk` / `supplier_nation_fk`: parent `nation`, 25
   rows) had already committed **despite the intended `ROLLBACK` never being
   reached** — the client was killed mid-script, before the `ROLLBACK`
   statement. These 11 constraints are correct (they mirror the PG oracle's
   names/columns exactly) and are now **permanent, validated state on the
   live `:65433` bench cluster** — this was not reverted; see "Disposition"
   below for why keeping them was the right call.
5. `pg_stat_activity` on a fresh connection showed backend PID 81 **still
   actively running** the 12th statement
   (`ALTER TABLE partsupp ADD CONSTRAINT partsupp_part_fk FOREIGN KEY
   (ps_partkey) REFERENCES part(p_partkey);`) — i.e. the server did not aborp
   the in-flight statement when the client TCP connection was killed.
   `SELECT pg_terminate_backend(81)` returned `t` (accepted) but the backend
   was **still shown active running the identical query 15+ seconds later**,
   CPU climbing — termination is not honoured once this scan starts.
   Confirmed the stuck backend does not block ordinary reads
   (`SELECT count(*) FROM partsupp`/`lineitem` both returned instantly from a
   separate session), so it was left running rather than forcing a disruptive
   server restart on the shared cluster (see "Disposition").

## Root-cause finding — why `partsupp_part_fk` (and the 2 lineitem-scale FKs)
## did not complete in reasonable time

Dispatched a read-only recon subagent to trace the FK-add executor path
(`internal/executor/operators_ddl.go:9033`
`case parser.AlterTableAddForeignKey`) end to end. Decisive findings, all
cited to file:line:

- **No index is used to check whether a referenced key exists**, at any
  scale. `validateFKConstraintExistingRows`
  (`operators_ddl.go:13974`/`13995`) does a full block-by-block heap scan of
  the *child* table, and for every live row calls `assertParentExists`
  (`internal/executor/operators_fk.go:632`) →
  `scanTableForMatchFKWait`/`scanRelForFKMatch`
  (`operators_fk.go:1318-1412`), which does a full block-by-block heap scan
  of the *parent* table via `ctx.Pool.Pin`/`storage.PageGetHeapTuple` — never
  consulting `partsupp_pk`/`part_pk`/any B-tree, even though those unique
  indexes exist on the exact referenced columns. This is an **O(child rows ×
  average parent-scan distance) unindexed nested-loop**, not PG's
  index-accelerated validation scan.
  - `nation_region_fk`/`customer_nation_fk`/`supplier_nation_fk` completed
    fast purely because their parent table (`nation`/`region`) has 25 rows —
    the unindexed scan is cheap when the *parent* is tiny, regardless of the
    child's size.
  - `partsupp_part_fk` (child `partsupp`, 800,000 rows; parent `part`,
    200,000 rows) did not complete inside several minutes.
    `lineitem_partsupp_fk` (child `lineitem`, 6,000,000 rows; parent
    `partsupp`, 800,000 rows) and `lineitem_order_fk` (child `lineitem`,
    6,000,000 rows; parent `orders`, 1,500,000 rows) are two to three orders
    of magnitude larger still and were never attempted for real — the dry
    run was killed before reaching either.
- **The scan does not check for backend termination/interrupt signals.**
  `pg_terminate_backend` returning `t` normally means the signal was
  delivered; PG's own long scans call `CHECK_FOR_INTERRUPTS()` periodically
  and abort promptly. goopg's `scanRelForFKMatch`/
  `validateFKConstraintExistingRowsRel` loop apparently has no equivalent
  check — the backend kept running, CPU climbing, 15+ seconds after
  termination was requested, with no sign of stopping. This makes the first
  finding materially worse: even a mis-sized FK add on this engine cannot be
  cancelled once started; the only way out is killing the whole server
  process, which is destructive on a cluster other loops depend on.

Neither finding is specific to TPC-H; both are properties of
`internal/executor/operators_ddl.go`'s FK-add path and
`internal/executor/operators_fk.go`'s existing-row scan, and would reproduce
on any table pair of comparable size.

## Secondary finding — `ADD CONSTRAINT` inside an explicit transaction did not roll back

The 11 landed constraints committed even though the driving script's
`ROLLBACK` was never reached (the client was killed first). This means DDL
issued after an explicit `BEGIN` on this cluster did not wait for `COMMIT`/
`ROLLBACK` to take effect — consistent with the already-documented
architecture split between `pg_class` heap-append and goopg-private
WAL+replay for catalog DDL (`goopg_catalog_ddl_durability_two_mechanisms` in
memory), but this loop did not trace the exact commit point and cannot rule
out a narrower bug (e.g. each DDL statement force-committing its own
sub-transaction regardless of an open explicit one). Recorded as a smaller,
separate deferral (below) rather than conflated with the FK-scan finding —
the resume point is a targeted repro (`BEGIN; ALTER TABLE …; ROLLBACK;` on a
throwaway table, checked from a second session before vs. after the
`ROLLBACK`), not yet done here.

## Disposition of the live shared cluster (`:65433`)

- **Kept, not reverted**: the 8 `PRIMARY KEY USING INDEX` adoptions and the 3
  small-parent FKs (`nation_region_fk`, `customer_nation_fk`,
  `supplier_nation_fk`). All 11 exactly match the PG oracle's names/columns
  (verified name-for-name against §"Method" step 1) and are real, validated
  constraints — reverting correct, PG-matching state to chase a clean rerun
  would destroy true progress for no benefit, and (per the uncancellable-scan
  finding above) there is no cheap way to undo them short of `DROP
  CONSTRAINT`, which is itself unvalidated-scan-free and safe to do later if
  ever needed.
- **Left running, not killed**: backend PID 81's `partsupp_part_fk` add.
  `pg_terminate_backend` did not stop it and it does not block ordinary
  reads on `partsupp`/`lineitem`, so forcing it off would require restarting
  the whole shared server — disruptive to any other loop using this cluster,
  for a CPU-only (not memory-risk) cost. If it eventually completes on its
  own, `partsupp_part_fk` lands as a 12th correct constraint "for free"; if
  not, it is harmless idle background load. The next loop should re-check
  `pg_constraint` count/`pg_stat_activity` before assuming this state is
  unchanged.
- **Not attempted**: `partsupp_supplier_fk`, `order_customer_fk`,
  `lineitem_partsupp_fk`, `lineitem_order_fk` — the last two are exactly the
  FKs Q9's `lineitem ⋈ partsupp` collapse (-0003d/-0003e) needs, so the Q9
  re-measurement this task was filed to unblock **remains blocked**.

## What this does NOT establish

- Whether `partsupp_part_fk`'s scan will ever complete, and how long
  `lineitem_partsupp_fk`/`lineitem_order_fk` would take if index-accelerated
  validation existed — not measured, would need the fix in M0142-0003g first.
- Whether the DDL-inside-explicit-transaction non-rollback is a real bug or
  intentional per the catalog-DDL-durability split — not traced to a root
  cause this loop (see "Secondary finding").
- Q9's plan shape/cost with the full FK set present — still unmeasured; the
  one FK that matters for Q9 (`lineitem_partsupp_fk`) is exactly the one
  blocked.

## Resolution

M0142-0003f is **partially landed, not complete**: 11/16 constraints are
real, permanent, PG-matching state on the shared bench cluster; the remaining
5 are blocked by a newly-discovered engine gap in FK-constraint validation
(unindexed nested-loop scan, not interrupt-checked), filed as
**M0142-0003g** below. The Q9 re-measurement this task exists to unblock is
deferred to after -0003g lands (or after a scoped, index-accelerated partial
fix that at minimum handles `lineitem_partsupp_fk`).

## Cross-references

- `.ralph/fix_plan.md` M0142-0003e/f/g
- `internal/executor/operators_ddl.go:9033` (`AlterTableAddForeignKey`
  dispatch), `:12089`/`:12207` (`execAlterTableAddPrimaryKey`/
  `execAlterTableAddPrimaryKeyUsingIndex`), `:13974`/`:13995`
  (`validateFKConstraintExistingRows`/`validateFKConstraintExistingRowsRel`)
- `internal/executor/operators_fk.go:632` (`assertParentExists`), `:1318`
  (`scanRelForFKMatch`)
- `internal/catalog/catalog.go:1656` (`ForeignKey`)
- `docs/design/0100-0149/m0142-0003e-bench-cluster-missing-fk-constraints.md`
  (prior task in this series)
- PG upstream reference: `postgres/src/backend/commands/tablecmds.c:13694`
  `validateForeignKeyConstraint` — tries `RI_Initial_Check` first (a single
  planner-optimized `LEFT JOIN` query, index-accelerated like any other
  query); if that is not usable it falls back to `table_beginscan` (a
  **single** heap scan of the *child* table only, `tablecmds.c:13741`) and,
  per row, calls the RI trigger, which probes the *parent* via its declared
  unique index (not a parent heap scan) — so even PG's slow-path fallback is
  O(child rows) index probes, not goopg's O(child rows × parent scan
  distance). The per-row loop also calls `CHECK_FOR_INTERRUPTS()`
  (`tablecmds.c:13751`), which is the mechanism goopg's scan needs to gain
  for cancellability.
