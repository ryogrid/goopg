# Milestone 0131 — Bidirectional cluster-directory cold-start + real-PG system-view hosting

**Status:** planned
**Filed:** 2026-08-11 (user directive — see the fix_plan Current Priority banner)
**Reference plan:** `.ralph/fix_plan.md` (M0131 section)
**Implementation plan (authoritative task decomposition):**
`docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Design of record:** M0130's Acceptance-bar item 1 (undischarged);
`docs/design/0130-0002-pg-class-heap-persistence.md` §"Remaining for full
reverse-path parity" items 1–3; deferral-ledger rows #428, #490, #995, #996.
(Throughout M0131, `#NNN` ledger references are **line numbers** in
`.ralph/deferral_ledger.md` — that file has no ID column.)
**Prerequisites:** M-NIGHTLY is the standing filing obligation (highest priority,
unconditional); **M0131 is the top-priority milestone after M-NIGHTLY** (user
directive 2026-08-11). Builds directly on M0130 S1–S11.6, which delivered the
on-disk format parity (per-relation FSM/VM forks, pg_class/pg_attribute/pg_type
heap persistence, nbtree PG-identical pages and tuples, `RM_BTREE` WAL content
parity, multi-timeline TLI reconciliation) that this milestone now exercises
from a cold start rather than through a `pg_basebackup`-derived directory.
**Branch:** inherits the current lineage and its discipline (worktrees off pinned
clean HEAD, explicit pathspec staging, guard re-runs after rebase/handoff).

## Background

M0130 closed nine of its ten acceptance-bar items. Every one of them was
discharged through the **replication lane**: `pg_basebackup` from a live goopg
primary produces a directory, a real PG 18.3 starts on it as a standby, streams,
promotes, and a goopg standby re-attaches to the promoted PG
(`TestE2E_PGStandbyFullCycle`, green 2026-08-10 after twelve blockers).

**Acceptance-bar item 1 was never discharged.** It asks for something strictly
stronger than the replication lane proves:

> **PG starts goopg's data dir:** a PG 18.3 `pg_ctl start` succeeds against a
> goopg-initdb'd `$PGDATA` — fresh, and after a workload of CREATE TABLE /
> ADD COLUMN / CREATE SCHEMA / CREATE INDEX / CREATE DATABASE / INSERT / UPDATE /
> DELETE — and serves reads via psql with zero FATAL. Reverse: goopg starts and
> serves against a PG-initdb'd `$PGDATA`.

A basebackup directory is *derived from* a goopg cluster but is not the same
artifact: `internal/server/basebackup.go:113` excludes `pg_internal.init` from
the tar. (That exclusion is the *one* substantive delta — file modes are **not**
normalised, since `writeTarFileWithMode` copies `info.Mode().Perm()`
(`basebackup.go:723`, `:728`, `:933`) and forces `0600` only for `pg_control`
and WAL segments; and `postmaster.opts` has no writer in goopg at all while
`postmaster.pid` is removed on a clean stop, so neither is present in the live
directory either.) Starting a stock `postgres -D <dir>` on the
**live** goopg directory after a clean `goopg stop` is the claim Goal 1 makes,
and no test makes it. Symmetrically, all three `pgcluster.OpenExisting` call
sites pass basebackup output; none passes a goopg `$PGDATA`.

**Status at filing (2026-08-11):** the reverse direction is further along than
its documentation suggests. `TestE2E_FailoverPGtoGoopg` already has real PG
create `public.bench_log`, and goopg then serves `SELECT count(*)` over a
**PG-authored** pg_class/pg_attribute heap — so `DecodePGClassPhysicalRow` /
`DecodePGAttributePhysicalRow` are proven against genuine PG rows. What is
unproven is the *cold-start, non-standby* shape. `internal/initdb/reverse_path_test.go`
only *simulates* it: it runs goopg's own `Init()`, deletes the goopg catalog
cache, and asserts `Open()` survives. No PG binary, no user table, no row read.

**The three remaining blockers, all newly diagnosed at filing:**

