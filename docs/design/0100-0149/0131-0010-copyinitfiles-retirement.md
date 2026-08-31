# Retire `copyInitFiles` — delete a provably inert test workaround and correct the record it created

**Status:** accepted (landed 2026-08-11)
**Date:** 2026-08-11
**Milestone:** M0131 (S10)

## Problem

Three E2E tests copy goopg's `pg_internal.init` relcache init files from the
primary data directory into the standby's before starting a real PG 18.3 on it:

- `internal/testport/e2e_pg183_standby_full_cycle_test.go:99`
- `internal/testport/e2e_checksum_replication_test.go:122`
- `internal/testport/e2e_failover_goopg_to_pg_test.go:154`

The helper is `copyInitFiles`
(`internal/testport/e2e_failover_goopg_to_pg_test.go:808-844`). It copies
`global/pg_internal.init` and `base/1/pg_internal.init`, additionally duplicating
the latter into `base/5/`, each written mode `0o400` so PG's
`write_relcache_init_file` cannot overwrite it.

The workaround does nothing. Worse, it encodes a **false belief** that has since
propagated into three deferral-ledger rows and two source comments: that a real
PG hosted on a goopg cluster directory refuses `SELECT * FROM <view>` (SQLSTATE
42809) because the copied `pg_internal.init` caches a *ruleless* relcache entry,
and that the fix is to emit rules into that file. Every part of that is
unimplementable, and the real fix is M0131 S5/S6 (runtime `pg_rewrite` index
maintenance + `relhasrules`).

## Evidence that it is inert

Four independent proofs. Any one of them is sufficient.

**1. The init-file format carries no rules.** `load_relcache_init_file`
(`postgres/src/backend/utils/cache/relcache.c:6167`) explicitly nulls them on
load: *"Rules and triggers are not saved (mainly because the internal format is
complex and subject to change). They must be rebuilt if needed by
`RelationCacheInitializePhase3`."* — comment at `relcache.c:6444-6451`,
followed by `rel->rd_rules = NULL; rel->rd_rulescxt = NULL;` at `:6452-6453`.
There is no rule payload to write, so "emit a PG-faithful `rd_rules` blob into
`pg_internal.init`" is not a task that can be completed.

**2. Views never enter the init file in the first place.** The writer skips any
local relation failing `RelationIdIsInInitFile` (`relcache.c:6673`), and that
predicate (`:6820-6835`) returns `RelationSupportsSysCache(relationId)` plus four
hard-coded shared-catalog special cases. `RelationSupportsSysCache`
(`postgres/src/backend/utils/cache/syscache.c:770-788`) is a binary search over
`SysCacheSupportingRelOid` — the set of catalogs a syscache is defined on. No
view is a member. So even a byte-perfect goopg init file could not legitimately
contain `pg_stat_replication` or a user view.

**3. PG deletes the file before any backend can read it.** `StartupXLOG` calls
`RelationCacheInitFileRemove()` unconditionally at
`postgres/src/backend/access/transam/xlog.c:5633` — not guarded by `InRecovery`,
and ahead of replay and of hot-standby connections. `RelationCacheInitFileRemove`
(`relcache.c:6899-6928`) unlinks `global/pg_internal.init` and then walks `base`
via `RelationCacheInitFileRemoveInDir` (`:6931-6954`), unlinking the init file in
every subdirectory whose name is all digits — so the extra `base/5` copy is wiped
too, and mode `0o400` is irrelevant because `unlink` obeys the *directory*
permissions. The only reader, `load_relcache_init_file`, is reached from
`InitPostgres`; in the standalone path `StartupXLOG` runs at
`postgres/src/backend/utils/init/postinit.c:787` and
`RelationCacheInitializePhase2` at `:818` — deletion first. In the postmaster
path the startup process runs `StartupXLOG` before any backend exists at all.
None of the three tests uses `--single`, `--boot`, or `--check`.

