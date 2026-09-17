# P0-E4 — reproducing the catalog-xmax/commit-status loss (2026-09-17)

Status: landed (repro + regression tests). Fix is **P0-E5** (not yet done).

## Background

The 2026-09-16 shared TPC-H cluster `:65433` lost its `tpch` database's
catalog rows after an uncommitted `ALTER TABLE … ADD CONSTRAINT … PRIMARY KEY
USING INDEX` transaction was stopped with `goopg stop -mode immediate`. The
incident is diagnosed in full in
`tmp/METHODLOGY3_RALPH_CHECK0917/03-new-problems.md` §2 and
`04-actions.md` §2 (owner-written, 2026-09-17). This task (`P0-E4` in
`.ralph/fix_plan.md`) is the "reproduce it on a throwaway cluster" step that
report's §2.2 asked for but had not yet executed end-to-end.

## Root cause (confirmed by code reading, now also confirmed live)

Two independent bugs, both required to close together as **P0-E5**:

1. **Disk side — the loader ignores commit status.** Any DDL that replaces a
   catalog row in place goes through `deleteCatalogRowsForOID`
   (`internal/executor/operators_ddl.go:18186`), which calls
   `stampCatalogRowsTuple` (`:18121`) to set the OLD row's `xmax` **and** the
   `XmaxCommitted` hint bit — before the owning transaction commits (see the
   `DU-002` comment at `:18152-18161` explaining why the hint is set eagerly).
   `finishPrimaryKeyConstraint` (`:12252`) is one caller: adopting an existing
   `UNIQUE INDEX` as a table's primary key via
   `ALTER TABLE t ADD CONSTRAINT … PRIMARY KEY USING INDEX …` rewrites the
   table's `pg_class`/`pg_attribute` rows this way.

   At startup, the catalog loader
   (`internal/initdb/catalog_heap_reload.go`) reconstructs the in-memory
   catalog straight from the heap files via `scanCatalogHeapRows` (:75) and
   `catalogRowLive` (:43). Both **unconditionally** discard any row whose
   `Xmax != InvalidTransactionID` — `scanCatalogHeapRows`'s own pre-filter
   (`:86-89`) never even reaches `catalogRowLive`'s (dead, for this caller)
   xmax check. Neither checks CLOG for whether that `xmax` transaction
   actually **committed**. So after a restart:
   - the NEW row is correctly dropped (its `xmin`'s transaction aborted —
     correct outcome), but
   - the OLD row is **also** dropped, purely because it carries a non-zero
     `xmax`, regardless of the fact that the `xmax`-owning transaction never
     committed.

   Net effect: the table's catalog rows vanish from every subsequent reload,
   even though the ALTER TABLE never committed. The heap **data file**
   itself is untouched — this is a catalog-only loss.

2. **In-memory side — `ProcessRollbackUndos` doesn't undo the xmax stamp.**
   `internal/executor/operators_tx.go:388` only restores
   `CREATE`/`TRUNCATE`/sequence undo entries; there's no undo entry type for
   `stampCatalogRowsTuple`'s effect (this is the disk analogue of
   **M0143-0008**, "ALTER TABLE not undone on ROLLBACK", which is the
   in-memory manifestation of the same missing-undo problem). Fixing only the
   loader (bug 1) without fixing bug 2 leaves the live (pre-restart) catalog
   and the reloaded (post-restart) catalog disagreeing about whether the
   ALTER happened — which is exactly why P0-E5 must land both together.

## Repro (executed on a throwaway `55xx`-class cluster, 2026-09-17)

Per fix_plan's repro recipe: inside a `CREATE DATABASE r; \c r` session (the
default `postgres` database's tables are additionally served from an
in-memory JSON catalog cache — M0114 — that bypasses the heap loader entirely
and only invalidates on a DDL **commit**, so an aborted DDL there would never
reach the loader and would false-negative):

```sql
CREATE TABLE t(id int NOT NULL);
CREATE UNIQUE INDEX t_pk ON t(id);
-- (a) / (b) / (c) below, then restart
```

Three distinct abort paths, each followed by a restart and `\d t` /
`SELECT count(*) FROM t` / relation-file-presence check:

