# M0106-0010 Step 3di — SEGV chain eliminated; new blocker is pg_stat_wal_receiver view absence

**Status**: accepted (2026-05-18)
**Milestone**: M0106-0010 (PG Relcache Init File Compatibility)
**Previous step**: 3dh (pg_database_datname_index name-typed descriptor + seeded leaf)
**Next step**: 3dj — seed `pg_stat_wal_receiver` (and underlying
`pg_stat_get_wal_receiver` function) as physical pg_class/pg_attribute/pg_rewrite/pg_proc
rows so the PG standby can resolve the syscache lookup that the
`TestE2E_FailoverGoopgToPG/async` poll loop performs.

## Summary

Re-ran `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` with the
Step 3dh fix in place. **The PG-against-goopg SIGSEGV chain that has dominated
Step 3 since Step 3da is gone.** The PG standby completes its full startup
sequence: archive recovery, consistent recovery state, hot-standby promotion,
walreceiver dial, WAL streaming, and hot-standby feedback. The next failure is
a normal SQL-level error (`42P01: relation does not exist`) — not a crash and
not a startup FATAL.

This closes the 9-step SEGV-attribution arc that began with Step 3da.

## Symptom (post-3dh)

`pg.log` excerpt — the standby's full startup sequence executes cleanly:

```
2026-05-18 13:24:47.879 [...] DEBUG: starting up replication slots
2026-05-18 13:24:47.879 [...] DEBUG: starting up replication origin progress state
2026-05-18 13:24:47.886 [...] DEBUG: initializing for hot standby
2026-05-18 13:24:47.889 [...] LOG:   completed backup recovery with redo LSN 0/4210 and end LSN 0/4210
2026-05-18 13:24:47.889 [...] LOG:   consistent recovery state reached at 0/4288
2026-05-18 13:24:47.889 [...] LOG:   database system is ready to accept read-only connections
2026-05-18 13:24:47.892 [...] DEBUG: updating PMState from PM_RECOVERY to PM_HOT_STANDBY
2026-05-18 13:24:47.894 [...] LOG:   started streaming WAL from primary at 0/0 on timeline 1
2026-05-18 13:24:47.894 [...] DEBUG: sending hot standby feedback xmin 0 ...
```

The test then advances to `waitForPhysicalStreamingGoopgToPG`, which polls the
standby for replication status. The standby responds with a clean SQL error:

```
2026-05-18 13:24:47.990 GMT [1358451] DEBUG:  42P01: relation
    "pg_catalog.pg_stat_wal_receiver" does not exist at character 20
2026-05-18 13:24:47.990 GMT [1358451] ERROR:  42P01: relation
    "pg_catalog.pg_stat_wal_receiver" does not exist at character 20
2026-05-18 13:24:47.990 GMT [1358451] STATEMENT:  SELECT status
    FROM pg_catalog.pg_stat_wal_receiver
```

`internal/testutil/pgcluster/cluster.go::QueryScalar` calls `t.Fatalf` on any
non-zero psql exit, so the wait loop in
`internal/testport/e2e_failover_goopg_to_pg_test.go::waitForPhysicalStreamingGoopgToPG`
(line 325) fails the test on the very first poll instead of retrying.

## Root cause

`pg_stat_wal_receiver` is **not** a built-in catalog row written by PG's
bootstrap C code; it is a SQL `CREATE VIEW` that `initdb` materialises by
executing `postgres/src/backend/catalog/system_views.sql` against the
freshly-bootstrapped cluster:

```sql
-- postgres/src/backend/catalog/system_views.sql:945
CREATE VIEW pg_stat_wal_receiver AS
    SELECT s.pid, s.status, s.receive_start_lsn, s.receive_start_tli,
           s.written_lsn, s.flushed_lsn, s.received_tli,
           s.last_msg_send_time, s.last_msg_receipt_time,
           s.latest_end_lsn, s.latest_end_time,
           s.slot_name, s.sender_host, s.sender_port, s.conninfo
    FROM pg_stat_get_wal_receiver() s
    WHERE s.pid IS NOT NULL;
```

`pg_stat_get_wal_receiver()` is a set-returning C-language function in
`postgres/src/backend/utils/adt/pgstatfuncs.c` registered in
`pg_proc.dat` (OID 3317).