**4. Both implementations deliberately exclude the file from a base backup.**
Upstream lists `{RELCACHE_INIT_FILENAME, true}` in `excludeFiles`
(`postgres/src/backend/backup/basebackup.c:203`, comment: *"Skip relation cache
because it is rebuilt on startup"*), and goopg mirrors it at
`internal/server/basebackup.go:113` (`{"pg_internal.init", true}`). `copyInitFiles`
re-introduces, by hand, the one file both engines agree must not be shipped.

Proof 3 is already written down inside goopg. The comment at
`internal/initdb/pg_type_bootstrap.go:322-331` states it plainly: *"PG18's
`StartupXLOG` (xlog.c:5633) unconditionally invokes `RelationCacheInitFileRemove()`
at WAL recovery start, wiping every `pg_internal.init` copied by
`copyInitFiles()`. So every backend rebuilds tupledesc from the heap."* The
knowledge that retires the helper has been in the tree since 2026-05-18.

## Provenance

| commit | date | what it did |
|---|---|---|
| `30b0716f` | 2026-05-17 | `fix(m0106): fix relcache init file index natts + add copyInitFiles workaround`. Added the helper *and* removed `pg_internal.init` from the basebackup exclusion list. Its own body concedes the premise never held: *"PG's `load_relcache_init_file` still rejects the file silently (returns false, falls back to formrdesc for heaps only). Remaining: debug PG binary acceptance of init file format."* |
| `c09d519e` | 2026-05-18 | `feat(m0106-0010): bootstrap PG-canonical pg_type heap (step 3cq proper)`. Found and fixed the actual cause (goopg-v0 `pg_type` rows ⇒ garbage `typalign` ⇒ FATAL at `tupdesc.c:105`), documenting at `pg_type_bootstrap.go:322-331` that the init files are wiped anyway. This supersedes `30b0716f` one day later. |
| `c31afd94` | 2026-06-13 | `test(M0102-0010)`: new `e2e_checksum_replication_test.go` copies the call in. |
| `2da52113` | 2026-08-09 | `feat(cluster): M0130-S8..S10`: new `e2e_pg183_standby_full_cycle_test.go` copies it in again. |

`git log -S copyInitFiles -- internal/testport/` returns exactly those four
commits. The basebackup exclusion was independently restored (the entry is
present today at `basebackup.go:113`), so `30b0716f`'s other half is already
reverted — only the test helper survived, twice cargo-culted.

## Design

- **S10.1 — delete.** Remove `copyInitFiles` and its doc comment
  (`e2e_failover_goopg_to_pg_test.go:808-844`) and the three call sites with
  their one-line comments. No import churn expected — `os` and `filepath` stay
  used in all three files; the compiler is the check.
- **S10.2 — re-attribute the two mis-stating comments.**
  `e2e_failover_goopg_to_pg_test.go:504-509` currently reads *"…the copied
  `pg_internal.init` caches a ruleless relcache entry for the view"* (the phrase
  is on `:507`); the prose at `e2e_pg183_standby_full_cycle_test.go:336-345`
  (`:340`) says *"a goopg-built catalog
  carries no rewrite rules that PG's relcache can load (`pg_internal.init` is
  written ruleless — ledgered gap)"*. Both must say the same true thing: PG
  rebuilds its own relcache from the heap, and `RelationBuildRuleLock`
  (`relcache.c:801-805`) opens `RewriteRelationId` (2618) with
  `systable_beginscan(..., RewriteRelRulenameIndexId, ...)` and a `ScanKey` on
  `Anum_pg_rewrite_ev_class`. With `indexOK=true` there is no seq-scan fallback,
  so an index with zero entries yields `rd_rules = NULL` and the rewriter never
  substitutes the view RTE; the 42809 is then raised in `plancat.c`. goopg does
  not maintain that index at runtime — that is the gap, and S5 closes it.
- **S10.3 — correct the ledger.** Append a row superseding the
  `pg_internal.init` attribution in `.ralph/deferral_ledger.md` rows 428, 995 and
  996. Two corrections: (a) the attribution is unimplementable — the format
  carries no rules (proof 1), views never pass `RelationIdIsInInitFile`
  (proof 2), and the file is deleted at `StartupXLOG` (proof 3); (b) **the index
  OID is 2693, not 2620.** Row 428 says *"index 2620 exists"*; 2620 is
  `pg_trigger` (`postgres/src/include/catalog/pg_trigger.h:34`,
  `CATALOG(pg_trigger,2620,TriggerRelationId)`). The index
  `RelationBuildRuleLock` scans is `pg_rewrite_rel_rulename_index` = **2693**
  (`postgres/src/include/catalog/pg_rewrite.h:57`), with
  `pg_rewrite_oid_index` = 2692 at `:56`. Name S5 (runtime maintenance of 2692 +
  2693) and S6 (`relhasrules` for the six nailed system views) as the real fix.
- **S10.4 — refresh `docs/design/0130-0002-pg-class-heap-persistence.md`.**
  Guard #1 still reads *"Needs E2E PG-attach test — not yet implemented"* and
  guard #2 *"Reverse path not yet implemented"* (`:68-71`), both stale relative
  to that document's own later "Reverse-Path Implementation (2026-08-09)"
  section. Item 3 of its "Remaining for full reverse-path parity" list
  (`:150-153`) names a blocker that no longer exists — *"needs a test-harness PG
  instance lifecycle (M0130-S10)"* — the harness landed in `2da52113`; the real
  obstruction is S1's GUC gap.
- **S10.5 — file the three missing ledger rows** for items 1–3 of that list
  (system catalogs from heap; unclean PG WAL replay; the E2E PG-attach test).
  None has ever had one, which the inherited filing rule does not permit.