| case | how the transaction aborted | `to_regclass('t')` after restart | `SELECT count(*) FROM t` after restart | heap file for `t`'s `relfilenode` |
|---|---|---|---|---|
| (a) | `BEGIN; ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk; ROLLBACK;` (explicit ROLLBACK, same session) | NULL | `ERROR: relation "t" does not exist (42P01)` | **present** |
| (b) | same `BEGIN`+`ALTER`, then the **client is killed** (SIGKILL) without ever sending ROLLBACK or COMMIT | NULL | `ERROR: relation "t" does not exist (42P01)` | **present** |
| (c) | same `BEGIN`+`ALTER` left open, then `goopg stop -mode immediate` (the incident's own shape) → restart | NULL | `ERROR: relation "t" does not exist (42P01)` | **present** |

**All three cases lose the table identically.** This matches the code
diagnosis exactly: the loss mechanism is the loader's unconditional
`Xmax != Invalid` filter, which fires the same way regardless of *how* the
`ALTER`'s transaction was aborted — an explicit client `ROLLBACK` is
sufficient on its own; the incident's `-mode immediate` stop is not a
distinguishing factor, just the specific trigger that happened to occur in
production. In every case the underlying heap data file (found via
`pg_class.relfilenode` → `base/<dbOid>/<relfilenode>`) is untouched; only the
catalog rows that describe it disappear.

## Deliverable: regression tests

`internal/testport/p0e4_catalog_xmax_loss_test.go` — three tests, one per
case above (`TestPort_P0E4CatalogXmaxRollback`,
`TestPort_P0E4CatalogXmaxClientKill`,
`TestPort_P0E4CatalogXmaxServerImmediateStop`), built on the
`cluster.New`/`StartPSQL`/two-handles-one-server pattern already used by
`TestE2E_PGColdStartOnGoopgDataDir` and `TestE2E_PGCrashStartOnGoopgDataDir`.
Each asserts the table and its row are still reachable, and the heap file is
still present, after the restart — i.e. they encode the **fixed** (P0-E5)
behavior and currently fail without it. All three carry a `t.Skip` naming
P0-E5 so they do not fail the unit gate before the fix lands (verified: the
skip makes `go test -run TestPort_P0E4 ./internal/testport/` a clean PASS
today, and temporarily removing the three `t.Skip` calls reproduces the FAIL
shown in the table above for all three, exactly as expected — see the git
history of this file for that verification run's console output, not
committed). **P0-E5 removes the three `t.Skip` calls as part of landing the
fix — these tests are that task's regression lock.**

## Resume point for P0-E5

- Disk side: `scanCatalogHeapRows` (`internal/initdb/catalog_heap_reload.go:75`)
  needs to resolve a non-zero `Xmax`'s commit status via CLOG (subtransactions
  resolved to their parent, PG's `TransactionIdDidCommit`/
  `HeapTupleSatisfiesMVCC`-style rule) instead of treating any non-zero
  `Xmax` as dead outright. A row whose `xmax` transaction **aborted** (or is
  unknown/in-flight at a crash boundary — see `internal/initdb/open.go`
  ~1330-1350's "uncommitted XIDs are treated as aborted at startup" rule)
  must stay live.
- The `XmaxCommitted` hint bit (`stampCatalogRowsTuple`,
  `operators_ddl.go:18121`) must only be set once commit is confirmed (PG's
  `SetHintBits` rule) — verify runtime visibility (`operators_ddl.go`'s own
  `DU-002` comment at `:18152-18161`) doesn't regress when the hint is set
  later.
- In-memory side: M0143-0008 (ALTER rollback-undo) — tick it in the same
  commit as this task's fix_plan entry (P0-E5) says.
- Once fixed, remove the three `t.Skip` calls in
  `internal/testport/p0e4_catalog_xmax_loss_test.go` and confirm all three
  pass.

## Gates

Recon/test-only task (`C1`: repro + tests only, no `internal/`/`cmd/`
production diff). `go build ./...` and `go vet ./internal/testport/` clean;
`go test -run TestPort_P0E4 ./internal/testport/` PASS (all three SKIP as
designed). No planner/executor/statistics production code changed, so
`tpch-spotcheck.sh`/`tpcds-sf025-regression.sh sweep`/`pg-plan-parity-diff.py`
are not required gates for this task (per AGENT.md's Plan-parity harness G
table — those gates apply to production planner/executor/statistics
changes).