1. **A PG-initdb'd `postgresql.conf` stops goopg before it reads one catalog
   page.** `cmd/goopg/main.go:360-363` auto-loads the file and
   `config.ApplyConfigEntries` (`internal/config/guc.go:563-579`) treats an
   unknown parameter as a hard error. PG 18 initdb emits eight settings goopg
   has never registered: `dynamic_shared_memory_type`, `log_timezone`,
   `autovacuum_worker_slots`, `lc_messages`, `lc_monetary`, `lc_numeric`,
   `lc_time`, `default_text_search_config`. This is why the existing PG→goopg
   test overwrites the conf wholesale rather than reading it.
2. **`global/system_identifier` has no pg_control fallback.**
   `LoadOrCreateSystemID` (`internal/initdb/initdb.go:54-76`) silently *invents*
   a fresh random ID on a directory that lacks the goopg-private file — so goopg
   on a PG dir disagrees with the ID already in pg_control. Its sibling
   `LoadOrCreateTimelineID` (`internal/initdb/timeline.go:44-68`) reads
   pg_control first; this one was never given the same treatment.
3. **A real PG hosted on a goopg catalog cannot evaluate ANY view** — 42809
   `cannot open relation` / `DETAIL: … not supported for views`. This is the gap
   ledger row #996 states as *"goopg therefore cannot yet host a real PG that
   reads any system view"*, and it is why M0130's Phase D had to be downgraded
   from querying `pg_stat_replication` to probing the two SRFs the view wraps.

**The ledger's diagnosis of blocker 3 is wrong, and this milestone corrects it.**
Rows #428/#995/#996 attribute the gap to *"a goopg-built `pg_internal.init`
leaves `rd_rules` empty"* and prescribe *"populating `rd_rules` in goopg's
`pg_internal.init`"*. That fix is not expressible: upstream
`load_relcache_init_file` (`postgres/src/backend/utils/cache/relcache.c:6443-6453`)
sets `rd_rules = NULL` unconditionally with the comment *"Rules and triggers are
not saved (mainly because the internal format is complex and subject to
change)"*; `write_relcache_init_file` never serialises them; views never pass
`RelationIdIsInInitFile`; and `StartupXLOG` deletes every init file at
`postgres/src/backend/access/transam/xlog.c:5633` before any backend reads one.
The rows also name index **2620**, which is `pg_trigger`; the index
`RelationBuildRuleLock` actually scans is **2693**
(`pg_rewrite_rel_rulename_index`, `postgres/src/include/catalog/pg_rewrite.h:57`).

The real mechanism has two independent causes, both small:

- **System views:** `relHasRules = true` is *commented out* at
  `internal/initdb/initdb.go:5817`, so PG reads `relhasrules='f'` and
  short-circuits at `relcache.c:1250` without ever scanning `pg_rewrite`.
- **User views:** `internal/executor/sys_pg_rewrite.go:102-106` writes the
  `pg_rewrite` heap row and discards the TID — indexes **2692** and **2693** get
  no runtime entry, so `RelationBuildRuleLock`'s index scan (which has no
  seq-scan fallback) finds nothing. This is blocker #8 (pg_index 2678) repeating
  exactly, and `internal/executor/sys_pg_attrdef.go:87-95` is the in-tree fix
  pattern.

Finally, the tests carry a **vestigial workaround that encodes the false belief**
the ledger recorded. `copyInitFiles`
(`internal/testport/e2e_failover_goopg_to_pg_test.go:808-844`, three call sites)
copies `pg_internal.init` onto the PG standby at mode `0o400` "to prevent PG's
`write_relcache_init_file` from overwriting them" — but PG `unlink()`s the file,
and `0o400` does not prevent unlink. Its own adding commit (`30b0716f`,
2026-05-17, subject ends *"add copyInitFiles workaround"*) admits *"PG's
load_relcache_init_file still rejects the file silently"*; it was superseded the
next day by `c09d519e` ("step 3cq proper"), whose code comment at
`internal/initdb/pg_type_bootstrap.go:322-331` states the removal outright. It
has since been cargo-culted into two more tests. Deleting it is part of this
milestone so no future reader re-derives the wrong model.