- **S10.6 — ledger the two gaps M0131 deliberately does not close.**
  (a) `pg_filenode.map` is **write-only** in goopg — writers at
  `internal/initdb/initdb.go:136`, `:1895-1960`, `:2166`, no reader — so catalog
  relfiles are addressed by OID, which holds on a fresh initdb and breaks the
  moment a mapped catalog is rewritten by `VACUUM FULL` / `CLUSTER` / `REINDEX`;
  the re-arm trigger is already recorded on ledger row 388 (wal-pg-identical-stream
  B0.4). (b) `replayDecodedXLogRecord` (`internal/wal/recovery.go:2207`, `default:`
  arm at `:2525` returning `unsupportedDecodedXLogRecord`) handles rmids
  0, 1, 2, 3, 4, 5, 7, 8, 9, 10, 11, 15 and 128 — missing **6 MultiXact,
  12 Hash, 13 Gin, 14 Gist, 16 SPGist, 17 BRIN, 18 CommitTs, 19 ReplicationOrigin,
  20 Generic, 21 LogicalMessage** (numbering per
  `postgres/src/include/access/rmgrlist.h:28-49`, IDs assigned in file order).
  6, 18 and 19 are **not** index AMs and do occur in ordinary PG workloads, so
  `0130-0002`'s framing — *"requires implementing the corresponding index AMs"*
  (`:148-149`) — understates the surface.

Land S10 early. Leaving the helper in place while S3 and S4 write new cold-start
tests risks a fourth copy.

## Guards

1. `grep -rn copyInitFiles internal/` returns nothing; `go vet ./internal/testport/`
   is clean and no import becomes unused.
2. `go test -run '^TestE2E_PGStandbyFullCycle$' ./internal/testport/` stays green
   — same pass/fail shape as before the deletion. Because the file was deleted by
   `StartupXLOG` anyway, removing the copy must change nothing; a behaviour change
   here would falsify the analysis above and is a finding, not a flake.
3. `go test -run '^TestE2E_FailoverGoopgToPG$' ./internal/testport/` and
   `-run '^TestE2E_ChecksumStreamingGoopgToPG$'` stay green.
4. The 42809 probe at `e2e_failover_goopg_to_pg_test.go:510-512` still fails
   before S5/S6 and still logs its promote-the-gate message after — deleting the
   workaround neither fixes nor worsens the view gap.
5. No comment anywhere under `internal/` or `.ralph/` attributes the view gap to
   `pg_internal.init` without also citing this document's correction.
6. `make ralph-state-guard` accepts the ledger edits; `gh api /markdown` renders
   the new rows without nesting (raw `<table>` hazard).
7. UNITS + SMOKE green.

## References

- `internal/testport/e2e_failover_goopg_to_pg_test.go:808-844` — `copyInitFiles`;
  `:154`, `:504-509`, `:510-512` — call site, mis-stating comment, 42809 probe
- `internal/testport/e2e_pg183_standby_full_cycle_test.go:99`, `:336-345`
- `internal/testport/e2e_checksum_replication_test.go:122`
- `internal/initdb/pg_type_bootstrap.go:322-331` — the in-tree proof (c09d519e)
- `internal/server/basebackup.go:113` — `{"pg_internal.init", true}`
- `internal/wal/recovery.go:2207`, `:2525` — `replayDecodedXLogRecord` and its
  `default:` arm; `internal/wal/xlog_record.go:53-96` — the handled rmid set
- `postgres/src/backend/access/transam/xlog.c:5633` —
  `RelationCacheInitFileRemove()` in `StartupXLOG`
- `postgres/src/backend/utils/cache/relcache.c:6167` —
  `load_relcache_init_file`; `:6444-6453` — rules are not saved; `:6673` — writer
  skip; `:6820-6835` — `RelationIdIsInInitFile`; `:6899-6954` —
  `RelationCacheInitFileRemove` + `…InDir`; `:801-805` — `RelationBuildRuleLock`
- `postgres/src/backend/utils/cache/syscache.c:770-788` —
  `RelationSupportsSysCache`
- `postgres/src/backend/utils/init/postinit.c:787` / `:818` — `StartupXLOG`
  before `RelationCacheInitializePhase2`
- `postgres/src/backend/backup/basebackup.c:203` — upstream exclusion
- `postgres/src/include/catalog/pg_rewrite.h:56-57` — indexes 2692 / **2693**;
  `postgres/src/include/catalog/pg_trigger.h:34` — 2620 is `pg_trigger`
- `postgres/src/include/access/rmgrlist.h:28-49` — rmgr id assignment
- deferral-ledger rows 388, 428, 490, 995, 996
- `docs/design/0130-0002-pg-class-heap-persistence.md:66-74`, `:138-153`
- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S10
