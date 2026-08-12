# Forward cold-start E2E — real PG 18.3 serves a live goopg cluster directory

**Status:** accepted (landed 2026-08-11 as `TestE2E_PGColdStartOnGoopgDataDir`;
see §Findings)
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

## Findings (2026-08-11, from the first runs)

**The headline result is positive, and it is the one the milestone asked for.**
A real PG 18.3 starts on the live directory a goopg server just shut down, with
**zero** edits to `postgresql.conf`, and reads goopg-written data correctly:
`SELECT relname FROM pg_class` lists both user tables (`0130-0002` Guard #1, now
discharged), `count(*)`/`sum(qty)` agree with goopg across the UPDATE and the
DELETE, the non-public schema resolves, and an index-qualified read under
`enable_seqscan = off` returns the right row through a goopg-authored btree —
so blocker #12 did **not** resurface (guard 7). Zero FATAL in the log up to the
two deliberately destructive probes below (guard 4). The three "deltas versus
the basebackup lane" predicted above all held: `pg_internal.init` was inert,
file modes were a non-issue, and the directory carried neither
`postmaster.pid` nor `postmaster.opts` after the clean stop.

Four gaps were measured. Each is locked into the test in the **fail-when-fixed**
direction — the same discipline `0131-0003` used for the HOT-flag finding that
became S11 — so none can be silently "fixed" without the test demanding the
assertion be inverted.

**F1 — a hosted PG cannot execute ANY sort, for ANY type (→ M0131-S12).**
`SELECT x FROM (VALUES (2), (1)) v(x) ORDER BY x` fails with *"could not
identify an ordering operator for type integer"*; nothing in that query touches
a goopg table. The chain is `lookup_type_cache(TYPECACHE_LT_OPR)` →
`GetDefaultOpClass`, which at `postgres/src/backend/commands/indexcmds.c:2374-2384`
scans `pg_opclass` through `OpclassAmNameNspIndexId` (**2686**) with
`indexOK = true` and no seq-scan fallback — the identical shape as blockers
\#7/\#8 and M0131-S5. The heap is complete and correct (probed on the hosted PG:
177 rows, `int4_ops` = oid 1978 / `opcmethod` 403 / `opcdefault` 't', 38 default
btree opclasses) and `pg_index` carries valid 2686/2687 rows. What is missing is
the index **content**: `internal/initdb/initdb.go` writes 2686 as a bare
`makeBtreeRootPage()` placeholder, while its sibling
`pg_opfamily_am_name_nsp_index` (2754) gets a real bulk-load bootstrapper —
and `pgBuildIndexTupleOidNameOidKey`
(`internal/initdb/btree_index_bootstrap.go:1909`) already names 2686 in its doc
comment as one of the two indexes it serves. No caller ever builds 2686's
tuples. Lock-in: `assertEmptyOpclassIndexStillBlocksSorts`; the Guard-#1 query
carries no `ORDER BY` and sorts in Go until S12 lands.

**F2 — calling any LANGUAGE SQL builtin crashes the hosted backend (→ M0131-S13).**
`SELECT 'a'::text || 1` aborts with
`TRAP: failed Assert("ARR_NDIM(array) == 1"), File: "guc.c", Line: 6411`, via
`TransformGUCArray` ← `fmgr_security_definer`. Cause: goopg writes **every one**
of its 3397 `pg_proc` rows with a NON-NULL `proconfig` (probed:
`SELECT count(*) FROM pg_proc WHERE proconfig IS NOT NULL` = 3397 = the full row
count; `prosecdef` is correctly `'f'` everywhere, so it is `proconfig` alone),
and the bytes behind that attribute are not a valid 1-D `text[]`. Upstream
reaches the handler from `fmgr_info_cxt_security`
(`postgres/src/backend/utils/fmgr/fmgr.c:203-211`), whose condition is
`prosecdef || !heap_attisnull(…, Anum_pg_proc_proconfig, NULL) || FmgrHookIsNeeded`.
The blast radius is bounded precisely by `fmgr_isbuiltin`: a function in the
compiled-in `fmgrtab` never reads `pg_proc`, which is why `int4eq`, `textout`,
`count` and `sum` are all fine and every other probe in this test survives. It
is the LANGUAGE SQL builtins that break — `textanycat` (oid 2003) among them,
which is what `text || integer` resolves to. This is an assert-enabled PG build,
so the failure is loud; a production build would walk a bogus `ArrayType`
instead, which is worse. Lock-in:
`assertProconfigGapStillCrashesSQLFunctions`, run LAST because it takes the
postmaster down; the row-content reads use two separate scalars rather than
`label || '/' || qty`.