**Theme F, added 2026-08-11 (user directive).** Themes A and B above scope both
cold starts to a *cleanly shut down* source — S3 and S4 assert `DB_SHUTDOWNED`
before the handover, per `0130-0002` §"WAL replay constraint". That makes the
milestone's own headline half-true: two engines are not interchangeable on a
directory if the interchange only works when the previous engine exited
politely. Theme F removes the precondition in both directions.

Its filing investigation found that **each direction already loses committed
data today** — this is not only a missing capability:

- **Reverse.** `internal/wal/xlog_record.go:218-220` bounds known rmids at 15,
  but PG 18's real maximum is 21. Rmids **16 SPGist, 17 BRIN, 18 CommitTs,
  19 ReplicationOrigin, 20 Generic, 21 LogicalMessage** therefore fail *header
  decode* inside the reader, and `readAllPageAware`
  (`internal/wal/reader.go:148-165`) treats a header-decode failure as **end of
  WAL**. Every later record is dropped, replay reports success, and
  `detectWritePos` then appends over the survivors — the loss becomes permanent
  on the first write. One `pg_logical_emit_message` in a PG crash tail is
  enough. (Rmids 6/12/13/14 take the other path and abort startup, which is
  safe.) A third silent path: the btree `default:` arm
  (`internal/wal/recovery.go:2516-2523`) discards any PG btree record whose
  blocks lack an FPI while returning `applied=true` — and PG emits an FPI only
  on a page's first post-checkpoint touch.
- **Forward.** goopg writes `pg_control.state` from only two runtime sites,
  the checkpointer (`internal/wal/checkpointer.go:736-748`), whose first tick is
  one full `checkpoint_timeout` (default 300 s) after start. **No startup path
  stamps `DB_IN_PRODUCTION`.** A SIGKILL inside that window leaves
  `DB_SHUTDOWNED`, so PG falls through all three arms of `xlogrecovery.c:924-937`, leaves
  `InRecovery` false, **skips `PerformWalRecovery()` entirely**, and resumes
  inserting WAL over goopg's tail — silently, with no PANIC.

Two findings that usefully *bound* Theme F: WAL segment zeroing is a non-issue
(goopg zero-fills recycled segments where upstream's `InstallXLogFileSegment`
does not, so goopg is strictly safer), and `CheckRequiredParameterValues` is a
no-op in crash recovery (every branch is gated on `ArchiveRecoveryRequested`).
Conversely the reverse direction is *worse* than the missing-rmgr list suggests:
the larger cost is opcode gaps **inside** rmgrs goopg already claims to handle —
`XLOG_HEAP2_MULTI_INSERT` (every COPY), `XLOG_HEAP2_VISIBLE` (every VACUUM),
`XLOG_HEAP_LOCK` (every `FOR UPDATE`), `XLOG_CLOG_ZEROPAGE` (every 32768 XIDs),
`XLOG_XACT_ASSIGNMENT` (any subtransaction), and seven btree opcodes.

## Goals

1. **Reverse cold start** — goopg starts and serves against a `$PGDATA` created
   by a real PG 18.3 `initdb`, after that PG ran a DDL+DML workload and shut
   down cleanly. Proven by an E2E test, not by simulation.
2. **Forward cold start** — a stock `postgres -D <dir>` starts and serves
   against a goopg-created `$PGDATA` after a clean `goopg stop`, with no
   `pg_basebackup` in the path. Proven by an E2E test.
