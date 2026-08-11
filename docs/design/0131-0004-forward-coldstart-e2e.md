# Forward cold-start E2E — real PG 18.3 serves a live goopg cluster directory

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (S4)

## Problem

`0130-0002`'s Guard #1 reads, unchanged since the day it was written: *"PG started
against goopg data dir: `SELECT relname FROM pg_class` lists user tables. (Needs
E2E PG-attach test — not yet implemented.)"* (`:68-69`). Guard #3 depends on it.
Everything M0130 landed downstream of that guard — pg_class / pg_attribute heaps,
pg_attrdef 2604/2656/2657, pg_index 2678/2679, pg_proc argument defaults, pg_hba
replication rules, PG-format user nbtree — was validated through the **basebackup**
lane, where the directory PG boots on is a tar goopg produced and `pg_basebackup`
extracted. No test has ever pointed a real PG at the *live* directory a goopg
server just shut down. This is S3's mirror image: `goopg init` → goopg workload →
clean stop → `postgres -D <same dir>` → psql reads.

## Design

### Test shape

`TestE2E_PGColdStartOnGoopgDataDir`, `internal/testport/`, gated on
`testing.Short()` and `pgcluster.Available` like the rest of the family.

1. `cluster.New` + `Init()` (`internal/testutil/cluster/cluster.go:95`, `:172`) →
   `Start()` (`:214`).
2. Workload, per M0130 acceptance item 1: CREATE TABLE, ALTER TABLE … ADD COLUMN
   (with a DEFAULT, so pg_attrdef participates), CREATE SCHEMA, CREATE INDEX,
   CREATE DATABASE, INSERT, UPDATE, DELETE.
3. `Stop(cluster.ShutdownFast)` (`:279-297`). The stop path takes an implicit
   shutdown checkpoint (`internal/server/server.go:685-690`) and `Runtime.Close`
   calls `Checkpointer.CheckpointShutdown` (`internal/initdb/open.go:3207`), which
   stamps `pg_control.State = DBStateShutdowned`
   (`internal/wal/checkpointer.go:503-516`, `:736-748`). Assert that byte before
   handing over — the precondition S3 asserts in the other direction.
4. `pgcluster.OpenExisting(name, Options{DataDir: goopgDir, User: "postgres"})`
   (`internal/testutil/pgcluster/cluster.go:117-125`) → `Start()` → psql reads.

### The harness needs nothing new

`OpenExisting` deliberately runs neither `initdb` nor `appendConf` — it only builds
a handle over an existing directory (`:120-125`). `Start` (`:236-289`) execs the
`postgres` binary directly with `-D`, `-p`, `-h` on argv; the comment above it
(`:231-235`) records why pg_ctl is unusable (it reads `postmaster.pid`'s PMStatus,
which never reaches `ready`/`standby` when recovering from a goopg backup). The
consequence for S4 is the useful one: **listener configuration arrives on argv, so
goopg's `postgresql.conf` needs no edit.**

That conf is `config.SampleConfig()` verbatim (`internal/initdb/initdb.go:132`),
and `seedPostgresqlConf` early-returns when no locale / text-search / group-access
/ `-c` override was passed (`internal/initdb/config_seed.go:32-35`), so a plain
`goopg init` leaves the file with **zero active assignment lines** — every setting
commented out; PG parses it and uses its own defaults. The one active line the
harness adds is `fsync = off`, appended by `cluster.Init` unless `SyncRuntime` is
set (`cluster.go:176-180`); that is a real PG GUC and is safe. An active goopg-only
GUC would FATAL PG at startup — so the test must not append one, and if a
goopg-private setting is ever seeded uncommented, this test catches it.
`User: "postgres"` is required, not cosmetic: pgcluster defaults `User` to `$USER`
(`:158-164`) while goopg's seeded superuser is `postgres` (`cluster.go:126-129`).

### The three deltas versus the directory PG is already proven to boot on

`TestE2E_PGStandbyFullCycle` proves PG 18.3 starts on goopg **basebackup output**.
This test hands it the live directory instead.