goopg currently models `pg_stat_wal_receiver` as a **virtual** view — see
`internal/initdb/replication_views.go::registerStatWalReceiverView` and the
runtime materialiser in `internal/wal/replmon.go`. The virtual view is only
visible to goopg's own catalog/planner; **no row** is written into the
physical `pg_class`, `pg_attribute`, `pg_rewrite`, or `pg_proc` heap pages.
When `pg_basebackup` clones the goopg cluster and a real PG postmaster boots
on top of those data files, PG sees the (absent) row state and the syscache
returns NULL — hence the `42P01` error.

The same pattern almost certainly holds for sibling views and SRFs PG's
own monitoring/admin scripts query (`pg_stat_replication`,
`pg_stat_subscription`, `pg_replication_slots`, etc.). Step 3dj will discover
which of those the failover E2E or follow-up admin queries trip first.

## What's not in scope for 3di

No code change. This step is purely the diagnostic/scoping bookend on the SEGV
chain Step 3da–3dh laid down. Step 3di intentionally does not:

* materialise `pg_stat_wal_receiver` as physical pg_class+pg_rewrite (3dj),
* seed `pg_stat_get_wal_receiver` as a physical pg_proc row (3dj prerequisite),
* widen the test helper to tolerate `42P01` (would mask, not solve, the gap),
* enumerate every other virtual view that needs the same treatment
  (deferred until 3dj surfaces it through the E2E).

## Why declaring this a milestone (not a stub fix)

Steps 3a→3dh have been a sustained, often slow attribution arc against
SIGSEGVs and FATALs that crashed the PG standby before it could utter a single
log line at the SQL level. Every prior step landed a single byte-layout fix
or seed of a missing catalog row. The cumulative effect is that the PG18
binary now:

1. Reads goopg's pg_control, WAL, and base files without abend.
2. Replays the WAL stream, including the bootstrap commit record.
3. Resolves every pg_class/pg_attribute/pg_proc/pg_type/pg_database/pg_authid
   syscache lookup needed to authenticate `postgres@postgres` and bring up a
   read-only backend.
4. Dials `libpqwalreceiver` and streams from the goopg primary.

The remaining gap is **semantic**, not structural: PG needs a few system
views that goopg has historically modelled in-process. Step 3dj is the first
of a small sequence of view/SRF seeding tasks (probably 3–6 sub-steps) to
close the gap.

## Regression artefacts

* Full re-run captured at `tmp/m0106-step3di/e2e_run.log` (not committed; see
  `/tmp/e2e_failover.log` from the loop run for the canonical capture).
* No new Go regression test added: the existing
  `TestE2E_FailoverGoopgToPG/async` is the regression for the SEGV chain — if
  any future change reintroduces an early-startup crash, that test will fail
  *before* the pg_stat_wal_receiver poll, attributing the regression.
* The `TestNailedPgDatabaseDatnameIndexHasNameDescriptor` and
  `TestBootstrapPgDatabaseDatnameIndexWritesPopulatedBtree` pins from 3dh
  remain the lowest-level guard on the descriptor and seed bytes that closed
  the chain's final hop.

## Step 3dj scope preview

1. Seed `pg_stat_get_wal_receiver` (OID 3317) as a physical pg_proc row,
   following the Step 3a pattern for AM handlers. The function is C-language
   `srf` — proretset=true, prorettype=record, with an `OUT` argument list
   matching the columns the view's SELECT references. The pg_proc.dat row is
   the source of truth for prorows, proargtypes, proallargtypes, proargmodes,
   proargnames.
2. Seed `pg_stat_wal_receiver` as a physical pg_class row (relkind='v'),
   pg_attribute rows for its 16 columns, and a pg_rewrite row carrying the
   rule action (SELECT … FROM pg_stat_get_wal_receiver() s WHERE …).
3. Add `pg_stat_wal_receiver` to `nailedLocalRels` / relcache init coverage if
   needed for the PG standby to find it without a fresh catalog scan.
4. Re-run E2E; the next failure (if any) attributes Step 3dk. Plausible
   candidates: `pg_stat_replication` (similar shape), `pg_replication_slots`,
   or `pg_stat_activity`.