3. **Real PG hosted on goopg evaluates views** — `SELECT * FROM <user view>` and
   `SELECT * FROM pg_stat_replication` both succeed on a PG running against a
   goopg catalog directory. Retires ledger rows #428/#995/#996 and restores the
   Phase D assertion that AI-20260810-011258-003 downgraded.
   **Scope, stated precisely so this goal cannot be over-read:** what M0131
   guarantees is *user views* (all of them, via S5) plus *the six already-captured
   replication system views* (via S6), plus whatever S9 sub-slices actually land.
   goopg has ~115 further virtual relations and 65 `information_schema` views
   that answer **42P01 relation does not exist** — a different failure from
   42809 — and those are **not** all closed here. "goopg can host a real PG that
   reads system views" becomes true for a named, tested set, not for the whole
   catalog. See `docs/design/0131-0009-system-view-corpus-widening.md`, which
   measures the remainder.
4. **Correct the record** — the ledger's `pg_internal.init` attribution and its
   `2620` index OID are fixed; the design-doc guards in `0130-0002` that still
   read *"not yet implemented"* are brought up to date; the three
   "Remaining for full reverse-path parity" items get the ledger rows the
   filing rule requires and never got.
5. **Retire `copyInitFiles`** — the function and its three call sites are
   deleted and the two prose comments that mis-attribute the view gap to the
   copied file are re-attributed.
6. **Crash-state interchange, both directions (added 2026-08-11)** — neither
   cold start may require the *other* engine to have exited politely. goopg
   starts on a PG `$PGDATA` whose `pg_control` says `DB_IN_PRODUCTION` and whose
   WAL tail is unreplayed; a real PG crash-recovers a goopg `$PGDATA` in the same
   state. Themes A and B deliberately scoped this out (`0130-0002`
   §"WAL replay constraint"); Theme F removes the precondition, because two
   engines that only interchange after a clean shutdown are not interchangeable.
7. **Fix the two live data-loss bugs this exposed** — Theme F's filing
   investigation found that *each direction already loses committed data today*,
   not merely that it lacks a feature. goopg mistakes an unrecognised WAL record
   for end-of-WAL and silently truncates (then overwrites) the rest; and goopg
   never stamps `pg_control.state = DB_IN_PRODUCTION` at startup, so a real PG
   skips recovery entirely and overwrites the tail. Both are unconditional
   fixes, independent of whether the rest of Theme F lands.
8. **Bounded, honest scope for system views** — the six already-captured
   replication views become genuinely queryable; widening to the remaining
   ~74 `system_views.sql` + 65 `information_schema` views is decomposed,
   OID-policy-gated, and explicitly allowed to outlive this milestone provided
   each unlanded slice carries a ledger row.

## Task list (summary — the design/0131 plan doc is authoritative)