> **F2 RESOLVED 2026-08-12 (M0131-S13).** The hosted PG now answers
> `SELECT 'a'::text || 1` = `a1`, and
> `SELECT count(*) FROM pg_proc WHERE proconfig IS NOT NULL` = **0**.
>
> Root cause, once located, was a **stale sibling**, not a bitmap or `t_hoff`
> defect as S13.1 hypothesised: goopg has two builders for the 30-column
> physical `pg_proc` row, and only one was right. The runtime builder
> `buildPGProcRow` (`internal/executor/sys_pg_proc.go`) already wrote
> `NullDatum` for every absent nullable varlena, with a comment explaining
> exactly why ("PG branches on `attisnull` for these"). The initdb seed builder
> `pgProcRow` (`internal/initdb/initdb.go`) wrote `NewStringDatum("")` for the
> same columns, which falls through `encodeValuePG` to `emptyArrayTypeBytes` —
> a **non-NULL zero-dimension `ArrayType`**. `NullBitmapPG` /
> `writeMultiPageHeapRows` were both already correct and needed no change; they
> simply had nothing to mark, because no datum was NULL.
>
> The fix makes the seed path match the runtime path for the whole trailing
> nullable group, per S13.2 — `proallargtypes`, `proargmodes`, `proargnames`,
> `proargdefaults` (when `pronargdefaults = 0`), `protrftypes`, `probin`,
> `prosqlbody`, `proconfig`, `proacl`. Fixing `proconfig` alone would have left
> `prosqlbody` as an empty `pg_node_tree` — `stringToNode("")` — one probe away.
>
> Guards: `internal/initdb/pg_proc_nullable_varlena_test.go` sweeps all 3397
> seed entries at the `Row` level **and** re-decodes one physical tuple through
> `DecodeRowIntoMctxPGTuple`, because the `Row`-level half alone would pass even
> if the tuple writer dropped the bitmap. Proven fail-when-broken: restoring
> `NewStringDatum("")` on `proconfig` fails it at `oid=3
> (heap_tableam_handler)`. The E2E lock-in is INVERTED per S13.4 —
> `assertHostedPGCanCallSQLBuiltins` asserts the call succeeds *and* the
> `proconfig IS NOT NULL` count is zero, and the row-content reads are back to
> the single `label || '/' || qty` form, which is now itself the tripwire
> (`text || integer` = `textanycat`, oid 2003, LANGUAGE SQL). The probe is no
> longer destructive, so it runs inline before guard 4 instead of last.
>
> **S13.3 partially discharged, and it found one more site.** `pg_attribute`
> was already correct (Step 3u had fixed `attacl`/`attoptions`/`attfdwoptions`/
> `attmissingval`/`attstattarget` for the identical reason — an empty varlena
> read as "attoptions present", ending in an `ERRORDATA_STACK_SIZE` PANIC).
> `pg_class` is **not**: `pgClassNailedRow` still writes `relacl = '{}'`,
> `reloptions = '{}'` and `relpartbound = ''` where upstream's initdb leaves all
> three NULL. It is latent rather than measured — the E2E connects as a
> superuser, who bypasses `pg_class_aclcheck` entirely, so a non-null EMPTY
> `relacl` (which grants nobody anything, not even the owner) cannot be seen
> from this lane. Deferred with a ledger row rather than fixed blind: it needs a
> non-superuser hosted-PG probe to state what it actually breaks.

