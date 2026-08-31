# M0131 Implementation Plan — Bidirectional cluster-directory cold-start + real-PG system-view hosting

**Status:** planned
**Date:** 2026-08-11
**Milestone:** `docs/milestones/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Design of record:** M0130 Acceptance-bar item 1 (undischarged);
`docs/design/0130-0002-pg-class-heap-persistence.md` §"Remaining for full
reverse-path parity"; deferral-ledger rows #428, #490, #995, #996.
**Convention:** this is the authoritative task decomposition for M0131; each
S-task may ride this doc or have its own `0131-NNNN-*.md` sub-design doc (listed
in the milestone's Required design docs table).
**Decomposition source:** three parallel investigations run at filing
(2026-08-11): the upstream relcache/rewriter path, the harness capability
inventory, and the `copyInitFiles` provenance audit. Their findings are folded
into the per-task Sources lines below.
**Corrections applied 2026-08-11** after the sub-design docs re-verified every
citation — where a sub-doc and this plan disagree, **the sub-doc wins**:
`internal/wal/recovery.go`'s `replayDecodedXLogRecord` is at `:2207` with its
`default:` arm at `:2525` (not 2208/2526); `postgres/src/backend/access/common/tupdesc.c:105`
is an `elog(ERROR)`, not a FATAL (the "FATAL" wording is goopg's own comment at
`internal/initdb/pg_type_bootstrap.go:322-331`); S1's blast radius is **six**
unregistered GUC names, not four (see S1); S4's "three deltas" reduces to
essentially one (see S4); `pg_stat_activity` and `pg_settings` swap S9 buckets
(see S9); and the ledger references written as `#NNN` throughout are **line
numbers** in `.ralph/deferral_ledger.md`, not values of an ID column — no such
column exists.

## Positioning

M0130 proved goopg and PG 18.3 interoperate **through the replication protocol**.
M0131 proves they interoperate **through the filesystem** — either engine cold-starts
on the other's directory — and removes the one engine limitation that makes a
hosted PG a second-class citizen: it cannot evaluate a view.

Nothing here re-does M0130's format work. The on-disk shapes (FSM/VM forks,
pg_class/pg_attribute/pg_type heaps, nbtree pages and tuples, `RM_BTREE` WAL,
TLI reconciliation, pg_attrdef, pg_proc defaults, pg_hba replication rules,
pg_index 2678/2679, index-relation pg_attribute rows) are already PG-identical
and are exercised, not modified.

## Ordering principle

Theme A first: it is the cheaper direction (the decoders are already proven
against PG-authored rows by `TestE2E_FailoverPGtoGoopg`), and its two engine
fixes are single-function. Theme B is "write the test and see" — budget for
discoveries. Theme C is independent of A and B and may proceed in parallel;
within C the order is strict — **corrected after review to
`S5 → S8a → S6 → S7 → S8b → S9`**. The original `S7 → S8` was circular: S7's
capture tool consumes S8's OID-mapping table, while S8's guards verify against
S7's manifest. Splitting S8 into **S8a** (the policy decision + the pinned OID
table, no manifest needed) and **S8b** (the manifest-based guards) breaks the
cycle. S8a must also precede **S6**, because under pinning S6.1's
`12261 → 12105` blob patch is *reverted* and `pg_replication_slots` moves the
other way (`12105 → 12261`) — deciding first avoids three rework items. S5 and
S6 are in fact independent (S5's `b5c_view` gate shares nothing with S6's
`pg_stat_replication` gate); S5 is listed first only because it is the smaller
of the two.
S10 should land early — leaving `copyInitFiles` in place while writing new
cold-start tests risks copying the same dead pattern into them.

## Common gate vocabulary

Same as M0129/M0130's binding list: UNITS (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`),
SMOKE (the git-hook pgbench), SPOT (`scripts/tpch-spotcheck.sh`),
DS05 (TPC-DS SF0.5 regression gate), PLAN (`make plan-gate`),
RACE (`make race-gate`), SIBLING (encode↔decode, fast-path↔interpreted,
column-lookup↔star-expansion), `make ralph-state-guard`.