| task | what | theme |
|---|---|---|
| S1 | GUC registry accepts a PG-18-initdb `postgresql.conf` (8 initdb-authored + 2 goopg-initdb-authored = **10** registrations) — **LANDED 2026-08-11 (`6c81151d`)** | A — reverse cold start |
| S2 | `LoadOrCreateSystemID` reads pg_control before inventing an ID | A |
| S3 | E2E: real `initdb` → PG workload → clean stop → **goopg** serves the dir | A |
| S4 | E2E: `goopg init` → goopg workload → clean stop → **real PG** serves the dir | B — forward cold start |
| S5 | Runtime `pg_rewrite` index maintenance (2692 + 2693) → user views work | C — view hosting |
| S6 | Flip `relhasrules=true` for the 6 nailed system views; fix the `12261` blob | C |
| S8a | System-view OID policy DECISION + the pinned OID table (must precede S6 and S7) | C |
| S7 | `ev_action` capture tooling + a blob-invariant guard test | C |
| S8b | Manifest-based pinning guards (needs S7's manifest) | C |
| S9 | Widen the on-disk system-view corpus (LARGE, sub-sliced by view group) | C |
| S10 | Delete `copyInitFiles` + 3 call sites; correct ledger rows and stale guards | D — hygiene |
| S16 | **Fail closed: an unrecognised WAL record is not end-of-WAL** (live data-loss fix) | E — crash-state interchange |
| S17 | **goopg stamps `DB_IN_PRODUCTION` at startup** (live data-loss fix) | F |
| S18 | pg_control writer durability (`O_RDWR`+fsync) + full checkpoint-struct coverage + live TLI | F |
| S19 | Validate `xlp_pageaddr`/`xlp_tli` — PG does not zero-fill recycled segments (also fixes S3) | F |
| S20 | pg_control-driven recovery in goopg: read `DBState`, redo start, startup hygiene | F |
| S21 | Opcode coverage inside the already-handled rmgrs (21a non-btree, 21b btree) — LARGE | F |
| S22 | CLOG replay opcode dispatch + commit-record `subxacts[]` parsing | F |
| S23 | The cheap tail: LogicalMessage / ReplicationOrigin / Generic / CommitTs | F |
| S24 | MultiXact durable `pg_multixact` SLRU + `multixact_redo` — LARGE/RISKY, deferrable | F |
| S25 | Index-AM boundary: detect GIN/GiST/SP-GiST/BRIN/hash, refuse specifically, ledger | F |
| S26 | `pd_lsn` completeness audit on logged change paths | F |
| S27 | Forward crash E2E (+ stale pidfile, torn contrecord) | F |
| S28 | Reverse crash E2E | F |
| S29 | BASE_BACKUP stops mutating the source `pg_control` | F |

**Filing rule (inherited from M0130):** no task is deferred without a strong reason
recorded in the deferral ledger; every item's subtasks are listed inline in the
fix_plan task body; every non-trivial subsystem lands its design doc (status
`draft` → `accepted`) **within M0131** — a design doc punted past the milestone is a
milestone failure.

## Acceptance bar

1. **Reverse cold start:** a test creates a `$PGDATA` with the real
   `initdb`, starts real PG on it, runs CREATE TABLE / CREATE INDEX / INSERT /
   UPDATE / DELETE, stops PG cleanly (SIGINT → shutdown checkpoint,
   `DB_SHUTDOWNED`), then starts **goopg** on that same directory with **zero
   edits to `postgresql.conf` and no `standby.signal`** — any edit the test turns
   out to need is an S1 defect, not a test detail — and `SELECT` returns the
   PG-written rows. Zero FATAL in the goopg log.
2. **Forward cold start:** the mirror-image test — `goopg init`, goopg workload,
   `goopg stop`, then `postgres -D <same dir>` — serves reads via psql with zero
   FATAL, and `SELECT relname FROM pg_class` lists the user tables.
3. **User views on a hosted PG:** on the promoted PG of
   `TestE2E_FailoverGoopgToPG`, `SELECT count(*) FROM b5c_view` equals the
   count over its base table. The assertion currently fails 42809 and is the
   precise gate.
4. **System views on a hosted PG:** `waitForPhysicalStreamingPGtoGoopg`
   (`internal/testport/e2e_pg183_standby_full_cycle_test.go`) queries the
   `pg_stat_replication` **view** again, not the
   `pg_stat_get_activity(NULL) JOIN pg_stat_get_wal_senders()` workaround
   AI-20260810-011258-003 installed. Reverting that downgrade is the honest gate.
5. **Blob invariant:** a guard test scans every `internal/initdb/*_ev_action.dat`
   for `:relid` values and fails on any OID that is neither a pinned catalog OID
   nor a goopg-assigned view OID. It must fail on today's
   `pg_stat_replication_slots_ev_action.dat` before S6 fixes it. It must count
   **occurrences, not lines** — the `.dat` files are single-line, so `grep -c`
   reports 1 where there are 2.
5b. **A minimum S9 landing (added after review — without this the milestone can
   close with the headline capability ~5 % delivered):** S9.1 lands in full, or
   at minimum `pg_settings` and `pg_stat_activity` are queryable on a hosted PG.
   S9.2–S9.4 may defer per item 8. An M0131 that converts zero additional views
   does not meet Goal 3.
6. **`copyInitFiles` is gone:** the function, its three call sites, and the two
   mis-attributing comments are deleted; the replication family stays green.
7. **Record corrected:** ledger rows #428/#995/#996 carry a follow-up row naming
   the real mechanism and the `2620`→`2693` correction; `0130-0002`'s **Guard #2**
   ("Reverse path not yet implemented") no longer contradicts that doc's own
   later sections, and its "Remaining for full reverse-path parity" item 3 no
   longer names a blocker that has since landed; its three "Remaining" items each
   have a ledger row. (**Guard #1** — "PG started against goopg data dir …
   *Needs E2E PG-attach test — not yet implemented*" — is **true** until S4
   lands, so S4 discharges it, not S10.)
8. **Scope honesty for S9:** every system view NOT converted in this milestone is
   covered by one ledger row naming the group, the blocker, and the resume point.
   A partially-converted corpus is acceptable; an undocumented one is not.
8b. **Crash-state interchange, forward (added 2026-08-11):** an E2E starts goopg,
   runs a workload with a known committed/uncommitted split, forces one online
   checkpoint, does more committed work, then `kill -9`s the server by PID file.
   `pg_control.State` must read `DB_IN_PRODUCTION`. A stock `postgres -D <same
   dir>` then logs `redo starts at` and `redo done at`, logs no PANIC/FATAL, and
   every committed row — including the post-checkpoint ones — is visible while
   the uncommitted ones are not. Any `invalid record length` / `invalid magic
   number` line must be the *last* WAL complaint (benign end-of-WAL). A second
   variant kills mid-`INSERT … SELECT` so the tail ends inside a multi-page
   record, and asserts PG writes its `XLOG_OVERWRITE_CONTRECORD` and logs
   `successfully skipped missing contrecord` — the one crash-only WAL path the
   standby lane structurally cannot reach.
8c. **Crash-state interchange, reverse (added 2026-08-11):** the mirror — real
   `initdb`, a **single-session** PG workload exercising COPY / VACUUM /
   `FOR UPDATE` / `TRUNCATE` / an index-heavy insert, SIGKILL the postmaster,
   then **goopg** serves the directory and its counts and aggregates match values
   captured before the kill. `pg_waldump` over the crash tail must confirm each
   asserted opcode is actually present — otherwise a workload that silently stops
   emitting one passes for the wrong reason. A second variant creates a GIN index
   first and asserts goopg refuses with a message naming the access method and
   exits non-zero — a specific refusal, never a silent skip.
   **Scope, stated so a green 8c is not over-read:** single-session is a
   deliberate narrowing that defers S24 (MultiXact) out of M0131 — a
   single-session workload emits no multixact record. A third `_concurrent`
   variant ships `t.Skip`ped as the executable re-arm trigger. 8c therefore
   proves crash interchange for single-session workloads, **not** in general.
8d. **The two live data-loss bugs are fixed unconditionally (added 2026-08-11),
   each proven by a named test that fails against today's HEAD:** (i) a WAL
   stream of `valid → rmid-18 → valid` reports a non-tail stop and does not
   silently truncate, and a PG-shaped `XLOG_BTREE_DEDUP` carrying block data but
   no FPI errors instead of reporting `applied=true` (S16); (ii) with a long
   `checkpoint_timeout`, `pg_control.State` reads `DB_IN_PRODUCTION` before any
   checkpoint could have run, and still does after a SIGKILL (S17). These two are independent of the rest of Theme F and must land even if
   nothing else in it does. Each carries a regression test that fails against
   today's HEAD.
9. **No regressions:** the replication family (`TestE2E_PhysicalReplication`
   + Sync, `TestE2E_FailoverGoopgToPG`, `TestE2E_FailoverPGtoGoopg`,
   `TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`,
   `TestE2E_PGStandbyFullCycle`, `TestE2E_ChecksumStreamingGoopgToPG`,
   `TestPort_PgBasebackup*`) stays green; UNITS / SMOKE / SPOT stay green and the
   nightly regress suite shows **no new** divergences (11 are already open as
   AI-20260811-014635-003..011 — this milestone does not inherit them);
   `make ralph-state-guard` clean.
10. **Hygiene:** every task's subtasks are inline in fix_plan.md; zero items closed
   by deferral without a ledger row stating the strong reason; every design doc
   listed below whose task is checked off is status `accepted`, and every doc
   whose task is still open carries a ledger row saying why.

## Required design docs

| doc | status | covers |
|---|---|---|
| `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` | created at filing | authoritative task decomposition (all S-tasks) |
| `docs/design/0131-0001-guc-registry-pg-initdb-conf-compat.md` | **accepted — landed 2026-08-11 (`6c81151d`)** | the 10 registrations (8 from PG initdb + `password_encryption`/`log_file_mode` from goopg's own initdb), stub policy, the `locale.go` self-inflicted sibling |
| `docs/design/0131-0002-system-identifier-pgcontrol-fallback.md` | draft — **within M0131 (S2, before code)** | pg_control-first system ID, mirroring `LoadOrCreateTimelineID` |
| `docs/design/0131-0003-reverse-coldstart-e2e.md` | draft — **within M0131 (S3, before code)** | the PG→goopg cold-start test, its clean-shutdown precondition, what it may not assert |
| `docs/design/0131-0004-forward-coldstart-e2e.md` | draft — **within M0131 (S4, before code)** | the goopg→PG cold-start test and the un-normalised-directory delta vs. basebackup |
| `docs/design/0131-0005-pg-rewrite-runtime-index-maintenance.md` | draft — **within M0131 (S5, before code)** | indexes 2692/2693, the oid+name key layout, base/5 mirroring |
| `docs/design/0131-0006-system-view-relhasrules-flip.md` | draft — **within M0131 (S6, before code)** | the commented-out flip, the `12261` blob, one-view-at-a-time risk control |
| `docs/design/0131-0007-ev-action-capture-tooling.md` | draft — **within M0131 (S7, before code)** | the capture generator and its byte-identical re-derivation oracle |
| `docs/design/0131-0008-system-view-oid-policy.md` | draft — **within M0131 (S8a, before S6 and S7)** | pin-to-upstream vs. rewrite (resolves in favour of pinning), why view-on-view forces the decision early, and the S8a/S8b split that breaks the S7↔S8 cycle |
| `docs/design/0131-0009-system-view-corpus-widening.md` | draft — **within M0131 (S9, before code)** | the view groups, the ~30 KB TOAST ceiling, the pgnodes non-path |
| `docs/design/0131-0010-copyinitfiles-retirement.md` | draft — **within M0131 (S10)** | the removal, the evidence, and the record corrections |
| `docs/design/0131-0012-crash-state-cluster-dir-interchange.md` | draft — **within M0131 (Theme F umbrella, before code)** | the theme-level design: both directions, the failure taxonomy, what is already fine and must not be re-planned |
| `docs/design/0131-0013-wal-reader-fail-closed.md` | draft — **within M0131 (S16 + S19, before code)** | unknown-record-vs-end-of-WAL, the btree FPI-only arm, `xlp_pageaddr` validation on both sibling paths |
| `docs/design/0131-0014-pgcontrol-runtime-state-and-durability.md` | draft — **within M0131 (S17 + S18 + S20, before code)** | `DB_IN_PRODUCTION` at startup, the `O_RDWR`+fsync writer, the full checkpoint struct, live TLI, and goopg reading `DBState` |
| `docs/design/0131-0015-pg-wal-opcode-coverage.md` | draft — **within M0131 (S21a + S21b + S22, before code)** | the per-rmgr opcode matrix goopg must add, and the CLOG/`subxacts[]` replay bug |
| `docs/design/0131-0016-multixact-durable-slru.md` | draft — **within M0131 (S24, before code)** | durable `pg_multixact` offsets+members SLRU and `multixact_redo`; why this is the only unavoidable missing rmgr |
| `docs/design/0131-0017-crash-interchange-e2e.md` | draft — **within M0131 (S27 + S28, before code)** | both crash E2Es, the committed/uncommitted split, stale pidfile handover, torn contrecord, and what a `kill -9` provably does NOT cover |

Smaller single-function changes may ride the implementation-plan doc per the repo
rule (a design doc is required for every *non-trivial subsystem*; single-function
changes with unit tests may cite this plan instead).