**F3 — `ADD COLUMN … DEFAULT` reads NULL on pre-existing rows (→ M0131-S14).**
goopg reads `'dflt'`; the hosted PG reads NULL for all 15 rows. **The pg_attrdef
half is entirely correct** — the hosted PG reads
`pg_get_expr(adbin, adrelid)` as `'dflt'::text` on `adnum` 4 and
`pg_class.relnatts` as 4, which is M0130's 2604/2656/2657 work validated outside
the basebackup lane for the first time, and the test asserts that positively.
What is missing is PG's **fast-default** mechanism: since PG 11, ADD COLUMN with
a non-volatile DEFAULT does not rewrite the heap but stores the value in
`pg_attribute.attmissingval` with `atthasmissing = true`, and short tuples
materialise it on read. goopg neither rewrites the rows nor records the missing
value. Underneath sat a sharper fact, and **that half is FIXED (S14.1,
2026-08-12)**: `attmissingval` did not exist as a column at all on the hosted PG
(`SELECT attmissingval FROM pg_attribute` → 42703).

**F3 is now CLOSED (S14.2/S14.4, 2026-08-12).** The lock-in was INVERTED: the
helper is `assertFastDefaultMaterialisesOnHostedPG`, and it asserts positively
that the hosted PG reads `atthasmissing = 't'` for `s4_items.tag` and
materialises `tag = 'dflt'` on all 15 physically short pre-ALTER rows — a real
PG 18.3 running `getmissingattr()` over goopg-authored catalog bytes, with no
heap rewrite on either side. See *S14.2* below.

*S14.2, as measured and fixed.* Three things had to agree.

1. **The value.** `buildUserPGAttributeRow` now writes `atthasmissing` plus a
   one-element `ArrayType` built exactly as `StoreAttrMissingVal`
   (`postgres/src/backend/catalog/heap.c:2030`) builds it via `construct_array`:
   `ndim=1`, `dataoffset=0` (a NULL default stores nothing at all —
   `ATExecAddColumn` skips the call when `missingIsNull`), `elemtype` = the
   COLUMN's own `atttypid` (so an array column's fast default is an
   array-of-array, as upstream), `dims[0]=lbound[0]=1`, and the element
   normalised to the 4-byte varlena header form. Source of truth for the value
   itself is unchanged: `catalog.Column.MissingValue`, set by the existing
   fast-default backfill in `execAlterTableAddColumn`. An encode failure
   degrades to the pre-S14.2 shape rather than failing the DDL.
2. **The type.** `attmissingval` was declared `text` in all three sibling column
   lists; it is `anyarray` (OID 2277). PG dereferences the datum as
   `ArrayType *`, so `text` was only survivable while the column was always
   NULL.
3. **The alignment.** `anyarray` carries `typalign => 'd'`
   (`postgres/src/include/catalog/pg_type.dat:573`) — 8 bytes, unlike every
   other varlena array's `'i'`. Both `initdb.pgTypeAlignChar` (nailed
   self-description) and `executor.physicalPGTypeAlign` (the wire encoder) had
   grouped it with the 4-byte varlenas. That mis-padding also covered
   `pg_statistic.stavalues1..5`, the only other `anyarray` catalog columns; it
   was invisible for the same reason the S14.1 permutation was — a NULL column
   consumes neither bytes nor padding — and would have shifted every following
   byte one word early in the first tuple carrying a real value.

That is the same shape as S14.1 and S13: a value that is always NULL hides
disagreement among sibling definitions until something finally writes it
([[pattern_sibling_paths_must_agree]]).

*S14.3, decided and deferred.* goopg's own reader still materialises from the
in-memory `catalog.Column.MissingValue` (`internal/executor/codec.go`, the
M0097-0077 short-tuple path), not from the heap's `attmissingval`. The two
halves are therefore written together but read independently. Making the heap
the single source of truth is the PG-faithful end state and
`pgSingletonArrayElement` is the reader it needs, but it is a separate change
with its own restart/reload surface — ledgered, not smuggled in here.

