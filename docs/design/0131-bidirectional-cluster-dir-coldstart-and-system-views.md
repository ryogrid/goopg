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
within C the order is strict (S5 → S6 → S7 → S8 → S9), because S5 gives the E2E
harness a working row-level view assertion that S6 then reuses, and S8's OID
decision must land before S9 captures a corpus that would have to be redone.
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

### S4 — E2E: `goopg init` → goopg workload → clean stop → real PG serves (est ~2 loops)

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
with `pgIndexTupleKeys = true` (`internal/access/btree/pgindex_btree.go:54`)
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
present but **commented out**, with a rationale ("Needed until the ev_action
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
- S6.3 Uncomment `relHasRules = true` at `internal/initdb/initdb.go:5811` and
  replace the stale comment with the real reason.
- S6.4 Invert the lock-in test
  `internal/initdb/pg_stat_wal_receiver_nailed_test.go:111-118`
  (`row[20].BoolValue()` must now be true) and rewrite its rationale.
- S6.5 Probe `reltype`: `relcache_init.go:693-697` gives all six `RelType: 2249`
  (RECORDOID) where real PG creates a per-view composite `pg_type` row. Plain
  SELECT after rule expansion likely does not touch it — probe, do not assume;
  ledger if it diverges.
- S6.6 **Risk control:** if the first flip FATALs a backend, flip the six one at
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

### S7 — `ev_action` capture tooling + invariant gate (est ~2 loops)

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

### S8 — System-view OID policy (est ~1-2 loops, DECISION)

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
- S10.4 Update `docs/design/0130-0002-pg-class-heap-persistence.md`: Guards #1
  and #2 still say *"not yet implemented"* / *"Reverse path not yet
  implemented"*, stale relative to that doc's own later sections; and item 3 of
  its "Remaining for full reverse-path parity" list names a blocker
  ("needs a test-harness PG instance lifecycle (M0130-S10)") that no longer
  exists — the harness landed; the real obstruction was S1's GUC gap.
- S10.5 File the three missing ledger rows for that list's items 1–3. They have
  never had one, which the inherited filing rule does not permit.
- S10.6 Note in the ledger the two unread/unsupported gaps this milestone
  deliberately does not close: `pg_filenode.map` is write-only (re-arm trigger
  already recorded at row #388), and `replayDecodedXLogRecord`
  (`internal/wal/recovery.go:2208+`, `default:` arm at `:2526`) handles rmids
  0,1,2,3,4,5,7,8,9,10,11,15,128 — missing **6 MultiXact, 12 Hash, 13 Gin,
  14 Gist, 16 SPGist, 17 BRIN, 18 CommitTs, 19 ReplicationOrigin, 20 Generic,
  21 LogicalMessage**. Note that 6/18/19 are *not* index AMs and do appear in
  ordinary PG workloads, so `0130-0002`'s framing ("requires implementing the
  corresponding index AMs") understates the surface.

**Gates:** E2E family stays green + UNITS + SMOKE.
**Design doc:** `docs/design/0131-0010-copyinitfiles-retirement.md`.

---

## Dependencies

```
S1 ─┬─> S3 ──> (Theme A complete)
S2 ─┘
S4  (independent; assertions over views wait for S6)
S5 ──> S6 ──> S7 ──> S8 ──> S9.1 ──> S9.2 ──> S9.3 ──> S9.4 (likely deferred)
S10 (independent; land early)
```

- S3 needs S1 (goopg cannot start on the directory otherwise) and S2 (or the
  system ID silently diverges).
- S4's view assertions need S6; its table assertions need nothing.
- S6 needs S5 only for ordering convenience — S5 gives the E2E harness a working
  row-level view assertion that S6 reuses. The two fixes are technically
  independent.
- S9 needs S8 decided, or its captured corpus may need redoing.
- S10 is independent and should land first so the new Theme A/B tests are not
  written against the dead pattern.