**1. `pg_internal.init` present rather than tar-excluded.** goopg's BASE_BACKUP
drops it (`internal/server/basebackup.go:113`, matching upstream
`basebackup.c:203`), so the proven lane never ships one; the live directory carries
it in `global/`, `base/1` and `base/5`. **Expected not to matter**, per S10's
removal proof: `StartupXLOG` calls `RelationCacheInitFileRemove()`
(`postgres/src/backend/access/transam/xlog.c:5633`) unguarded by `InRecovery`,
before any replay and before hot-standby connections; `relcache.c:6891-6929`
unlinks the `global/` copy and the copy in every all-digit `base/` subdirectory;
the only reader, `load_relcache_init_file` (`relcache.c:6167`), is reached from
`InitPostgres`, which in the standalone path runs *after* `StartupXLOG`
(`postinit.c:787` vs `:818`). This test is where that argument becomes evidence.

**2. File modes.** *(Correction to the S4 sources line.)* The basebackup lane does
**not** normalise modes — `writeTarFileWithMode` copies `info.Mode().Perm()` for
regular files and directories (`basebackup.go:723`, `:728`, `:933`) and forces
`0600` only for `global/pg_control` (`:741`) and `pg_wal` segments (`:817`, `:830`).
So the mode delta is near-nil. What PG enforces is the *data directory's own* bits:
`checkDataDir` FATALs unless `st_uid == geteuid()`
(`postgres/src/backend/utils/init/miscinit.c:384-389`) and unless
`st_mode & PG_MODE_MASK_GROUP` is clear — "Permissions should be u=rwx (0700) or
u=rwx,g=rx (0750)" (`:404-409`). `goopg init` creates the directory `0700`
(`internal/initdb/initdb.go:792`) and `--allow-group-access` relaxes the tree to
`0750`/`0640` (`:622-668`); both are accepted. **Expected not to matter** — assert
nothing specific, but a FATAL here is a real finding.

**3. `postmaster.opts` / `postmaster.pid`.** *(Correction.)* Both are on
basebackup's exclusion list (`basebackup.go:105-106`), matching upstream — but in
the live goopg directory only one can exist at all. `postmaster.opts` has **no
writer anywhere in goopg**: that exclusion entry is its only non-test occurrence.
`postmaster.pid` **is** written (`control.WritePIDFile` from `startControlPlane`,
`internal/server/server.go:677`) and **removed on a clean stop** (`stopControlPlane`
→ `control.RemovePIDFile`, `:808-815`). After `Stop(ShutdownFast)` the directory
carries neither, so this delta is real only after a crash or SIGKILL, which this
test does not do — assert the absence of `postmaster.pid` before handing over so a
surprise fails here rather than as a confusing PG lock-file error.

Beyond the three, goopg-private files PG has never seen: `global/system_identifier`
(`internal/initdb/initdb.go:47`) and the per-database `pg_goopg_catalog_cache.json`
— the latter is *not* on the exclusion list, so the proven lane already ships it
and it is no delta; `.goopg.ctl.sock` is excluded (`basebackup.go:107`) and is
unlinked with the control listener. The postmaster does not enumerate or validate
unknown files at the data-directory root, so these are expected inert, and Guard #4
covers the expectation.

### What may be asserted — and what waits for S6

**No view assertions until S6 lands.** A real PG hosted on a goopg cluster
directory cannot evaluate **any** view today: the rewriter reads relcache
`rd_rules`, `RelationBuildRuleLock` populates it only through `pg_rewrite`'s
`RewriteRelRulenameIndexId` (2693) — which goopg does not maintain at runtime — and
`systable_beginscan` with `indexOK=true` has no seq-scan fallback, so 42809 comes
out of `postgres/src/backend/optimizer/util/plancat.c:139-149`. Same wall blocker #7
hit in `0130-0010`, where the harness rewrote a `pg_stat_replication` probe as
`pg_stat_get_activity(NULL) JOIN pg_stat_get_wal_senders()`. S4 phrases every probe
over **user tables and SRFs**; once S6 flips `relhasrules`, add the view assertions
here too (S4.4). In scope now:

- `SELECT relname FROM pg_class WHERE …` lists the user tables — literally
  `0130-0002` Guard #1, which this test discharges.
- Row-level reads, including the `ADD COLUMN … DEFAULT` column (M0130's pg_attrdef
  work is what makes this survivable) and reads inside the
  `CREATE DATABASE`-created database.
- **Index behaviour is live, not hypothetical.** `relhasindex` is no longer
  hardcoded false: `buildUserPGClassRow` computes it via `pgClassRelhasindex`
  (`internal/executor/pg18_user_catalog_rows.go:564`, `:591-635`), true only when
  *every* index on the table has a PG-describable key (`buildPGIndexKeyDesc`), with
  the S11.4 gate `pgIndexTupleKeys` currently `true`
  (`internal/executor/pgindex_btree.go:54`). So PG may plan an index scan over a
  goopg-authored user index. A predicate on the indexed column belongs in the
  assertions; an `XX002 … contains corrupted page at block 0` there is blocker #12
  resurfacing — a finding, not a test bug.
- Zero `FATAL` in the PG log. pgcluster allocates the log as a sibling of the data
  dir when `LogPath` is empty (`cluster.go:146-149`) and truncates it on `Start`
  (`:240`), so the scan is unambiguous.

### S4.5 — the non-atomic UPDATE gap is in scope to diagnose

The general `updateOp` fallback emits `HeapDelete` + `HeapInsert` as two records
rather than one atomic non-HOT update (ledger M0118-0129, restated in M0130-S7.2).
The workload includes UPDATE deliberately. If it surfaces — a row visible twice,
missing, or a PG complaint during startup replay of the tail — diagnose and ledger
it; do not drop UPDATE from the workload. The shape differs from the replication
lane: here PG replays goopg's WAL tail from its **own** startup, not through a
walreceiver, so this is the first test exercising those records on that path.

## Guards

1. `TestE2E_PGColdStartOnGoopgDataDir` runs the full chain: `goopg init` → workload
   → `Stop(ShutdownFast)` → `postgres -D <same dir>` → psql reads.
2. `pg_control.State == DB_SHUTDOWNED` and the absence of `postmaster.pid` are
   asserted between the stop and the PG start.
3. `SELECT relname FROM pg_class` on the hosted PG lists the user tables —
   `0130-0002` Guard #1 discharged; update that doc's guard text in the same
   change, alongside S10.4's edits.
4. Zero `FATAL` in the PG log for the whole run.
5. PG boots with **no** edit to goopg's `postgresql.conf`; the only active line in
   it is the harness's `fsync = off`.
6. Assertions over user tables and SRFs only, with a `TODO(S6)` naming the view
   assertion to add once `relhasrules` flips.
7. An index-qualified predicate is asserted; `XX002` is treated as a blocker-#12
   regression and ledgered.
8. If the UPDATE gap surfaces it is diagnosed and ledgered — the workload keeps its
   UPDATE.
9. The E2E family stays green (`go test -v -run '^TestE2E_' ./internal/testport/`).
10. UNITS + SMOKE green.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S4,
  §S5 (the 42809 chain), §S10 (the `pg_internal.init` removal proof)
- `docs/design/0130-0002-pg-class-heap-persistence.md` Guards #1/#3;
  `0130-0010-pg183-standby-e2e-harness.md` (basebackup lane; blocker #7's SRF
  detour, blockers #10/#12)
- `internal/testutil/{pgcluster,cluster}/cluster.go`;
  `internal/initdb/{config_seed.go,initdb.go,open.go}`;
  `internal/wal/checkpointer.go`; `internal/server/{basebackup.go,server.go}`;
  `internal/executor/{pg18_user_catalog_rows.go,pgindex_btree.go}`
- `postgres/src/backend/access/transam/xlog.c:5633`;
  `…/utils/cache/relcache.c:6167`, `:6891-6929`; `…/utils/init/postinit.c:787`,
  `:818`; `…/utils/init/miscinit.c:384-409`; `…/optimizer/util/plancat.c:139-149`
- deferral ledger M0118-0129 (non-atomic non-HOT UPDATE)
