# Milestone 0118 — Upstream Isolation Spec Suite Pass-Through (post REPEATABLE READ + SERIALIZABLE)

**Status:** planned
**Filed:** 2026-06-20
**Depends on:** M0100 (READ COMMITTED isolation suite — runtime correctness, accepted), M0104 (SERIALIZABLE isolation via SSI anomaly prevention)
**Reference plan:** `.ralph/fix_plan.md` (M0118 section)

## Goal

Now that goopg implements REPEATABLE READ (transaction-level pinned snapshot) and
SERIALIZABLE (RR snapshot + real SSI raising SQLSTATE `40001`) in addition to
READ COMMITTED, the **entire** upstream PostgreSQL isolation spec suite is in
scope. Previously 99 of the 121 specs were parked as `not-tried` because only
READ COMMITTED was supported; they have been re-classified to `failed`
(in-scope-but-not-yet-passing) in
`docs/test-port/postgres-oracle-target-inventory.csv`.

This milestone drives the **112 targeted isolation specs** (13 previously
attempted/blocked + 99 newly in scope) to `pass` against the vanilla PostgreSQL
18.3 oracle (`./postgres/local_install`), using the existing multi-session
isolation runner (`internal/testport/framework/isolation_runner.go`). The 9 specs
already passing must stay green.

## In Scope

Decomposed into thematic slices (see `.ralph/fix_plan.md` M0118 sub-tasks). Each
slice ports/fixes the goopg behavior its specs exercise and promotes the CSV rows
to `pass`:

1. **SERIALIZABLE / SSI anomaly specs** — write-skew and dangerous-structure
   rejection with `40001`: `simple-write-skew`, `matview-write-skew`,
   `read-only-anomaly{,-2,-3}`, `read-write-unique{,-2,-3,-4}`, `two-ids`,
   `total-cash`, `receipt-report`, `project-manager`, `classroom-scheduling`,
   `multiple-row-versions`, `update-conflict-out`, `serializable-parallel{,-2,-3}`.
2. **Predicate-lock granularity specs** — per access method / scan type:
   `predicate-gin`, `predicate-gist`, `predicate-hash`,
   `predicate-lock-hot-tuple`, `index-only-scan`, `index-only-bitmapscan`,
   `partial-index`.
3. **Row-locking / SELECT FOR UPDATE/SHARE / SKIP LOCKED / NOWAIT** —
   `skip-locked{,-2,-3,-4}`, `nowait{,-2,-3,-4,-5}`, `lock-nowait`,
   `tuplelock-{conflict,partition,update,upgrade-no-deadlock}`,
   `lock-update-{delete,traversal}`, `update-locked-tuple`,
   `propagate-lock-delete`, `lock-committed-keyupdate`.
4. **Deadlock detection** — `deadlock-{hard,simple,soft,soft-2,parallel}`,
   `multixact-no-deadlock`.
5. **FK / referential-integrity concurrency** — `fk-contention`,
   `fk-deadlock{,2}`, `fk-partitioned-{1,2}`, `referential-integrity`,
   `ri-trigger`, `temporal-range-integrity`.
6. **MERGE & INSERT ON CONFLICT output parity** — `merge-{update,delete,
   insert-update,match-recheck,join}`, `insert-conflict-do-update-{2,3,4}`,
   `insert-conflict-specconflict`, `insert-conflict-do-nothing-2`.
7. **Planner / output-format blockers (previously `failed`)** —
   `eval-plan-qual` (RETURNING support in planner), `drop-index-concurrently-1`
   (EXPLAIN EXECUTE plan-format parity).
8. **DDL / VACUUM / maintenance concurrency** — `alter-table-{1,2,3,4}`,
   `detach-partition-concurrently-{1,2,3,4}`, `partition-concurrent-attach`,
   `partition-drop-index-locking`, `reindex-concurrently{,-toast}`,
   `reindex-schema`, `multiple-cic`, `vacuum-{concurrent-drop,conflict,
   no-cleanup-lock,skip-locked}`, `truncate-conflict`, `sequence-ddl`,
   `cluster-conflict{,-partition}`, `create-trigger`, `inherit-temp`,
   `plpgsql-toast`.
9. **Misc / system-level** — `async-notify`, `timeouts`, `stats`, `horizons`,
   `freeze-the-dead`, `inplace-inval`, `intra-grant-inplace{,-db}`,
   `subxid-overflow`, `prepared-transactions{,-cic}`, `temp-schema-cleanup`,
   `multixact-no-forget`, `aborted-keyrevoke`, `delete-abort-savept{,-2}`.

## Out of Scope

- Changing any PostgreSQL behavior to match goopg — divergence from the PG 18.3
  oracle is always a goopg bug to fix in goopg.
- Non-isolation suites (regress, TAP, modules, contrib) — tracked elsewhere.
- New isolation-level *semantics* beyond what M0100/M0104 delivered; this
  milestone is execution-porting + closing the per-spec feature gaps the specs
  surface. Where a spec needs a genuinely new subsystem (e.g. a missing access
  method's predicate locks), that sub-task may stage with a deferral-ledger entry
  and a resume point rather than block the rest.

## Definition of Done

1. Each targeted isolation spec runs green via its `TestPort_Isolation*` test in
   `internal/testport`, normalized against PG 18.3.
2. Each promoted spec's row in `postgres-oracle-target-inventory.csv` is set to
   `status=pass` with the Go test function name in the rationale.
3. The generated docs are regenerated and consistent:
   `go run ./cmd/gen-isolation-coverage --repo-root .` and
   `go run ./cmd/gen-oracle-inventory --repo-root .`.
4. The 9 isolation specs already passing remain green (no regression).
5. Full unit suite passes with `-race`; the TPC-H spot-check gate
   (`scripts/tpch-spotcheck.sh`) and pgbench pre-commit gate remain green for any
   slice that touches `internal/executor` or `internal/planner`.

## Required Design Docs

Under `docs/design/` (added per slice as work begins):

- `0118-0001-isolation-spec-port-strategy.md` — runner usage, output
  normalization, and the per-slice promotion workflow (CSV → `pass`, regenerate
  coverage docs).

## PostgreSQL References

This milestone validates goopg against, and follows the model of:

- `postgres/src/test/isolation/` (`isolationtester.c`, `specs/*.spec`,
  `expected/*.out`) — the oracle for output and scheduling.
- `postgres/src/backend/storage/lmgr/predicate.c` and
  `postgres/src/include/storage/predicate*.h` — SSI predicate locks + rw-conflict
  detection (slices 1–2).
- `postgres/src/backend/storage/lmgr/lock.c`, `proc.c`, `deadlock.c` — row
  locking, NOWAIT/SKIP LOCKED, deadlock detection (slices 3–4).
- `postgres/src/backend/executor/nodeModifyTable.c`
  (`ExecMerge`/`ExecModifyTable`) — MERGE / ON CONFLICT / EvalPlanQual (slices 6–7).
