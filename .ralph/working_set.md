# Working set — M0134-0004 Bucket 4 landed, cluster.sql PARKED; pick the next M0134 case

**Task:** M0134-0004 (`cluster.sql`) — **Bucket 4 LANDED, case PARKED
2026-08-18** (commit `821cd17c`). Selected per the Current Priority banner
(M0134 next after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` is
still run `20260817-011734`, all 6 filed and `[x]` — nothing new to file.

**What landed.** `CREATE TABLE` never recorded the creating role in
`catalog.Table.Owner`, so the owner-shortcut in `dmlPrivilegePermittedAs`
(`internal/executor/operators_storage.go:2222-2242`) and `maintenancePermitted`
(`internal/executor/operators_vacuum.go:333-342`) could never fire and a
non-superuser got a false 42501 on its own table. A **sibling-path omission** —
`execCreateView` (`operators_ddl.go:5391`) already stamped it. Fixed at **three**
independent construction sites: `execCreateTable`, `execCreateTableAs`, and
`execCreatePartitionChild` (the third was missed by the first brief — `PARTITION
OF` returns early into its own top-level function; the implementer caught it and
I widened the brief in round 2 rather than ship a partial fix). Assignment is
`if o.ctx.NonSuperuserRole != "" { tbl.Owner = … }` — deliberately **not**
`currentDDLOwnerName()`, which returns literal `"postgres"` and would break the
`""`-means-bootstrap-superuser sentinel (`catalog.go:611-621`).

**Safety was verified, not assumed** (do not re-derive): an exhaustive
`find_referencing_symbols` pass over `Owner` found no read site where empty means
allow-everyone — `HasTablePrivilege` is default-deny
(`catalog.go:16351-16366`) and the only allow-on-empty branches key on the
*session's* role. Pinned by `TestCreateTableOwnerNegativeGuardDeniesOtherRole`,
which passes both pre- and post-change.

**Why cluster.sql is parked.** 5747 lines (5223 `+` / 257 `-`), ~4900 of them
Bucket 1: `CLUSTER` is a no-op stub (`internal/executor/operators_cluster.go:1-97`
flips `pg_index.indisclustered`, never rewrites the heap) so every SELECT after
the first CLUSTER diverges on row order — VACUUM-FULL-scale, not a slice.
Buckets 2/3/5 are bounded but **inert** behind it; Bucket 6 is **inferred, not
verified**. **Re-arm trigger:** a real CLUSTER milestone (no design doc exists —
writing one is step 1), then re-measure from scratch.

**Files:** `internal/executor/operators_ddl.go` (3 sites),
`internal/executor/create_table_owner_test.go` (5 guards, new),
`docs/design/0134-0004-cluster-sql-divergence.md` (new), `docs/design/README.md`,
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (3 rows 2026-08-18).

**Next step:** 0001-0004 are all parked, so select the **next unparked M0134
case**: M0134-0005 (`constraints.sql`), then 0006 onward. Also newly filed and
selectable: **M0134-0004-a** (CREATE DATABASE ... TEMPLATE drops table owners,
`internal/postmaster/database_ddl.go:917-923`) — a small, well-specified
follow-up if a short loop is wanted. Start any case with
`scripts/pg-regress-runner.sh <case>` + a researcher classification pass before
briefing an implementer. **Never compare to a pre-2026-08-18 regress number** —
they predate the C19 harness fix (`-v HIDE_TABLEAM=on
-v HIDE_TOAST_COMPRESSION=on`); re-measure from scratch.

**Gates run:** `go test ./internal/executor/` PASS; `go test ./internal/catalog/`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (both
rounds); 5 named guards re-run by coordinator pre-commit PASS; pre-commit pgbench
smoke PASS (TPC-B 336 tps, simple update 635 tps, select-only 12.6k tps). No
TPC-H/TPC-DS — no planner/codec change.

**Delegation:** `tmp/ralph-handoffs/m0134-0004-s01-measure/` (researcher
`a8907815f4943e613`, 2 rounds — round 2 was the Owner blast-radius probe that
de-risked the fix); `tmp/ralph-handoffs/m0134-0004-s02-create-table-owner/`
(implementer `ac8651874d73574a7`, 2 rounds, DONE — note: the harness blocks
worker `report.md` writes, so its findings live only in the commit message,
design doc, and ledger).

**In-flight:** none.
