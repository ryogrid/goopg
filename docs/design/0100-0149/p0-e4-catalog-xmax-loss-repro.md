# P0-E4 — reproducing the catalog-xmax/commit-status loss (2026-09-17)

Status: landed (repro + regression tests). **P0-E5 (the fix) has landed —
see "P0-E5 — the fix" below.**

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

## P0-E5 — the fix (2026-09-17)

Both halves landed together, per the resume point above.

### Disk side: `catalogRowLive` + `scanCatalogHeapRows`'s OWN pre-filter

`catalogRowLive` (`internal/initdb/catalog_heap_reload.go:43`) rule 2 was
upgraded exactly as designed: a non-zero `Xmax` is dead only if
`clog.GetStatus(Xmax) != TxnStatusAborted` (committed, sub-committed, or an
unresolved/horizon "unknown" status are all treated as dead — matching doc
`wal-pg-identical-stream/02a-phase-b0-enablers.md` §2.3's B0.2 rule: "xmax
committed or in the recovered-CLOG unknown window → dead; aborted → live").
The crash-recovery implicit-abort sweep (`initdb.Open`'s
`clog.MarkUnknownAsAborted`) always runs before any catalog reload, so a
genuinely in-flight-at-crash `Xmax` is already resolved to `Aborted` by the
time this filter runs.

**A second bug surfaced while verifying live**: `scanCatalogHeapRows`
(`catalog_heap_reload.go:~105`) had its OWN inline pre-filter —
`if ht.Header.Xmin == Invalid || ht.Header.Xmax != Invalid { continue }` —
that ran BEFORE `catalogRowLive` was ever called, so the rule-2 upgrade above
was a no-op against every production reload call until this pre-filter's
`Xmax` half was removed too (the pre-filter now only rejects `Xmin ==
Invalid`; `catalogRowLive` is the sole liveness decision). The design doc's
§2.3 literally named this exact trap ("`scanCatalogHeapRows`'s own pre-filter
never even reaches `catalogRowLive`'s xmax check") but the first pass at this
fix missed it anyway — caught only because the P0-E4 regression tests were
run LIVE against the actual fix, not just built. Two more inline copies of
the identical pre-B0.1 pattern were found and fixed the same way in
`internal/initdb/open.go`: `loadUserIndexesFromHeapForDB`'s two scans
(pg_class RelKind='i' pass and pg_index pass) and the pg_statistic reload —
all three now delegate to `catalogRowLive` instead of duplicating (and
under-fixing) the filter inline. `detectCatalogDBOID` (`open.go:3310`, an
early-bootstrap pg_database OID lookup with no `*transam.CLog` available at
all) was deliberately left alone — it has no CLOG to consult and was already
maximally conservative.

**Known residual gap (not fixed, ledgered)**: a catalog row deleted by a
SUBTRANSACTION whose parent later committed can still resurrect after a
restart far enough in the future that `MarkUnknownAsAborted`'s sweep has
already force-stamped the never-individually-marked sub-XID `Aborted` (goopg
never calls `CLog.SetSubCommitted`/individually stamps sub-XIDs at
parent-commit time, unlike PG's `TransactionIdCommitTree`) — see the deferral
ledger row dated 2026-09-17 for task P0-E5. Out of scope: P0-E4's incident
used only top-level transactions (no `SAVEPOINT`).

### In-memory side: M0143-0008 (ALTER rollback-undo) — partially landed

`AlterIndexUndoEntry` and `NotNullUndoEntry` (`internal/executor/session.go`)
were added alongside the existing `DDLUndoEntry`/`DDLDropUndoEntry` pattern,
recorded by `adoptExistingIndexAsConstraint` and `finishPrimaryKeyConstraint`
(`operators_ddl.go`) and consumed by `ProcessRollbackUndos`
(`operators_tx.go`), so a ROLLBACKed `ALTER TABLE ... ADD CONSTRAINT ...
{PRIMARY KEY|UNIQUE} USING INDEX ...` now reverts `idx.IsConstraint`/
`idx.Primary`/`idx.Deferrable`/`idx.InitiallyDeferred`/a constraint rename,
plus any PRIMARY KEY NOT-NULL synthesis (`col.NotNull` and
`tbl.NotNullConstraints`, including on every inheritance/partition child the
synthesis cascade reached) — in the SAME session, with no restart, closing
the exact symptom M0143-0008 described ("ROLLBACK reports success but does
nothing"). Covered by
`internal/testport/p0e5_alter_rollback_undo_test.go`.

`ALTER TABLE ... DROP CONSTRAINT` also got undo for its three index-backed
forms (PRIMARY KEY / UNIQUE / EXCLUDE — `execAlterTableDropConstraint`,
`operators_ddl.go`): these drops are pure map-removals structurally identical
to `DROP INDEX`, so they reuse the EXISTING `DDLDropUndoEntry`/`RestoreIndex`
mechanism (`Table` is now allowed to be `nil` on that entry — guarded in
`ProcessRollbackUndos` — for an index-only restore).

**Deliberately NOT covered this task** (M0143-0008's own text scoped itself
to "at minimum ADD CONSTRAINT/DROP CONSTRAINT", and the CHECK/FOREIGN
KEY/NOT-NULL `DROP CONSTRAINT` branches mutate table-owned slices in place —
`tbl.CheckConstraints`/`NamedChecks`/`ForeignKeys`/`NotNullConstraints`/
`Columns[i].NotNull`, with their own cascades — rather than the simple
map-removal the index-backed branches get for free): `DROP CONSTRAINT` for
CHECK, FOREIGN KEY, and NOT NULL constraints still has no rollback-undo. See
the deferral ledger row dated 2026-09-17 for task P0-E5 and the new
`M0143-0008b` fix_plan item.

### Gates run for the fix

`go build ./...` clean. `go test ./internal/initdb/...
./internal/catalog/... ./internal/executor/...` PASS.
`go test -v -run 'TestPort_P0E4|TestPort_P0E5' ./internal/testport/` — all 5
PASS (the three P0-E4 repro cases, unskipped, plus the two new P0-E5 live-
session M0143-0008 tests). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — every package passes except the
pre-existing, already-documented `internal/parser` `GroupedJoinUnaliased`
AST-drift (unrelated; this task touched no parser file).
`scripts/tpcds-sf025-regression.sh sweep` — PASS=96 MISMATCH=0 ERROR=0
TIMEOUT=0, plan-shapes identical (99/99), status-delta verdict-changes=none.
`scripts/tpch-spotcheck.sh` is SKIP-BLOCKED by the `:65433` evidence hold
(the one allowed G6 exception per the P0-E5 fix_plan entry) — P0-E7 is the
named re-run owner once P0-E6 restores that cluster.

### M0143-0008b — the DROP-direction sibling (CHECK/FOREIGN KEY/NOT NULL)

P0-E5 deliberately left the three `DROP CONSTRAINT` branches above out of
scope. `M0143-0008b` closes them with `DropConstraintUndoEntry`
(`internal/executor/session.go`) — the DROP-direction twin of
`NotNullUndoEntry`: a single wholesale snapshot of a table's
`CheckConstraints`/`NamedChecks`/`ForeignKeys`/`NotNullConstraints`/
per-column `NotNull` flags, taken before any of the three mutations, applied
to the target table plus every cascade-reachable inheritance/partition child
(reusing `collectNotNullCascadeClosure`'s read-only pre-walk — it is a
generic `collectInheritanceAndPartitionChildren` tree walk, not actually
NOT-NULL-specific, so both the CHECK and NOT NULL branches reuse it
unchanged). `snapshotDropConstraintState` (`operators_ddl.go`) builds the
snapshot; `RecordDropConstraintUndo`/`TakePendingDropConstraintUndos`
(`session.go`) queue and drain it; `ProcessRollbackUndos`
(`operators_tx.go`) writes every field back onto the same live `*catalog.Table`
pointer on ROLLBACK. The FOREIGN KEY branch snapshots only the target table
(FKs are not inherited, so there is no cascade to capture).

**Live discovery while testing the FK branch**: the already-filed
`M0143-0002` bug (`catalog.InMemory.DropForeignKeyConstraint` hardcodes
`DefaultDBOid`) is real and was reproduced live — on a non-default database
(the `db "r"` pattern the sibling P0-E5 tests use), a plain **COMMITted**
`ALTER TABLE ... DROP CONSTRAINT <fk>` (no ROLLBACK involved at all) silently
no-ops: the FK stays fully enforced. The identical statement against the
cluster's default database correctly disables enforcement. Because of this,
`TestPort_M0143_0008b_DropForeignKeyRollbackUndo` runs against the default-db
cluster handle instead of the per-DB one — against db "r" the DROP itself
never mutates `tbl.ForeignKeys`, so a ROLLBACK "restoring" it would be a
false-positive pass regardless of whether this task's fix exists. See the
deferral ledger row dated 2026-09-17 for task `M0143-0008b`; re-verify the FK
undo against db "r" once `M0143-0002` is fixed.

Every one of the three new tests
(`internal/testport/p0e5b_alter_drop_constraint_rollback_undo_test.go`) was
confirmed to genuinely fail with the fix reverted (`git stash` on the three
touched files) and pass with it restored — not just a green run against the
fixed tree.

**Gates**: `go build ./...` clean; `go test ./internal/executor/...
./internal/catalog/...` PASS; `go test -v -run
'TestPort_P0E4|TestPort_P0E5|TestPort_M0143_0008b' ./internal/testport/`
PASS 8/8; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` —
same pre-existing `internal/parser` AST-drift as P0-E5's own gate run, no new
failures; `scripts/tpcds-sf025-regression.sh sweep` — `PASS=96 MISMATCH=0
ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical.
