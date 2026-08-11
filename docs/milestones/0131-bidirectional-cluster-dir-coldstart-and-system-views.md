# Milestone 0131 — Bidirectional cluster-directory cold-start + real-PG system-view hosting

**Status:** planned
**Filed:** 2026-08-11 (user directive — see the fix_plan Current Priority banner)
**Reference plan:** `.ralph/fix_plan.md` (M0131 section)
**Implementation plan (authoritative task decomposition):**
`docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Design of record:** M0130's Acceptance-bar item 1 (undischarged);
`docs/design/0130-0002-pg-class-heap-persistence.md` §"Remaining for full
reverse-path parity" items 1–3; deferral-ledger rows #428, #490, #995, #996.
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
the tar, and the transfer normalises file modes and drops
`postmaster.pid`/`postmaster.opts`. Starting a stock `postgres -D <dir>` on the
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
  `internal/initdb/initdb.go:5811`, so PG reads `relhasrules='f'` and
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
4. **Correct the record** — the ledger's `pg_internal.init` attribution and its
   `2620` index OID are fixed; the design-doc guards in `0130-0002` that still
   read *"not yet implemented"* are brought up to date; the three
   "Remaining for full reverse-path parity" items get the ledger rows the
   filing rule requires and never got.
5. **Retire `copyInitFiles`** — the function and its three call sites are
   deleted and the two prose comments that mis-attribute the view gap to the
   copied file are re-attributed.
6. **Bounded, honest scope for system views** — the six already-captured
   replication views become genuinely queryable; widening to the remaining
   ~74 `system_views.sql` + 65 `information_schema` views is decomposed,
   OID-policy-gated, and explicitly allowed to outlive this milestone provided
   each unlanded slice carries a ledger row.

## Task list (summary — the design/0131 plan doc is authoritative)

| task | what | theme |
|---|---|---|
| S1 | GUC registry accepts a PG-18-initdb `postgresql.conf` (8 unregistered settings) | A — reverse cold start |
| S2 | `LoadOrCreateSystemID` reads pg_control before inventing an ID | A |
| S3 | E2E: real `initdb` → PG workload → clean stop → **goopg** serves the dir | A |
| S4 | E2E: `goopg init` → goopg workload → clean stop → **real PG** serves the dir | B — forward cold start |
| S5 | Runtime `pg_rewrite` index maintenance (2692 + 2693) → user views work | C — view hosting |
| S6 | Flip `relhasrules=true` for the 6 nailed system views; fix the `12261` blob | C |
| S7 | `ev_action` capture tooling + a blob-invariant guard test | C |
| S8 | System-view OID policy (pin to upstream vs. rewrite captured relids) | C |
| S9 | Widen the on-disk system-view corpus (LARGE, sub-sliced by view group) | C |
| S10 | Delete `copyInitFiles` + 3 call sites; correct ledger rows and stale guards | D — hygiene |

**Filing rule (inherited from M0130):** no task is deferred without a strong reason
recorded in the deferral ledger; every item's subtasks are listed inline in the
fix_plan task body; every non-trivial subsystem lands its design doc (status
`draft` → `accepted`) **within M0131** — a design doc punted past the milestone is a
milestone failure.

## Acceptance bar

1. **Reverse cold start:** a test creates a `$PGDATA` with the real
   `initdb`, starts real PG on it, runs CREATE TABLE / CREATE INDEX / INSERT /
   UPDATE / DELETE, stops PG cleanly (SIGINT → shutdown checkpoint,
   `DB_SHUTDOWNED`), then starts **goopg** on that same directory with no file
   rewriting other than what a real operator would do, and `SELECT` returns the
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
   `pg_stat_replication_slots_ev_action.dat` before S6 fixes it.
6. **`copyInitFiles` is gone:** the function, its three call sites, and the two
   mis-attributing comments are deleted; the replication family stays green.
7. **Record corrected:** ledger rows #428/#995/#996 carry a follow-up row naming
   the real mechanism and the `2620`→`2693` correction; `0130-0002`'s Guards
   section no longer claims the reverse path is unimplemented; its three
   "Remaining for full reverse-path parity" items each have a ledger row.
8. **Scope honesty for S9:** every system view NOT converted in this milestone is
   covered by one ledger row naming the group, the blocker, and the resume point.
   A partially-converted corpus is acceptable; an undocumented one is not.
9. **No regressions:** the replication family (`TestE2E_PhysicalReplication`
   + Sync, `TestE2E_FailoverGoopgToPG`, `TestE2E_FailoverPGtoGoopg`,
   `TestE2E_StandbyAttachRoundtrip`, `TestE2E_PGStandbyFullCycle`,
   `TestE2E_ChecksumReplication`, `TestPort_PgBasebackup*`) stays green; UNITS /
   SMOKE / SPOT and the nightly regress suite stay green; `make ralph-state-guard`
   clean.
10. **Hygiene:** every task's subtasks are inline in fix_plan.md; zero items closed
   by deferral without a ledger row stating the strong reason; all design docs
   listed below exist with status `accepted` (or the task itself is open).

## Required design docs

| doc | status | covers |
|---|---|---|
| `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` | created at filing | authoritative task decomposition (all S-tasks) |
| `docs/design/0131-0001-guc-registry-pg-initdb-conf-compat.md` | draft — **within M0131 (S1, before code)** | the 8 unregistered GUCs, stub-vs-implement policy, the `locale.go` self-inflicted sibling |
| `docs/design/0131-0002-system-identifier-pgcontrol-fallback.md` | draft — **within M0131 (S2, before code)** | pg_control-first system ID, mirroring `LoadOrCreateTimelineID` |
| `docs/design/0131-0003-reverse-coldstart-e2e.md` | draft — **within M0131 (S3, before code)** | the PG→goopg cold-start test, its clean-shutdown precondition, what it may not assert |
| `docs/design/0131-0004-forward-coldstart-e2e.md` | draft — **within M0131 (S4, before code)** | the goopg→PG cold-start test and the un-normalised-directory delta vs. basebackup |
| `docs/design/0131-0005-pg-rewrite-runtime-index-maintenance.md` | draft — **within M0131 (S5, before code)** | indexes 2692/2693, the oid+name key layout, base/5 mirroring |
| `docs/design/0131-0006-system-view-relhasrules-flip.md` | draft — **within M0131 (S6, before code)** | the commented-out flip, the `12261` blob, one-view-at-a-time risk control |
| `docs/design/0131-0007-ev-action-capture-tooling.md` | draft — **within M0131 (S7, before code)** | the capture generator and its byte-identical re-derivation oracle |
| `docs/design/0131-0008-system-view-oid-policy.md` | draft — **within M0131 (S8, before code)** | pin-to-upstream vs. rewrite, and why view-on-view forces the decision early |
| `docs/design/0131-0009-system-view-corpus-widening.md` | draft — **within M0131 (S9, before code)** | the view groups, the ~30 KB TOAST ceiling, the pgnodes non-path |
| `docs/design/0131-0010-copyinitfiles-retirement.md` | draft — **within M0131 (S10)** | the removal, the evidence, and the record corrections |

Smaller single-function changes may ride the implementation-plan doc per the repo
rule (a design doc is required for every *non-trivial subsystem*; single-function
changes with unit tests may cite this plan instead).