*S14.1, as measured and fixed.* The 42703 was **not** a missing heap row —
goopg's rows for relid 1249 always numbered 25, `attmissingval` among them. It
was `pg_class.relnatts = 24` on the nailed pg_attribute row. PG unlinks goopg's
`pg_internal.init` at startup (`xlog.c:5633` `RelationCacheInitFileRemove`) and
builds `pg_attribute` from its own compiled 25-column `Desc_pg_attribute`, but
`RelationCacheInitializePhase3` then copies `rd_rel` straight out of goopg's
`pg_class` tuple — so the short `relnatts` truncated the last column off an
otherwise correct descriptor. In PG18 order that last column is `attmissingval`.

Correcting `relnatts` exposed a permutation beneath it. goopg ordered the five
nullable trailing columns
`attacl/attoptions/attfdwoptions/attmissingval/attstattarget`; PG18 orders them
`attstattarget/attacl/attoptions/attfdwoptions/attmissingval`. The old code
appended `attstattarget` last on the stated theory that its canonical position
was #4 — true through PG16, but PG18 moved it after `attcollation` when it
became nullable. With all five NULL the two orders are physically
indistinguishable (identical null bitmap, unchanged `t_hoff`), which is why it
survived this long; `ALTER COLUMN … SET STATISTICS` writes `attstattarget`, and
a hosted PG would then have read that `int2` as `attacl`.

goopg described `pg_attribute` in four places and they had drifted apart:
`initdb.pgAttrColDefs`, `catalog.PGAttributeColumns` and
`executor.pgAttributeColumnsPG18` shared the wrong tail order, while
`initdb.pgAttributeAttrs` (the `pg_internal.init` descriptor) was a 24-column
PG-11-era layout with an `attcacheoff` PG18 no longer has. `pgAttributeAttrs`
now DERIVES from `pgAttrColDefs` through a shared helper, so that pair cannot
drift again; the other two are pinned by
`TestPGAttributeSelfDescriptionIsPG18Canonical` (initdb) and
`TestPGAttributeColumnsPG18IsCanonical` (executor), which spell out PG18's order
in full. This is the [[pattern_sibling_paths_must_agree]] failure mode for the
third time in this milestone (S13's `pg_proc`, Step 3u's `attoptions`, now this).

**F4 — a goopg-`CREATE DATABASE`-minted database is unopenable (→ M0131-S15).**
Connecting to `s4other` fails with
`PANIC: could not open critical system index 2662` (`pg_class_oid_index`) —
`RelationCacheInitializePhase3` nails and opens a small set of critical indexes
during `InitPostgres`, and a failure there is a PANIC, so the cluster goes down
with the connection attempt. This is a statement about goopg's **runtime**
CREATE DATABASE, not about the cold start: the `postgres` database in the SAME
directory serves every assertion above. `initdb` writes each bootstrapped btree
into `base/1` **and** `base/5`; the runtime path clones only what goopg itself
needs, and goopg reads its catalogs without those indexes, so nothing in the
goopg-only world notices. Lock-in:
`assertGoopgCreatedDatabaseStillUnopenableByPG`, also destructive.

**Harness correction.** `pgcluster.Stop` sent SIGINT and waited unboundedly. A
postmaster that has entered crash recovery after a backend abort never acts on
it, which hung the whole `go test` for its 20-minute timeout instead of
reporting the crash that caused it. `Stop` now waits 20 s and then SIGKILLs.
A new `PSQLCombined` returns combined output without a `t.Fatalf`, so a caller
can classify the error text — F1/F2/F4 are all diagnosed from error text.

**S4.5 verdict: the non-atomic non-HOT UPDATE gap did NOT surface.** The
workload's UPDATE (`qty = qty + 1 WHERE id % 3 = 0`) and DELETE both read back
correctly through the hosted PG — `sum(qty)` matches goopg exactly, and the
UPDATEd row id=9 has the right label and qty. Ledger M0118-0129 stays open, but
this lane is not where it shows.

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