**E2E (this milestone's characteristic gate):**
`go test -v -run '^TestE2E_' ./internal/testport/` — the replication family.
Individual tests: `-run '^TestE2E_PGStandbyFullCycle$'` (~40 s),
`-run '^TestE2E_FailoverGoopgToPG$'`, `-run '^TestE2E_FailoverPGtoGoopg$'`.
These are gated behind `testing.Short()` and PG-binary presence
(`pgcluster.Available`), so they do **not** run in an ordinary short-mode CI
pass — run them explicitly.

---

## Theme A — Reverse cold start (goopg on a PG-initdb'd directory)

### S1 — GUC registry accepts a PG-18-initdb `postgresql.conf` (est ~1 loop)

**Sources:** `cmd/goopg/main.go:360-363` (auto-discovery), `:406-415` (hard exit);
`internal/config/guc.go:563-579` (`ApplyConfigEntries` aggregates unknown
parameters into an error); measured against a live PG 18.3 initdb output.

The eight settings a stock PG 18 initdb writes that goopg has never registered:
`dynamic_shared_memory_type`, `log_timezone`, `autovacuum_worker_slots`
(PG18-new), `lc_messages`, `lc_monetary`, `lc_numeric`, `lc_time`,
`default_text_search_config`. goopg refuses to start before it opens a single
catalog page — this is the first thing that fails in direction A, and it is not
a catalog problem at all.

**Subtasks:**
- S1.1 Decide per GUC: accepted-stub (parsed, stored, not acted on) vs. real
  implementation. Follow the existing stub pattern at
  `internal/config/defaults.go:122` (the pg_dump GUCs). Locale and text-search
  GUCs are stubs; `dynamic_shared_memory_type` is a stub; `log_timezone` should
  track the existing timezone handling if one exists.
- S1.2 Register them in `BuildDefaultRegistry` with PG 18 `BootVal`s — per the
  repo's GUC discipline, `BootVal` is PG's default, never a goopg-tuned value.
- S1.3 Add the matching commented-out entries to
  `internal/config/postgresql.conf.sample` in the correct PG-style sections
  (mandatory — `TestSampleConfigCoversRegistry` enforces it).
- S1.4 Probe and, if confirmed, fix the self-inflicted sibling. **Wider than
  first filed:** `seedPostgresqlConf` (`internal/initdb/config_seed.go:32-83`)
  writes **six** unregistered GUC names into goopg's *own* `postgresql.conf`,
  not four — the four `lc_*` from `localeGUCSettings`
  (`internal/initdb/locale.go:235-245`), plus `default_text_search_config`
  (`-T`, `:56`), `password_encryption` (`--pwfile` with md5/scram, `:63`) and
  `log_file_mode` (`-g`, `:71`). The last two are absent from
  `internal/config/` entirely. Because `replaceGUCValue` appends when the
  template has no matching line, **every one of these paths produces a
  directory `goopg start` refuses** — e.g. `goopg init --lc-messages=C`.
  Verify with a throwaway init per flag; ledger any not fixed here.

**Gates:** UNITS (incl. `TestSampleConfigCoversRegistry`) + SMOKE.
**Design doc:** `docs/design/0131-0001-guc-registry-pg-initdb-conf-compat.md`.

### S2 — `LoadOrCreateSystemID` reads pg_control first (est ~1 loop)

**Sources:** `internal/initdb/initdb.go:47` (`const systemIdentifierFile =
"global/system_identifier"`), `:54-76` (the function);
`internal/initdb/timeline.go:44-68` (`LoadOrCreateTimelineID`, the correct
shape, landed as M0130-S8.1).

On a directory that has no goopg-private `global/system_identifier`, the current
code **invents a fresh random ID** and writes it — diverging from the system
identifier already recorded in `pg_control`. That is silent state corruption on
any PG-authored directory, and it is exactly the bug M0130-S8.1 already fixed
for the timeline ID without generalising the lesson.

**Prerequisite discovered while writing `0131-0002`:** `internal/control`'s
`ControlFileData` has **no `system_identifier` field at all** — the struct
begins at `State` (offset 16, `pgcontrol.go:44-46`). S2 must therefore first add
a decode of `buf[0:8]`. Decode-only is recommended, since `UpdateControlFile`
(`pgcontrol.go:205`) preserves untouched bytes.

**Subtasks:**
- S2.1 Read `pg_control`'s `system_identifier` when the flat file is absent;
  write the flat file from it so subsequent starts are unchanged.
- S2.2 Keep random generation only for a genuinely fresh `goopg init`.
- S2.3 If both exist and disagree, decide and document the precedence
  (pg_control wins is the PG-faithful answer) and log at WARNING.

**Gates:** UNITS + a new unit test that stages a pg_control without the flat
file and asserts the ID matches.
**Design doc:** `docs/design/0131-0002-system-identifier-pgcontrol-fallback.md`.

### S3 — E2E: real `initdb` → PG workload → clean stop → goopg serves (est ~2 loops)

**Sources:** harness inventory — `internal/testutil/pgcluster/cluster.go:103-115`
(`New` runs the real `initdb`), `:183-195` (`runInitdb`), `:293-305` (`Stop`
sends SIGINT = PG fast shutdown → shutdown checkpoint → `DB_SHUTDOWNED`);
goopg side `cluster.New{DataDir:…}` + `Start()` **without** `Init()`, the shape
already used at `internal/testport/e2e_failover_pg_to_goopg_test.go:131-148`.
Clean-shutdown precondition and its rationale:
`docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint".

**Subtasks:**
- S3.1 New test `TestE2E_GoopgColdStartOnPGDataDir` in `internal/testport/`.
  Real PG with `pgcluster.Options{User: "postgres"}` so the PG-created
  `pg_authid` row matches what goopg's connections expect.
- S3.2 PG-side workload: CREATE TABLE, CREATE INDEX, INSERT, UPDATE, DELETE,
  and a CREATE SCHEMA — matching M0130 acceptance item 1's list minus the parts
  the reverse decoder provably cannot see yet (see S3.5).
- S3.3 `Stop()` (SIGINT), then assert `DB_SHUTDOWNED` in pg_control before
  handing the directory over — the reverse path is only defined for a cleanly
  shut down source.
- S3.4 Start goopg on the directory with **no** conf rewriting (S1 makes this
  possible; if the test still needs a rewrite, S1 is incomplete). Verify whether
  `normalizePGWALSegmentNames`
  (`e2e_failover_pg_to_goopg_test.go:315-339`, currently test-local) is still
  required for a non-basebackup directory — if it is, that is a finding, not a
  harness detail: ledger it.
- S3.5 Assert `SELECT`s return the PG-written rows. Bound the assertions
  honestly: `DecodePGClassPhysicalRow` (`internal/catalog/codec.go:853-901`)
  decodes only the fields `loadUserTablesFromHeap` needs and drops `relowner`,
  `relacl`, `relhastriggers`, `relrowsecurity`, `relreplident`, `relchecks` and
  the partitioning fields; `loadUserTablesFromHeap` further keeps only
  `relkind ∈ {r,m,v,S}` at `OID >= catalog.FirstUserOID`. Do not assert over
  what the decoder discards — assert it, ledger it, or widen the decoder.
- S3.6 Exclude `VACUUM FULL` / `CLUSTER` / `REINDEX` on any catalog from the
  workload and say why in a comment: `pg_filenode.map` is **written but never
  read** by goopg (writers at `internal/initdb/initdb.go:136`, `:1895-1960`,
  `:2166`; no reader anywhere), so goopg addresses catalog relfiles by OID. That
  holds on a fresh initdb (`base/5/1247`, `1249`, `1259`, … are literally named
  by OID) and breaks the moment a mapped catalog is rewritten. Ledger row #388
  already names this as its re-arm trigger.

**Gates:** the new test + E2E family + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0003-reverse-coldstart-e2e.md`.

---

## Theme B — Forward cold start (real PG on a goopg-created directory)

### S4 — E2E: `goopg init` → goopg workload → clean stop → real PG serves (est ~4 loops)

**Sources:** `internal/testutil/pgcluster/cluster.go:117-125` (`OpenExisting`
takes any directory and does not run initdb or touch the conf), `:236-289`
(`Start` execs `postgres -D -p -h` directly, so goopg's all-commented
`postgresql.conf` needs no edit); goopg's clean shutdown writes a
PG-recognisable shutdown checkpoint via `wal.CheckpointShutdown`
(`internal/wal/checkpointer.go:503-506`, `:741` sets `DB_SHUTDOWNED`).

**The harness needs nothing new.** The work is the test plus whatever it
uncovers.

**The "three deltas" reduce to one — corrected while writing `0131-0004`:**
- `pg_internal.init` present rather than tar-excluded — **real**, and the only
  substantive delta.
- File modes — **not a delta.** The basebackup lane does *not* normalise them:
  `writeTarFileWithMode` copies `info.Mode().Perm()` (`internal/server/basebackup.go:723`,
  `:728`, `:933`) and forces `0600` only for `pg_control` and WAL segments. What
  actually matters is `checkDataDir`'s 0700/0750 + ownership check
  (`postgres/src/backend/utils/init/miscinit.c:384-409`), which goopg's `0700`
  init (`internal/initdb/initdb.go:792`) already satisfies.
- `postmaster.opts` / `postmaster.pid` — **not a delta after a clean stop.**
  goopg has no writer for `postmaster.opts` anywhere (only the basebackup
  exclusion entry references it), and `postmaster.pid` is removed on clean
  shutdown (`stopControlPlane` → `control.RemovePIDFile`,
  `internal/server/server.go:808-815`). Turn this into an assertion, not a
  caveat; it becomes a real delta only after a crash.

**Also corrected:** `pg_class.relhasindex` is no longer hardcoded false —
`pgClassRelhasindex` (`internal/executor/pg18_user_catalog_rows.go:591-635`)
with `pgIndexTupleKeys = true` (`internal/executor/pgindex_btree.go:54`)
means a hosted PG may genuinely plan an index scan over a goopg user index. So
S4 should put an index-qualified predicate in scope and treat `XX002`
(`_bt_checkpage` rejection) as a **regression signal**, not an expected wall.

**Subtasks:**
- S4.1 New test `TestE2E_PGColdStartOnGoopgDataDir`. goopg init → start →
  workload (CREATE TABLE / ADD COLUMN / CREATE SCHEMA / CREATE INDEX /
  CREATE DATABASE / INSERT / UPDATE / DELETE, per M0130 acceptance item 1) →
  `Stop(cluster.ShutdownFast)`.
- S4.2 `pgcluster.OpenExisting` on that same directory → `Start()` → psql reads.
  Assert `SELECT relname FROM pg_class` lists the user tables (this is
  `0130-0002`'s Guard #1, which has read *"Needs E2E PG-attach test — not yet
  implemented"* since it was written).
- S4.3 Assert zero FATAL in the PG log — the acceptance bar's actual wording.
- S4.4 **Do not assert over views until S5/S6 land.** Until then a hosted PG
  cannot evaluate any view; phrase assertions over user tables and SRFs. Once
  S6 lands, add the view assertion here too.
- S4.5 Non-atomic non-HOT UPDATE is a known open gap (ledger M0118-0129,
  restated in M0130-S7.2): the general `updateOp` fallback emits `HeapDelete` +
  `HeapInsert` as two records rather than one atomic update. The workload
  includes UPDATE, so if this surfaces, it is in scope to diagnose and ledger,
  not to silently avoid.

**Gates:** the new test + E2E family + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0004-forward-coldstart-e2e.md`.

---

## Theme C — Real PG hosted on goopg evaluates views

### S5 — Runtime `pg_rewrite` index maintenance, indexes 2692 + 2693 (est ~1 loop)

**Sources:** `postgres/src/backend/utils/cache/relcache.c:752-917`
(`RelationBuildRuleLock` scans `RewriteRelationId` 2618 via
`RewriteRelRulenameIndexId` **2693**, `ScanKey` on `Anum_pg_rewrite_ev_class`);
`systable_beginscan` with `indexOK=true` has **no seq-scan fallback**, so zero
index entries means `rd_rules = NULL`; the 42809 is then raised at
`postgres/src/backend/optimizer/util/plancat.c:139-149`.
goopg side: `internal/executor/sys_pg_rewrite.go:90-107` (`writeViewRewriteRow`
discards the TID); the fix pattern is `internal/executor/sys_pg_attrdef.go:87-95`;
the key encoder to port is `pgBuildIndexTupleOidNameKey`,
`internal/initdb/btree_index_bootstrap.go:1573-1597`.

Why `pg_get_viewdef` works while `SELECT * FROM view` does not:
`ruleutils.c:336`/`:834` run `SELECT * FROM pg_catalog.pg_rewrite WHERE
ev_class = $1 AND rulename = $2` through SPI — an ordinary planned query that
seq-scans the tiny heap. The heap row is fine; only the index path is blind.

**Subtasks:**
- S5.1 `internal/executor/sys_catalog_index_insert.go`: add
  `buildIndexTupleOidNameKey(heapBlk, heapOff, oid, name)` + `cmpKeyOidName`,
  porting the bootstrap layout exactly (8-byte header; LE uint32 oid at [8..11];
  zero-padded 64-byte `NameData` at [12..75]; MAXALIGN to 80).
- S5.2 Add `insertPgRewriteOidIndexEntry` (2692, reuse
  `buildIndexTupleOidKey`/`cmpKeyUint32`) and
  `insertPgRewriteRelRulenameIndexEntry` (2693).
- S5.3 `writeViewRewriteRow`: capture the TID from `writeHeapRowCanonical` and
  call both, mirroring `sys_pg_attrdef.go`.
- S5.4 `internal/executor/sys_catalog_postgres_db_mirror.go`: add 2692 and 2693
  to `mirroredOIDs` beside the existing `pgRewriteRelOID // 2618` (:172).
  **Omitting this repeats blocker #8 exactly** — the standby reads `base/5`, and
  an unmirrored index leaves it unindexed there.
- S5.5 Assert the DROP/recreate cycle: `stampViewRewriteRows`
  (`sys_pg_rewrite.go:205-211`) only stamps xmax and leaves stale index entries,
  which is what PG does too (visibility filters them) — verify, do not assume.

**Verify:** extend `internal/testport/e2e_failover_goopg_to_pg_test.go` after the
existing `pg_get_viewdef` assertions (:448-464) with a row-level probe on the
promoted PG: `SELECT count(*) FROM b5c_view` must equal the count over its base
table. That assertion fails 42809 today and is the precise gate.
**Gates:** `go test -run '^TestE2E_FailoverGoopgToPG$' ./internal/testport/` + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0005-pg-rewrite-runtime-index-maintenance.md`.

### S6 — Flip `relhasrules=true` for the six nailed system views (est ~1-2 loops, RISKY)

**Sources:** `internal/initdb/initdb.go:5809-5817` — `relHasRules = true` is
present but **commented out** (`:5817`), with a rationale ("Needed until the ev_action
format is fully compatible with the running PG18 version") that is now stale;
`RelationBuildDesc` short-circuits on `relhasrules` at `relcache.c:1250-1256`.
The six views (OIDs 12100, 12102-12106: `pg_stat_wal_receiver`,
`pg_stat_replication`, `pg_stat_recovery_prefetch`, `pg_stat_subscription`,
`pg_replication_slots`, `pg_stat_replication_slots`) already have on-disk
pg_class + PG-faithful pg_attribute rows (`internal/initdb/relcache_init.go:688`,
`:693-697`, `:2597-2620`) and verbatim upstream `ev_action` blobs with **both**
indexes 2692 and 2693 bootstrapped (`internal/initdb/pg_rewrite_bootstrap.go`,
`internal/initdb/btree_index_bootstrap.go:1610`, `:1659`). The hard part is done;
this slice is one uncommented line plus one landmine.

**The landmine, measured:** `internal/initdb/pg_stat_replication_slots_ev_action.dat`
contains `:relid 12261` **twice** (in the `RANGETBLENTRY` and in the
`RTEPERMISSIONINFO`). 12261 is *upstream PG's* OID for the `pg_replication_slots`
view; goopg assigns it **12105**. The bootstrap file's header comment
(`pg_rewrite_bootstrap.go:15-17`) claims "no view-side relid appears in the tree
… so no OID rewriting is needed" — true for the other five, false for this one.
The instant `relhasrules` flips, opening `pg_stat_replication_slots` makes PG
try to open relation 12261, which does not exist. The other blobs were checked
and are clean (`pg_stat_replication` → only 1260 pg_authid;
`pg_replication_slots` → 1262 pg_database; `pg_stat_subscription` → 6100
pg_subscription; the other two → none).

**Subtasks:**
- S6.1 Fix the blob first: rewrite `:relid 12261` → `12105` in both places, **or**
  repin the view OIDs per S8. Whichever S8 decides — if S8 has not landed, patch
  the blob and let S8 revisit.
- S6.2 Add the invariant guard test (this is acceptance item 5): scan every
  `internal/initdb/*_ev_action.dat` for `:relid` values; fail on any that is
  neither a pinned catalog OID nor a goopg-assigned view OID. It must fail on
  the unfixed blob.
- S6.3 Uncomment `relHasRules = true` at `internal/initdb/initdb.go:5817` and
  replace the stale comment with the real reason.
- S6.4 Invert the lock-in test
  `internal/initdb/pg_stat_wal_receiver_nailed_test.go:111-118`
  (`row[20].BoolValue()` must now be true) and rewrite its rationale. **Do not
  assume it is the only one:** a bootstrap pg_class byte change plausibly moves
  more. `grep` the whole 12100-12111 band (42 references across 6 files per
  `0131-0008`) — at least `internal/initdb/pg_replication_views_nailed_test.go`,
  `catalog_heap_reload.go:617`/`:674` and `internal/estimateaudit/parity_test.go`
  are in range — and re-run `internal/initdb` + `internal/estimateaudit`.
- S6.5 Probe `reltype`: `relcache_init.go:693-697` gives all six `RelType: 2249`
  (RECORDOID) where real PG creates a per-view composite `pg_type` row. Plain
  SELECT after rule expansion likely does not touch it — probe, do not assume;
  ledger if it diverges.
- S6.6 **Risk control:** if the first flip errors out a backend, flip the six one at
  a time. The blobs are independent and a bad one takes the whole backend down
  (`TupleDescInitEntry` / `populate_compact_attribute_internal` FATAL at
  `tupdesc.c:105` is the known shape from M0106).

**Verify:** restore `waitForPhysicalStreamingPGtoGoopg`
(`internal/testport/e2e_pg183_standby_full_cycle_test.go`) to query the
`pg_stat_replication` **view** instead of the SRF-join workaround that
AI-20260810-011258-003 installed. Reverting that harness downgrade is the gate.
**Gates:** `go test -v -run '^TestE2E_PGStandbyFullCycle$' ./internal/testport/`
(~40 s) + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0006-system-view-relhasrules-flip.md`.

### S7 — `ev_action` capture tooling + invariant gate (est ~3 loops)

**Sources:** the six existing blobs and their `//go:embed` wiring
(`internal/initdb/pg_rewrite_bootstrap.go:29-45`, heap writer at `:221`).

Makes S9 mechanical rather than artisanal. A script under `scripts/` runs a
throwaway real PG 18.3 initdb, queries `pg_rewrite` joined to
`pg_class`/`pg_attribute` for a named view set, and emits (a) the `.dat` blobs,
(b) the `nailedAttr` tables, (c) the `nailedRel` entries — applying an
OID-mapping table to relids inside the blobs.

**Verify:** re-run it against the existing six and assert byte-identical output
to the committed `.dat` files and the hand-written attribute tables. That is a
real oracle test, and it independently re-surfaces the 12261 bug if S6 chose
blob-patching over OID-pinning.
**Gates:** the oracle test + UNITS.
**Design doc:** `docs/design/0131-0007-ev-action-capture-tooling.md`.

### S8a / S8b — System-view OID policy (est ~1-2 loops, DECISION; SPLIT after review)

**Sources:** the 12261 finding; `system_views.sql` is full of view-on-view
chains (`pg_stat_sys_tables` → `pg_stat_all_tables`, the `pg_statio_*` family),
and **any view built on another view carries that view's OID inside its
`ev_action`**.

Decide: pin goopg's system-view OIDs to upstream PG 18.3's initdb-assigned
`12xxx` values, or commit to an OID-rewriting step over captured blobs. Pinning
is strongly preferred once view-on-view enters scope. Little code — a policy, a
table, and a guard test — but expensive to reverse, so it must land **before**
S9 or the captured corpus needs redoing.

**`0131-0008` resolves this and recommends pinning, on stronger grounds than
this plan assumed.** The "pinning might collide with goopg's own allocator"
objection is **void**: `catalog.FirstUserOID = 16384` (`internal/catalog/catalog.go:3604`)
is the floor for every dynamically minted OID, and nothing in goopg allocates
into 12000..16383 — that band is 100% hand-written constants (42 references
across 6 files). The only real cost of pinning is PG-version coupling. Option B
is also worse than it looks: numeric-range detection of view refs is unsound
because `RTEPERMISSIONINFO` carries `:relid` too (which is exactly why 12261
appears **twice** in one blob), so rewriting *still* needs the mapping table.
Measured: `system_views.sql` defines 80 views with 14 view-on-view edges, 12 of
them a bare `SELECT * FROM <base>`.

**Verify:** a test asserting every `nailedRel` with `RelKind=='v'` carries the
OID upstream PG assigns, checked against a manifest produced by S7's tool.
**Gates:** the new test + UNITS.
**Design doc:** `docs/design/0131-0008-system-view-oid-policy.md`.

### S9 — Widen the on-disk system-view corpus (est ~many loops, LARGE)

**Sources:** goopg's ~118 system/information_schema relations are **virtual**
(`catalog.Table{Virtual: true, VirtualRows: …}`, `internal/catalog/catalog.go:335-342`;
materialised into a `Values` node at `internal/executor/operators.go:61-63`),
registered by `registerSystemTables()` (catalog.go:7980),
`registerInformationSchemaTables()` (catalog.go:11603), and ~25 `register…View`
calls (`internal/initdb/open.go:1885-2260`). They have **no pg_class heap row** —
`catalog.go:7025-7034` skips them explicitly — so a real PG asking for
`pg_stat_activity` or any `information_schema.*` gets **42P01**, not 42809.

**Do not attempt to generate `ev_action` from `internal/pgnodes`.** Its IR
supports 21 node tags and `RangeTblEntry` hard-rejects anything but
`rtekind=0` (`internal/pgnodes/readfuncs_query.go:324-325`); there is no
`RangeTblFunction`, `JoinExpr`, `Aggref`, or `SubLink`, and `ResolveViewQuery`
rejects joins, subqueries, `*`, GROUP BY/ORDER BY/LIMIT/DISTINCT/set-ops/CTEs
(`resolver_query.go:109-132`, `:191-201`). Measured: `pg_stat_replication`'s real
`ev_action` is `rtekind 3` ×2 (RTE_FUNCTION) + `rtekind 2` ×2 (RTE_JOIN) +
`rtekind 0` ×1. Reaching `system_views.sql` coverage through the resolver means
reimplementing PG's parse analyzer. **Capture is the mechanism** and is already
the established one.

**Sub-slice order, cheapest first:**
- S9.1 Views over SRFs only, no view dependencies — the `pg_stat_get_*` family
  (`pg_stat_slru`, `pg_stat_archiver`, `pg_stat_bgwriter`, `pg_stat_checkpointer`,
  `pg_stat_wal`, `pg_stat_io`, `pg_stat_ssl`, `pg_stat_gssapi`) **plus
  `pg_settings`** (corrected: it is `FROM pg_show_all_settings()`,
  `system_views.sql:604` — SRF-only). Precondition per view: every referenced
  `pg_proc` OID must exist in goopg's seed
  (`internal/initdb/pg_proc_seed_data.go`) — **largely already discharged**: the
  seed holds 3397 entries, exactly upstream `pg_proc.dat`'s count, and all
  sampled S9.1 SRFs are present with `AllArgTypes` populated.
- S9.2 Views over real catalogs — `pg_tables`, `pg_views`, `pg_indexes`,
  `pg_roles`, **and `pg_stat_activity`** (corrected: `system_views.sql:878`
  joins `pg_stat_get_activity(NULL)` against `pg_authid` **and** `pg_database`,
  so it carries two `:relid`s and is not SRF-only). `pg_views` does not exist in
  goopg in any form.
- S9.3 View-on-view chains — the `pg_stat_*_tables` / `pg_statio_*` families.
  This is where S8's policy pays off.
- S9.4 `information_schema` (65 views) — effectively a separate milestone: needs
  the namespace, its domains (`sql_identifier`, `cardinal_number`, …) and its
  helper functions on disk first. Expected to be **deferred with a ledger row**.

**Known ceilings and unknowns (all must be ledgered, not assumed away):**
- **TOAST ceiling ~30 KB raw.** Measured on the existing blobs: largest is
  `pg_stat_replication` at 27670 B raw → 4235 B as a pglz varlena, comfortably
  inline (max heap tuple ≈ 8160 B), and `pglzVarlenaDatum`
  (`internal/initdb/pglz.go:36-47`) already handles this. The `pg_rewrite` TOAST
  relation (2838/2839) is not on the critical path **until a capture overflows** —
  scope it the moment one does.
- **`pg_proc` signature drift.** A captured blob pins `funcid`,
  `funcresulttype`, `funccoltypes`; any disagreement with goopg's `pg_proc` is a
  runtime type error or worse. Per-view check, not a blanket assumption.
- **Dual definitions — worse than "types".** `pg_stat_replication` exists both
  as a virtual view (`internal/initdb/replication_views.go:34`) and as an
  on-disk nailed rel (`relcache_init.go:693`). The virtual one has **24**
  columns, all `text` (20 upstream + `slot_name` + 3 goopg ring counters); the
  on-disk one has **20** with PG-faithful types. **Count and type both
  diverge.** The pattern is proven for one view and has not been stress-tested
  at scale.
- **Two unmeasured `ev_action` shapes** (found while writing `0131-0009`, not in
  the original plan): none of the six captured blobs contains `RTE_RESULT` or
  `LATERAL`. `pg_stat_bgwriter`/`pg_stat_checkpointer` have no `FROM` at all
  (RTE_RESULT), and `pg_statio_all_tables`/`pg_stats_ext` use `LATERAL`. Both
  land in S9.1/S9.3 and neither is known to round-trip.

**Gates:** per sub-slice — the E2E view assertion extended to the new views,
plus UNITS + SMOKE + DS05 (the virtual/on-disk duality touches the planner's
relation resolution).
**Design doc:** `docs/design/0131-0009-system-view-corpus-widening.md`.

---

## Theme D — Hygiene

### S10 — Retire `copyInitFiles`; correct the record (est ~1 loop)

**Sources:** function at `internal/testport/e2e_failover_goopg_to_pg_test.go:808-844`;
call sites at `e2e_pg183_standby_full_cycle_test.go:98-99`,
`e2e_checksum_replication_test.go:121-122`,
`e2e_failover_goopg_to_pg_test.go:153-154`.
Removal proof: `postgres/src/backend/access/transam/xlog.c:5633`
(`RelationCacheInitFileRemove()` in `StartupXLOG`, unguarded by `InRecovery`,
before any replay and before hot-standby connections);
`relcache.c:6891-6929` (unlinks `global/` and every all-digit `base/` subdir, so
the extra `base/5` copy is wiped too); the only reader
`load_relcache_init_file` (`relcache.c:6167`) is reached from `InitPostgres`,
which in the standalone path runs *after* `StartupXLOG` (`postinit.c:787` vs
`:818`). None of the three tests uses `--single`, `--boot`, or `--check`.
Provenance: adding commit `30b0716f` (2026-05-17) whose own body says *"PG's
load_relcache_init_file still rejects the file silently"*; superseded next day by
`c09d519e` (2026-05-18, "step 3cq proper"), documented at
`internal/initdb/pg_type_bootstrap.go:322-331`; cargo-culted onward in
`c31afd94` (2026-06-13) and `2da52113` (2026-08-09).
goopg's own BASE_BACKUP already excludes the file
(`internal/server/basebackup.go:113`), matching upstream `basebackup.c:203` —
so `copyInitFiles` re-introduces a file both implementations deliberately drop.

**Subtasks:**
- S10.1 Delete the function (and its doc comment) and the three call sites with
  their one-line comments. No import churn expected (`os`/`filepath` stay used);
  the compiler confirms.
- S10.2 Re-attribute the two mis-stating comments:
  `e2e_failover_goopg_to_pg_test.go:507` (*"the copied pg_internal.init caches a
  ruleless relcache entry for the view"* — provably wrong) and the prose mention
  at `e2e_pg183_standby_full_cycle_test.go:340`. The correct attribution is the
  init file **PG rebuilds itself** from goopg's unindexed `pg_rewrite`.
- S10.3 Append a deferral-ledger row correcting #428/#995/#996: the
  `pg_internal.init` attribution is unimplementable (the format carries no
  rules, views never pass `RelationIdIsInInitFile`, and the file is deleted at
  `StartupXLOG`), and the index OID is **2693**, not 2620 (2620 is `pg_trigger`).
  Name S5/S6 as the real fix.
- S10.4 Update `docs/design/0130-0002-pg-class-heap-persistence.md` — **Guard
  #2 only** (corrected after review). Guard #2 (*"Reverse path not yet
  implemented"*) is stale relative to that doc's own later sections. Guard #1
  (*"PG started against goopg data dir … Needs E2E PG-attach test — not yet
  implemented"*) is **still true** until S4 lands, so **S4 owns it, not S10**.
  S10 also corrects item 3 of the "Remaining for full reverse-path parity" list,
  which names a blocker ("needs a test-harness PG instance lifecycle
  (M0130-S10)") that no longer exists — the harness landed; the real obstruction
  was S1's GUC gap.
- S10.5 File the three missing ledger rows for that list's items 1–3. They have
  never had one, which the inherited filing rule does not permit.
- S10.6 Note in the ledger the two unread/unsupported gaps this milestone
  deliberately does not close: `pg_filenode.map` is write-only (re-arm trigger
  already recorded at row #388), and `replayDecodedXLogRecord`
  (`internal/wal/recovery.go:2207`, `default:` arm at `:2525`) handles rmids
  0,1,2,3,4,5,7,8,9,10,11,15,128 — missing **6 MultiXact, 12 Hash, 13 Gin,
  14 Gist, 16 SPGist, 17 BRIN, 18 CommitTs, 19 ReplicationOrigin, 20 Generic,
  21 LogicalMessage**. Note that 6/18/19 are *not* index AMs and do appear in
  ordinary PG workloads, so `0130-0002`'s framing ("requires implementing the
  corresponding index AMs") understates the surface.

**Gates:** E2E family stays green + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0010-copyinitfiles-retirement.md`.

---

---

## Theme F — Crash-state cluster-directory interchange (added 2026-08-11)

> **Numbering note.** Theme F occupies **S16–S29** and design docs
> **`0131-0012` … `0131-0017`**. It was drafted as S11–S24 / `0131-0011`…`0131-0016`
> and renumbered before commit: a concurrently-running Ralph loop had already
> landed `M0131-S11` (HOT flags → `t_infomask2`, doc `0131-0011`) and filed
> `M0131-S12`–`S15` from S3/S4's measured findings. Any external note referring
> to Theme F by the old ids means the slice with the same title here.


**Why this theme exists.** Themes A and B deliberately scope both cold starts to
a **cleanly shut down** source directory — `0130-0002` §"WAL replay constraint"
says the reverse path requires one, and S3/S4 assert `DB_SHUTDOWNED` before the
handover. That leaves the milestone's headline claim half-true: two engines are
not really interchangeable on a directory if the interchange only works when the
previous engine exited politely. Theme F removes the precondition in both
directions.

**Two investigations at filing (2026-08-11) found that this is not merely a
capability gap. Each direction already loses committed data today**, on
directories goopg is *supposed* to handle. Those two bugs are S16 and S17 and
they must land before anything else in the theme.

### The reverse safety bug (S16) — unknown record read as end-of-WAL

`internal/wal/xlog_record.go:218-220` rejects only rmids in the open interval
`(15, 128)`:

```go
if h.Rmid > MaxKnownRmgr && h.Rmid < RmgrGoopgCustomBase {
    return h, fmt.Errorf("%w: unknown rmid=%d", ErrInvalidRecordHeader, h.Rmid)
}
```

`MaxKnownRmgr = RmgrSeq = 15`. PG 18's real maximum is 21. So rmids **16 SPGist,
17 BRIN, 18 CommitTs, 19 ReplicationOrigin, 20 Generic, 21 LogicalMessage** fail
*header decode*, inside the reader — and `readAllPageAware`
(`internal/wal/reader.go:148-165`) treats a header-decode failure as **end of
WAL**, not as an error. `endOfWAL` (`reader.go:212-241`) is a `slog.Warn` and
nothing more, and the guard `if int64(len(stream)-off) <= segSize { break }`
suppresses even that for any tail in the last segment — which is where a crash
tail always is.

Consequences, in order: every record after the first rmid-16..21 record is
silently dropped; `ReplayRecords` reports success; and `detectWritePos` →
`scanLastSegmentEnd` (`writer.go:1287`, `:1494`) stops at the same point, so
goopg **appends new WAL over the durable-but-unreplayed records** and the loss
becomes permanent on the first write. A single `pg_logical_emit_message` in a PG
crash tail is enough.

Rmids 6 MultiXact, 12 Hash, 13 Gin, 14 Gist take the *other* path — they decode,
reach `replayDecodedXLogRecord`'s `default:` (`recovery.go:2525`) and error out,
so startup refuses. That half is safe.

A third silent path: the btree `default:` arm (`recovery.go:2516-2523`) restores
each block from its FPI and returns `applied=true`, `continue`-ing over any
block with no image. That premise holds for goopg-emitted records; PG emits an
FPI only on a page's first touch after a checkpoint, and never with
`full_page_writes=off`. A PG `XLOG_BTREE_DEDUP`/`_DELETE`/`_INSERT_UPPER` is
therefore **silently discarded while reporting success** — index corruption with
no diagnostic.

### The forward safety bug (S17) — `DB_IN_PRODUCTION` is never stamped

goopg writes `pg_control.state` from only two runtime sites, the checkpointer
(`internal/wal/checkpointer.go:736-748`). There is **no `State` write anywhere
in the server-startup path**. `initdb` stamps `DB_SHUTDOWNED`, and the
checkpointer's first tick is one full `checkpoint_timeout` (PG default 300 s)
after start — `ticker := time.NewTicker(c.cfg.Interval)` at `checkpointer.go:440`,
with no leading immediate checkpoint.

So a SIGKILL inside the first 300 s of a clean start leaves `pg_control` saying
`DB_SHUTDOWNED`. PG then falls through ALL THREE arms of
`postgres/src/backend/access/transam/xlogrecovery.c:924-936`, leaves
`InRecovery` false, **skips `PerformWalRecovery()` entirely**, and resumes
inserting WAL at the end of the shutdown checkpoint — overwriting goopg's tail.
Every committed transaction in it is gone, with no PANIC and nothing alarming in
the log.

goopg's own code already documents the inverse belief as if it were true, at
`internal/initdb/open.go:3195-3198`: *"immediate shutdown: skipping final
checkpoint … pg_control left at DB_IN_PRODUCTION; recovery on next start"*. That
holds only if an online checkpoint has already run.

### What the two directions need, and what they do not

The forward direction is in much better shape than expected, and the milestone
should not spend effort re-planning what is already fine:

- **WAL segment zeroing is a non-issue** — and goopg is *safer* than upstream
  here. `wal_init_zero` BootVal is `on` (`internal/config/defaults.go:399`), and
  `recycleSegmentFile` (`internal/wal/writer.go:2369-2379`) renames **and then
  zero-fills**, which upstream's `InstallXLogFileSegment` does not. A goopg
  crash tail is followed by zeros, so PG sees a clean `xl_tot_len == 0` /
  `xlp_magic == 0` end-of-WAL at LOG level.
- **`CheckRequiredParameterValues` is entirely a no-op in crash recovery** —
  every branch is gated on `ArchiveRecoveryRequested` (`xlog.c:5429`, `:5442`).
  Drop it from scope.
- **`pg_twophase`, `pg_commit_ts`, `pg_multixact` being empty are all fine
  forward.** `PrescanPreparedTransactions` over an empty dir is a no-op and
  returns `nextXid`, which is exactly what `StartupSUBTRANS` wants;
  `StartupCommitTs` is gated on `track_commit_timestamp`, which goopg writes
  false; and `TrimMultiXact` succeeds because initdb's zeroed
  `pg_multixact/offsets/0000` placeholder is load-bearing.
- **`checkPointCopy.oldestActiveXid` frozen at 0 is not a blocker** — it is read
  only on the hot-standby path (`xlog.c:5835`, gated on
  `ArchiveRecoveryRequested && EnableHotStandby`). Crash recovery derives it from
  `PrescanPreparedTransactions` instead.
- **Unlogged relations are a "too durable" divergence, not corruption.** goopg
  never creates `_init` forks, so PG's `ResetUnloggedRelations` is a no-op and a
  goopg unlogged table survives where PG would have truncated it. Wrong, but not
  a crash-recovery blocker — ledger it, do not absorb it here.

Conversely the reverse direction is **worse** than the missing-rmgr list
suggests. The bigger cost is opcode gaps *inside* rmgrs goopg already claims to
handle (S21): `XLOG_HEAP2_MULTI_INSERT` (every COPY), `XLOG_HEAP2_VISIBLE`
(every VACUUM), `XLOG_HEAP_LOCK` (every `SELECT … FOR UPDATE`),
`XLOG_HEAP_TRUNCATE`, `XLOG_CLOG_ZEROPAGE` (every 32768 XIDs),
`XLOG_SMGR_TRUNCATE`, `XLOG_STANDBY_LOCK`, `XLOG_XACT_ASSIGNMENT` (**not** every subtransaction —
gated on `isSubXact && XLogStandbyInfoActive()` AND `nUnreportedXids >= 64`,
`postgres/src/backend/access/transam/xact.c:751-782`, so a single SAVEPOINT
emits none), and seven btree opcodes. None of these is exotic; all are
ordinary heap+btree traffic.

### S16 — Fail closed: an unrecognised record is not end-of-WAL (est ~1 loop, LAND FIRST)

**This is a live data-loss bug fix, not a feature.** It is worth landing even if
the rest of Theme F never does.

**Subtasks:**
- S16.1 `internal/wal/xlog_record.go:218-220` — raise the structural bound to
  PG's real `RM_MAX_BUILTIN_ID` (21) so 16..21 decode as records. Keep rejecting
  `(21, 128)`.
- S16.2 `internal/wal/reader.go:102-211` — split the two meanings collapsed into
  `endOfWAL`. A torn/zero/CRC-failed tail is end-of-WAL; a *decodable-but-
  unhandled* record is an error the caller must see. Return an explicit stop
  reason from `readAllPageAware` and surface it through `ReadAll`. Delete the
  unconditional `len(stream)-off <= segSize` silent breaks (`:157`, `:169`,
  `:186`, `:198`) that suppress even the warning.
- S16.3 `internal/wal/recovery.go:2516-2523` — the btree `default:` arm applies
  FPIs only if **every** mutated block carries `ImageApply`; otherwise
  `unsupportedDecodedXLogRecord`. Converts silent corruption into refusal.
- S16.4 `internal/wal/recovery.go:2245-2249` — audit the `RmgrXLog` `default:
  return false, nil`. Keep the no-op for NOOP/SWITCH/CHECKPOINT_*/
  RESTORE_POINT/FPW_CHANGE/BACKUP_END/END_OF_RECOVERY/CHECKPOINT_REDO; error on
  genuinely unknown opcodes. **`XLOG_NEXTOID` 0x30 is NOT a benign no-op** —
  `xlog_redo` sets `nextOid` exactly (`xlog.c:8292-8308`), so dropping it lets
  goopg re-issue OIDs a PG already allocated after the checkpoint; it moves to
  S21a as real work.
- S16.5 **substantially already done** — `internal/wal/pg_xlog_decode.go:345-350`
  (the *block-header* decoder, not `decodeXLogBlockImage`) already returns
  "compressed PostgreSQL backup block images are not supported yet". The residual
  defect is a *symptom of S16.2*: that error is swallowed by the `<= segSize`
  guard and reported as a clean end-of-WAL. Keep S16.5 only to wrap it in
  `ErrCorruptRecord` and to test that the error now escapes.

**Gates:** a unit test feeding `readAllPageAware` a valid record, then an rmid-18
record, then more valid records, asserting a non-tail stop (today: 1 record, no
error). A unit test that a PG-shaped `XLOG_BTREE_DEDUP` with block data and no
image now errors. UNITS + RACE (`internal/wal`) + SMOKE.
**Design doc:** `docs/design/0131-0013-wal-reader-fail-closed.md`.

### S17 — goopg stamps `DB_IN_PRODUCTION` at startup (est ~1 loop, LAND FIRST)

**The other live data-loss bug, and the cheapest fix in the milestone.** Nothing
else in the forward direction is reachable until PG actually enters recovery.

**Subtasks:**
- S17.1 Stamp `State = DB_IN_PRODUCTION` and a current `Time` durably in the
  runtime open path (`internal/initdb/open.go`, near where
  `internal/server/server.go:677` writes the PID file), **before the first client
  is accepted**. Mirrors `xlog.c:6204-6205`.
- S17.2 Fix the misleading comment at `internal/initdb/open.go:3195-3198`, which
  asserts a postcondition the code does not establish.
- S17.3 Confirm goopg's own restart path is unaffected (it does not read `State`
  today — S20 changes that, so the two must agree).

**Gates:** a unit test — `Init` → `Start` with a long `checkpoint_timeout` →
`control.ReadControlFile` asserts `DBStateInProduction` **before any checkpoint
can have run**; then SIGKILL → still `DBStateInProduction`. No PG needed.
**Design doc:** `docs/design/0131-0014-pgcontrol-runtime-state-and-durability.md`.

### S18 — pg_control writer durability + full checkpoint-struct coverage (est ~2 loops)

**Sources:** `internal/control/pgcontrol.go:213-237` (`UpdateControlFile` uses
`os.WriteFile`, i.e. `O_TRUNC` + write with **no fsync**, where upstream's
`update_controlfile` (`postgres/src/common/controldata_utils.c:189`) opens
`O_RDWR`, writes all 8192 bytes and fsyncs); `internal/wal/checkpointer.go:757-759`
(`ThisTimeLineID`/`PrevTimeLineID`/`fullPageWrites` hardcoded).

**Subtasks:**
- S18.1 Rewrite `UpdateControlFile` as `O_RDWR` + `WriteAt` + `Sync`. A SIGKILL
  inside the current `O_TRUNC` window leaves a **zero-length pg_control** and PG
  PANICs `could not read file "global/pg_control": read 0 of 296`.
- S18.2 Extend `ControlFileData` with the never-decoded `checkPointCopy` fields:
  `nextMulti`(76), `nextMultiOffset`(80), `oldestXid`(84), `oldestXidDB`(88),
  `oldestMulti`(92), `oldestMultiDB`(96), `oldestCommitTsXid`(112),
  `newestCommitTsXid`(116), `oldestActiveXid`(120) — decode **and** encode.
  (Today they survive only by `UpdateControlFile`'s read-modify-write.)
- S18.3 Thread the **live** TLI into `Checkpointer.Config` and
  `UpdateControlCheckpoint`. Today both hardcode `1`, so the very next checkpoint
  after M0130-S8.5's `finalizePromotion` stomps the TLI back to 1 while segments
  are named for TLI 2 — PG then PANICs `could not locate a valid checkpoint
  record`. Thread `full_page_writes` the same way (the GUC reaches the buffer
  pool at `cmd/goopg/main.go:719-721` but not pg_control).
- S18.4 Stop hardcoding `nextMulti=1`/`oldestMulti=1`/commitTs in
  `encodeCheckPointStruct` (`internal/wal/recovery.go:783-794`); write live
  values and the currently-never-set `nextMultiOffset`/`oldestMultiDB`. Note the
  existing initdb-vs-checkpoint divergence (`oldestMultiDB` 1 vs 0) is a bug
  either way.

**Gates:** golden-byte round-trip against the real `pg_controldata` binary in
`postgres/local_install/bin`; a test asserting the writer cannot produce a
zero-length file; a promotion test asserting the TLI survives the next
checkpoint. UNITS + SMOKE.
**Design doc:** `docs/design/0131-0014-pgcontrol-runtime-state-and-durability.md`.

### S19 — Validate `xlp_pageaddr`/`xlp_tli`; stop trusting recycled segments (est ~2 loops, RISKY)

**This also fixes the CLEAN reverse path (S3), not only the crash path.**
goopg zero-fills recycled segments; **PG does not** — `InstallXLogFileSegment`
(`postgres/src/backend/access/transam/xlog.c:3559`) is a bare `durable_rename`.
A real PG's `pg_wal` therefore contains full-size future segments packed with
stale, CRC-valid records from a previous WAL cycle, and PG's only defence is the
`xlp_pageaddr` check at `postgres/src/backend/access/transam/xlogreader.c:1324-1340`.
goopg decodes `PageAddr` (`internal/wal/xlog_page.go:91`, `:144`) and writes it
(`xlog_emit.go:125`) but **never compares it** — grep for `PageAddr` outside
those three sites returns nothing.

**Subtasks:**
- S19.1 `internal/wal/reader.go` — **there is no per-page header decode to hook**:
  the walk's only `DecodeXLogPageHeader` is `:119` (the first-page contrecord
  skip), and the per-page block just computes `pageHeaderSizeAt` + `isZeroBytes`.
  S19.1 must therefore ADD a per-page decode, then require
  `hdr.PageAddr == baseOffset + off` and a consistent `hdr.TLI`; otherwise stop
  as end-of-WAL. Consider validating `xlp_sysid` against
  `pg_control.system_identifier` while here (S2 made that readable).
- S19.2 `internal/wal/writer.go:1538-1549` (`scanLastSegmentEnd`) — the **sibling
  path**, per Hard-won Rule #2. Also revisit `detectWritePos`'s "non-final
  segments are fully used" assumption (`:1435-1440`) and its phantom-drop loop
  (`:1409-1417`): a PG recycled segment scans as non-empty, so the loop breaks
  immediately and `writePos` lands tens of MB past the true end of WAL, inside
  garbage. **The reader half is conditional on the last page ending exactly on a
  boundary; this writer half is not conditional and should be reproducible
  directly.**

**Risk:** this is the slice most likely to break goopg's own restart path. Land
it with the pgbench smoke and a crash-restart test, not unit tests alone.
**Gates:** a fixture directory with a real PG segment followed by a hand-crafted
recycled segment of stale valid records; assert `ReadAll` stops at the real end
and `detectWritePos` returns the real end. UNITS + RACE + SMOKE + SPOT.
**Design doc:** `docs/design/0131-0013-wal-reader-fail-closed.md`.

### S20 — pg_control-driven recovery in goopg: DBState, redo start, hygiene (est ~2 loops)

goopg **never reads `pg_control.State`.** The constants exist
(`internal/control/pgcontrol.go:29-36`) and the field is decoded (`:126`), but
there is no reader anywhere in `internal/` or `cmd/`; `Open()` reads pg_control
only for `DataChecksumVersion` (`internal/initdb/open.go:292`) and later
`nextOid`/`nextXid` (`:1188-1206`). goopg treats `DB_IN_PRODUCTION` and
`DB_SHUTDOWNED` identically, and finds its redo start by **scanning the WAL** for
the last checkpoint (`internal/wal/recovery.go:4408-4432`) rather than reading
`checkPointCopy.redo`.

**Subtasks:**
- S20.1 Read `State` before replay. If it is not
  `DBStateShutdowned`/`DBStateShutdownedInRecovery`, log "crash recovery
  required", set `DB_IN_CRASH_RECOVERY`, replay, then `DB_IN_PRODUCTION` and
  force an end-of-recovery checkpoint.
- S20.2 Let `replayStart` take an explicit redo LSN from
  `pg_control.CheckPointCopyRedo` when the caller has one; keep the scan as the
  fallback for goopg-authored dirs. Teach `isCheckpointRecord` about
  `XLOG_CHECKPOINT_REDO` (0xE0, PG17+).
- S20.3 Add a `RelationCacheInitFileRemove` equivalent — unconditionally unlink
  `global/pg_internal.init` and every `base/*/pg_internal.init` **before** replay,
  then let goopg's existing regeneration rebuild them. Today goopg only unlinks
  reactively (`internal/catalog/relcache_inval.go:14-21`), so a PG-authored init
  file survives into a goopg session. (Note the pleasing symmetry with S10: goopg
  must do to PG's init file exactly what PG does to goopg's.)
- S20.4 Seed `multixact.NewStoreAt` from `pg_control.nextMulti` — the seam exists
  at `internal/multixact/store.go:88` and **has no caller**;
  `cmd/goopg/main.go:560` always calls `NewStore()`.
- S20.5 Decide and document the `minRecoveryPoint` policy: for crash recovery
  (not archive recovery) PG leaves it invalid, and goopg should not invent one.

**Gates:** unit tests on the new pg_control round-trip and on a `DB_IN_PRODUCTION`
goopg dir producing the recovery banner + end-of-recovery checkpoint. UNITS + SMOKE.
**Design doc:** rides `docs/design/0131-0014-pgcontrol-runtime-state-and-durability.md`.

### S21 — Opcode coverage inside the already-handled rmgrs (est ~6-8 loops, LARGE — split 21a/21b)

**The bulk of the reverse work, and the part `0130-0002` does not anticipate.**
goopg's per-rmgr arms cover only the opcodes goopg *emits*; every other PG opcode
hits `unsupportedDecodedXLogRecord` (hard error) or, for btree, S16.3's new
refusal. All of the following are ordinary heap+btree traffic.

**S21a — non-btree (est ~2 loops):**
- **FIRST, the mask bug — highest-value item in the slice and absent from the
  original filing.** `internal/wal/recovery.go:2439` masks RM_HEAP2 with
  `XLRRmgrInfoMask` (0xF0) while RM_HEAP correctly uses `xlogHeapOpMask` (0x70).
  But `XLOG_HEAP_OPMASK` applies to HEAP2 too, and upstream OR's
  `XLOG_HEAP_INIT_PAGE` (0x80) into MULTI_INSERT
  (`postgres/src/backend/access/heap/heapam.c:2607-2611`) — so a `COPY` onto a
  fresh page arrives as `info == 0xD0` and **would miss its case even after the
  case is added**. Fix the mask before adding any HEAP2 opcode.
- RM_HEAP (10): `XLOG_HEAP_LOCK` 0x60 and `XLOG_HEAP_CONFIRM` 0x50.
  (**`XLOG_HEAP_TRUNCATE` 0x30 needs NO redo** — upstream is explicit that
  "TRUNCATE is a no-op because the actions are already logged as SMGR WAL
  records", `postgres/src/backend/access/heap/heapam_xlog.c:1201-1208`; it needs
  a recognised case, not an implementation.) goopg has a native heap-lock replay
  (`recovery.go:4079`) to model on. Also zero-extend rather than error at
  `recovery.go:2665` when `block.Block > nblocks`, matching PG's
  `XLogReadBufferExtended`; a PG crash tail routinely references a block past
  the file's flushed length.
- RM_HEAP2 (9): `XLOG_HEAP2_MULTI_INSERT` 0x50 (every COPY) and
  `XLOG_HEAP2_VISIBLE` 0x40 (every VACUUM) first — goopg has a native analog for multi-insert at
  `recovery.go:3842`, but **`HeapVisible` gives 0% reuse — goopg's
  `RecordKindHeapVisible` replay is an explicit no-op
  (`recovery.go:2109-2118`), not an implementation to model on**, so
  `XLOG_HEAP2_VISIBLE` is written from scratch. Then `LOCK_UPDATED` 0x60,
  `NEW_CID` 0x70 (`wal_level=logical` only, no-op is fine), `REWRITE` 0x00.
- RM_XACT (1): `ASSIGNMENT` 0x50 and `INVALIDATIONS` 0x60 → recognised no-ops,
  matching `postgres/src/backend/access/transam/xact.c:6428-6443`.
  `PREPARE`/`COMMIT_PREPARED`/`ABORT_PREPARED` → refuse loudly (2PC recovery is
  out of scope; `max_prepared_transactions` BootVal is `"0"`).
- RM_STANDBY (8): `LOCK` 0x00 and `INVALIDATIONS` 0x20 → recognised no-ops.
  Upstream's `standby_redo` returns immediately when
  `standbyState == STANDBY_DISABLED`, always true outside hot standby.
- RM_CLOG (3): `CLOG_ZEROPAGE` 0x00 — maps onto the existing `EnablePGSLRUMirror`
  page-zeroing (`internal/mvcc/clog.go:567-601`).
- RM_SMGR (2): `XLOG_SMGR_TRUNCATE` 0x20 — goopg has a native
  `replaySmgrTruncate`.
- RM_XLOG (0): apply `XLOG_FPI_FOR_HINT` 0xA0 through the existing FPI path
  (currently a silent no-op, **losing torn-page protection**); handle
  `XLOG_OVERWRITE_CONTRECORD` 0xD0.

**S21b — btree (est ~2 loops):** real redo for **six** opcodes — `INSERT_UPPER` 0x10,
`INSERT_META` 0x20, `INSERT_POST` 0x50, `DEDUP` 0x60, `DELETE` 0x70,
`META_CLEANUP` 0xE0. (**`REUSE_PAGE` 0xD0 needs NO redo**: its whole body is
`if (InHotStandby) ResolveRecoveryConflictWithSnapshotFullXid(…)`,
`postgres/src/backend/access/nbtree/nbtxlog.c:1006-1015` — recognise it.) M0130-S11's PG-identical nbtree page and
tuple work is the foundation; `postgres/src/backend/access/nbtree/nbtxlog.c` is
the reference.

**Gates:** per-opcode unit tests against fixtures captured from a real PG via
`internal/testutil/pgcluster`. Then the S28 E2E. UNITS + RACE + SMOKE + SPOT.
**Design doc:** `docs/design/0131-0015-pg-wal-opcode-coverage.md`.

### S22 — CLOG replay opcode dispatch + `subxacts[]` parsing (est ~1 loop)

A separate, second WAL pass with its own bug. `internal/initdb/xact_recovery.go:87-92`
treats **any** `RmgrXact` record with a non-zero XID as commit-or-abort:

```go
isCommit := (r.XLog.Header.Info & wal.XlogXactOpMask) == wal.XlogXactCommit
xactStampAndAdvance(clog, txnMgr, xid, isCommit)
```

So a PG `XLOG_XACT_ASSIGNMENT` (0x50), `PREPARE` (0x10) or `COMMIT_PREPARED`
(0x30) is **stamped ABORTED**. Separately, goopg never parses the commit
record's `subxacts[]` array, so **subtransactions of a committed top-level xact
are never stamped committed** — their rows stay invisible after a reverse crash
start. Dispatch on the opcode and parse the array.

**Gates:** a unit test over a captured PG commit record carrying subxacts; an E2E
whose PG workload uses a SAVEPOINT before the kill.
**Design doc:** rides `docs/design/0131-0015-pg-wal-opcode-coverage.md`.

### S23 — The cheap tail: LogicalMessage, ReplicationOrigin, Generic, CommitTs (est ~1 loop)

- **RM_LOGICALMSG (21)** — a recognised **no-op** plus an opcode check is
  byte-exact parity, not an approximation: upstream's `logicalmsg_redo`
  (`postgres/src/backend/replication/logical/message.c:83-97`) has an empty body
  with the comment *"Redo is basically just noop for logical decoding messages."*
- **RM_REPLORIGIN (19)** — touches only in-shmem state goopg has no consumer for;
  a documented no-op is defensible (~45 LOC if ever wanted for real).
- **RM_GENERIC (20)** — port `generic_redo`/`applyPageRedo`
  (`postgres/src/backend/access/transam/generic_xlog.c:451-533`, ~78 LOC). This
  is the **only** missing rmgr implementable correctly with zero AM knowledge:
  it applies an opaque `(offset, length, bytes)` delta. Reachable only from
  extensions, so optional — but cheap and complete.
- **RM_COMMIT_TS (18)** — refuse loudly when `pg_control.TrackCommitTimestamp` is
  true; no-op the ZEROPAGE/TRUNCATE records otherwise. `track_commit_timestamp`
  defaults false and **is not even a registered GUC in goopg**. The actual
  commit-timestamp data rides `xact_redo_commit`, not this rmgr, so a half
  implementation would be worse than a refusal.

**Design doc:** rides this plan (each is a single function with a unit test).

### S24 — MultiXact: durable `pg_multixact` SLRU + `multixact_redo` (est ~4 loops, LARGE/RISKY)

**RM_MULTIXACT_ID (6) is the only genuinely unavoidable missing rmgr.** It is
independent of `wal_level` and is produced by ordinary concurrency: two sessions
taking `FOR SHARE` on one row; `FOR UPDATE` + `FOR KEY SHARE` from different
xacts; an UPDATE of a row already locked by a live xact; **two concurrent
sessions inserting children that reference the same parent row** (FK RI checks
take `FOR KEY SHARE` — the most common real-world source); and VACUUM, both via
`heap_prepare_freeze_tuple` and via `TruncateMultiXact`.

goopg has a real in-memory engine (`internal/multixact/multixact.go`,
`internal/multixact/store.go`) but it is **process-local and transient**;
`pg_multixact/{offsets,members}` are created empty at initdb and never written.

**Subtasks:** a durable offsets+members SLRU modelled on
`internal/mvcc/clog_bufferpool.go` — but note its 2-bits-per-key locate math does
**not** generalise to variable-length member runs, and there is no shared SLRU
abstraction (`internal/mvcc/subxact_slru.go` already duplicates the constants;
extract one here rather than writing a third hand-roll). Then declare
`RmgrMultiXact = 6` and port `multixact_redo`
(`postgres/src/backend/access/transam/multixact.c:3481`, 4 opcodes), **carrying
the `pre_initialized_offsets_page` flag across records** (`multixact.c:383`,
`:969`, `:3500`, `:3539`) — skipping it double-zeroes a live offsets page.

**Two findings worth their own ledger rows regardless of whether this lands:**
(a) goopg's emit side stamps multi xmax with **no WAL record** at all
(`internal/executor/operators_lockrows.go:2040`, `:2126`;
`internal/executor/operators_storage.go:3468`, `:3485`) — defensible for
lock-only multis, **not** for the updater-bearing producers, so goopg's *own*
crash recovery has this defect today; (b) `internal/mvcc/visibility.go:126-146`
makes a tuple with an unresolvable multi xmax **invisible**, so a PG-authored
multi xmax silently hides rows rather than erroring.

**Scoping DECISION (recorded here, per the filing rule — not left to omission):**
**S28's workload is single-session, so S24 is DEFERRED out of M0131.** A
single-session PG workload emits no multixact record, so S16-S23 + S25 suffice
for S28 to pass. The re-arm trigger is executable, not a sentence: S28 ships a
third variant `..._concurrent` (two sessions taking `FOR SHARE` on one row, plus
concurrent FK inserts) carrying `t.Skip("re-arm trigger for M0131-S24")`.
Un-skipping it is what re-opens S24. A ledger row records the deferral.

**Scoping note:** the trigger is *concurrency*. If the Theme F E2E workloads are
single-session, S24 can be deferred with a very clear re-arm trigger and S16-S23
+ S25 suffice. Decide this explicitly rather than by omission.
**Design doc:** `docs/design/0131-0016-multixact-durable-slru.md`.

### S25 — Index-AM boundary: detect, refuse specifically, ledger (est ~1 loop)

Give rmids 12 Hash / 13 Gin / 14 Gist / 16 SPGist / 17 BRIN named arms returning
a *specific* error naming the access method and the LSN — the message an operator
can act on, unlike `unsupported xlog record rmid=13`. Add a **pre-flight scan**
that reports every distinct unimplementable rmid at once, so one start attempt
teaches "GIN and BRIN" rather than five.

Then ledger the sizes honestly: Hash ~1090 LOC / 13 opcodes; GIN ~750 / 9; GiST
~400 / 6 (**and record the FPI trap: `gistxlog.c:50-54` requires redo work even
on `BLK_RESTORED`, because the updated NSN is not in the image — FPI-only replay
is provably wrong**); SP-GiST ~940 / 8; BRIN ~320 / 6 (**mask
`XLOG_BRIN_INIT_PAGE` 0x80 off before the opcode switch — it is a flag, not an
opcode**). Record that `REGBUF_WILL_INIT` blocks structurally never carry an FPI
(20 such sites across the five AMs), which is why an FPI-only shortcut cannot
work for any of them.

**Design doc:** rides this plan.

### S26 — `pd_lsn` completeness audit on logged change paths (est ~2 loops)

PG's crash recovery replays over live data files whose pages may already be
*ahead* of the record, and skips them via
`if (lsn <= PageGetLSN(page)) return BLK_DONE;`. If any goopg **logged** mutation
dirties a page without stamping `pd_lsn` to the emitting record's end LSN, PG
double-applies — a duplicated row or a btree unique violation, not a crash.
Streaming from a base backup barely exercises this; crash recovery does so
constantly.

Audit every `Pool.MarkDirty*` variant against its `SetLSN`
(`internal/storage/bufpool.go:2091-2340`, 9 `SetLSN` sites) and every `logXxx`
hook (`internal/initdb/open.go:430-745`); add a debug assertion. Document that
`MarkDirtyHint` deliberately does **not** bump `pd_lsn`
(`internal/storage/bufpool.go:2053-2057`) and that this specific exception is
PG-faithful because hint bits are recomputable — the question is whether any
*logged* path shares it.

**Design doc:** rides this plan.

### S27 — Forward crash E2E (+ stale pidfile, torn contrecord) (est ~3 loops)

New `internal/testport/e2e_pg_crashstart_on_goopgdata_test.go`, sibling of S4's
test. Shape: `goopg init` → start → workload with a **known committed/uncommitted
split** → force one online checkpoint (`goopg checkpoint`) so `state` is
`DB_IN_PRODUCTION` and there is a real tail past redo → more committed work →
**`kill -9` via `cluster.Kill()`** (`internal/testutil/cluster/cluster.go:303-333` —
it SIGKILLs the process GROUP by pgid from the cmd handle; no PID-file read, and
it satisfies the never-`pkill -f` rule) → assert
`pg_control.State == DB_IN_PRODUCTION` → `postgres -D <same dir>` → assert the PG
log contains `redo starts at` and `redo done at`, contains **no** PANIC/FATAL,
and that any `invalid record length` / `invalid magic number` line is the *last*
WAL complaint (benign end-of-WAL) → assert every committed row is visible, the
uncommitted ones are not, and the aggregates match values captured before the kill.

Two sub-items:
- **Stale `postmaster.pid`.** A SIGKILL leaves it (`RemovePIDFile` runs only on
  the clean path, `internal/server/server.go:808-815`). PG's `CreateLockFile`
  (`postgres/src/backend/utils/init/miscinit.c:1300-1425`) reads line 0 as a pid
  and **FATALs `lock file "…" already exists`** if `kill(pid, 0)` succeeds — so
  this fails *non-deterministically*, whenever the dead PID has been recycled.
  Otherwise it unlinks and proceeds (goopg's 5-line file has no line 6, so the
  shmem check is skipped). Either make the file PG-shaped or remove it on start;
  until then the test must delete it as a documented handover step.
- **Torn contrecord.** Kill during a large `INSERT … SELECT` so the tail ends
  inside a multi-page record; assert PG logs `there is no contrecord flag at
  %X/%X` (or `invalid contrecord length`) then `successfully skipped missing
  contrecord at %X/%X` after writing its `XLOG_OVERWRITE_CONTRECORD`. **This is
  the one crash-only WAL-format path the standby lane structurally cannot
  reach** — `xlogrecovery.c:3188-3193` suppresses it under
  `ArchiveRecoveryRequested`.

**Honesty note for the design doc:** a `kill -9` does **not** exercise torn
*pages* — writes reach the page cache atomically from a reader's perspective.
Torn-page coverage needs a machine-level crash or a deliberate half-page-write
fault injector. Do not let this test be cited as FPI coverage.
**Design doc:** `docs/design/0131-0017-crash-interchange-e2e.md`.

### S28 — Reverse crash E2E (est ~3 loops)

The mirror: real `initdb` → PG → workload exercising **COPY, VACUUM,
`SELECT … FOR UPDATE`, `TRUNCATE`, a SAVEPOINT, and an index-heavy insert** (the
S21/S22 opcode set) → SIGKILL the postmaster — **`pgcluster.Kill()` is NOT a SIGKILL**
(`internal/testutil/pgcluster/cluster.go:325-336` runs `pg_ctl -m immediate stop`,
i.e. SIGQUIT, and the postmaster still reaches `on_proc_exit(UnlinkLockFiles)` at
`postgres/src/backend/utils/init/miscinit.c:1495`, so it removes
`postmaster.pid`), so **S28.0 must first add a true-SIGKILL helper with
`Setpgid` to `pgcluster`** → start **goopg** on the directory
→ compare row counts and aggregates against answers captured before the kill.
Add a second variant that creates a GIN index first and asserts goopg refuses
with S25's specific message and a non-zero exit.
**Design doc:** `docs/design/0131-0017-crash-interchange-e2e.md`.

### S29 — BASE_BACKUP stops mutating the source `pg_control` (est ~1 loop)

Adjacent bug found while auditing the forward path. `internal/server/basebackup.go:223`
calls `UpdateControlCheckpoint` on the **live** data directory, writing
`MinRecoveryPoint=1`, `MinRecoveryPointTLI=1`, `BackupEndPoint=redo` into it
(`internal/initdb/pgcontrol.go:147-165`). On a promoted (TLI ≥ 2) cluster, a
crash inside the BASE_BACKUP → next-checkpoint window then makes PG FATAL
`requested timeline %u does not contain minimum recovery point`
(`postgres/src/backend/access/transam/xlogrecovery.c:878-886`). Build the
backup's control image into the tar stream instead of mutating the source.

**Gates:** after a BASE_BACKUP, assert the live `pg_control` still has
`MinRecoveryPoint == 0` and `BackupEndPoint == 0`; `TestE2E_PGStandbyFullCycle`
proves the tar side still works.
**Design doc:** rides this plan.

## Dependencies

```
S1 ─┬─> S3 ──> (Theme A complete)          S1 LANDED 2026-08-11 (6c81151d)
S2 ─┘
S4  (independent; assertions over views wait for S6)
S5 ─┬─> S8a ──> S6 ──> S7 ──> S8b ──> S9.1 ──> S9.2 ──> S9.3 ──> S9.4 (likely deferred)
    └── (S5 and S6 are technically independent; S5 first only because it is smaller)
S10 (independent; land FIRST)
```

- S3 needs S1 (goopg cannot start on the directory otherwise) and S2 (or the
  system ID silently diverges).
- S4's view assertions need S6; its table assertions need nothing.
- **S6 does NOT depend on S5** (corrected after review). S5's gate is
  `b5c_view` in `TestE2E_FailoverGoopgToPG`; S6's is `pg_stat_replication` in
  `waitForPhysicalStreamingPGtoGoopg`. They share nothing. S5 is listed first
  only because it is the smaller change; S6 has the stronger claim to urgency,
  since it reverts a harness downgrade that ledger rows 995/996 record as a
  knowing loss of coverage.
- **S6 needs S8a**, not the reverse: under pinning, S6.1's blob patch inverts.
- **S7 needs S8a** (the OID-mapping table is S8a's output, and S7 refuses to
  emit for an in-band relid with no mapping entry). **S8b needs S7** (its guards
  verify against S7's manifest). This is why S8 is split.
- S9 needs S8a decided, or its captured corpus may need redoing.
- **S6's flip reaches only freshly `goopg init`'d directories.** `relhasrules`
  is written by `pgClassRow` → `bootstrapPgClassTuples` at initdb time, so every
  existing goopg `$PGDATA` — the bench clusters on 65433/65436/65437 and any
  operator directory — keeps `relhasrules='f'` on disk and a hosted PG still
  cannot read its system views. There is **no in-place upgrade path; re-initdb
  is required.** Every M0131 gate initdbs fresh, so nothing catches this: say it
  in the S6 commit message and file a ledger row.
- S10 is independent and should land first so the new Theme A/B tests are not
  written against the dead pattern.

**Theme F** (added 2026-08-11):

```
S16 (reverse safety bug) ─┬─> S16.3 ─> S21b ┐
                          ├─> S19            ├─> S28
                          ├─> S21a ──────────┤
                          ├─> S22 ───────────┤
                          ├─> S23            │
                          └─> S25 ───────────┘   (S28's GIN variant needs S25)
S17 (forward safety bug) ──> S18 ─┬─> S20
                                  ├─> S26
                                  └─> S29        (S29 removes today's only
                                                  DB_IN_PRODUCTION writer)
S27 needs S17; benefits from S18 and S26.
S24 DEFERRED (see the S24 scoping decision) — re-armed by S28's _concurrent variant.
```

- **S16 and S17 are live data-loss bug fixes and land before everything else in
  the theme — including before each other's direction.** Each is worth landing
  even if no other Theme F slice ever does.
- S19 also repairs the **clean** reverse path (S3), so it is not gated on the
  crash work; it can land as soon as S16 does.
- S20 needs S18's extended `ControlFileData` for the multixact/oldest fields
  (S20.4 explicitly waits on S18.2).
- S21b (btree opcodes) needs **S16.3's** refusal in place first, or a silent
  regression is indistinguishable from success. That is the *only* edge into
  S21b — corrected after review: **S21a, S22 and S23 do not chain.** S22 lives in
  `internal/initdb/xact_recovery.go` and S23 in four unrelated rmgr arms; neither
  shares code with S21a or S21b, so the earlier
  `S21a → S21b → S22 → S23 → S28` chain was over-serialised.
- **S24 (MultiXact) is DEFERRED** — see the scoping decision in S24. It is
  re-armed by S28's skipped `_concurrent` variant, not by a note.
- S26 is **not** a predecessor of S27 (corrected after review); S27 only
  *benefits* from S18 and S26. Its one hard predecessor is S17 — without it PG
  never enters recovery and the test proves nothing.
- S28 needs **S21a and S22** at minimum (its workload asserts their opcodes), and
  its GIN variant needs **S25**'s specific refusal message. S28.0 (a true-SIGKILL
  `pgcluster` helper) comes before all of it.
- **S29 is NOT independent** (corrected after review): `UpdateControlCheckpoint`
  is today the *other* writer of `DB_IN_PRODUCTION`
  (`internal/initdb/pgcontrol.go:148`, reached from
  `internal/server/basebackup.go:223` on the live directory), so removing it
  before S17 lands would leave goopg with **no** path that stamps the state at
  all. **S29 must follow S17.**
- **Theme F does not depend on Themes C/D and can proceed in parallel with them.**
