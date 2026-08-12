(idle — nothing in flight)

M0131-S13 LANDED (loop #12 of this run), pushed.

Files: `internal/initdb/initdb.go` (`pgProcRow` — the nullable-varlena group now
`executor.NullDatum`; `pgProcColDefs` CATALOG_VARLEN comment rewritten), new
`internal/initdb/pg_proc_nullable_varlena_test.go`,
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go` (S13.4 inversion),
design `0131-0004` §Findings F2 + README index, fix_plan S13 checked, 1 ledger row.

Worth carrying:
- The bug was a STALE SIBLING, not a bitmap/`t_hoff` defect (S13.1's hypothesis
  was wrong). goopg has TWO builders for the physical 30-col `pg_proc` row:
  runtime `buildPGProcRow` (`internal/executor/sys_pg_proc.go`) had `NullDatum`
  all along with a comment explaining why; initdb's `pgProcRow` had
  `NewStringDatum("")`. That falls through `encodeValuePG` →
  `emptyArrayTypeBytes` = a NON-NULL zero-dimension ArrayType. `NullBitmapPG` /
  `writeMultiPageHeapRows` were already correct — nothing was NULL to mark.
- Generalisable: `NewStringDatum("")` on a nullable varlena catalog column is
  never a NULL in this codebase; it is an empty-array/empty-varlena shell, and
  PG branches on `heap_attisnull` for all of them.
- `pg_attribute` was already fixed for the identical reason (Step 3u,
  `attoptions` → ERRORDATA_STACK_SIZE PANIC). `pg_class` (`pgClassNailedRow`,
  ~initdb.go:5986) is STILL WRONG: `relacl='{}'`, `reloptions='{}'`,
  `relpartbound=''`. Latent only because the hosted-PG E2E connects as a
  superuser, who bypasses `pg_class_aclcheck`. Ledgered with a non-superuser
  probe as the resume point — do NOT fix it blind.

Gates: UNITS precommit PASS, `internal/{initdb,executor}` PASS (76s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS (the S13 acceptance measurement:
`'a'::text || 1` = `a1`, `proconfig IS NOT NULL` count 3397 → 0),
`TestE2E_PGStandbyFullCycle` + `TestE2E_FailoverGoopgToPG` PASS, pgbench smoke
via the commit hook, `make ralph-state-guard` OK (auto-repaired the stale marker).
Fail-when-broken proven: restoring `NewStringDatum("")` on proconfig fails the
new guard at `oid=3 (heap_tableam_handler)`.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): S24 (MultiXact) and S30 are
the large open ones; S14 (`atthasmissing`/`attmissingval`, est ~2, and F3 is the
next finding in the same E2E) is the natural successor to this loop.

In-flight: none.
