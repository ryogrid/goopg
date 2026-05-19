# M0106-0010 Step 3cy — E2E Standby PG Log Capture + Type 23 Cache Miss

Status: LANDED (diagnostic step) — 2026-05-18

## Goal

Step 3cx (pg_authid OS-user role + indexes) eliminated the FATAL
`28000: role "ryo" does not exist` standby-boot blocker but its
verification run only saw a Go test deadline at 300s with a goroutine
dump. The standby PG `pg.log` (where the real PG server records its
FATAL/ERROR lines) was never printed to the test's stdout because
`TestE2E_FailoverGoopgToPG` only emitted the standby log on
`WaitReady` failure — and we believed `WaitReady` was succeeding.

Step 3cy is a pure diagnostic step: widen the timeout, *always* dump
the standby PG log, and identify the next blocker by name.

## Test-side diagnostic improvement (permanent)

`internal/testport/e2e_failover_goopg_to_pg_test.go::runFailoverGoopgToPG`
now installs an unconditional `t.Cleanup` immediately after
`standbyDir` is known. The cleanup reads
`<baseDir>/pg.log` (the path that
`pgcluster.OpenExisting` derives from `DataDir`) and emits it under a
greppable `[m0102-pg-standby-log]` tag. The pre-existing
WaitReady-only dump is retained (it fires earlier, before
`pgcluster.Cluster.Stop()` is invoked, so it still adds value if
WaitReady is the failure point), but the new cleanup guarantees we
get the log even when the test fails much later — for example inside
`waitForPhysicalStreamingGoopgToPG` where each `t.Fatalf` triggers a
deferred `standby.Stop()` that can itself hang if PG is mid-crash.

The dump is harmless in the success case (a few thousand DEBUG lines
under `-v`); the visibility win on every future regression is
load-bearing.

## What the dump revealed

Reproduction: `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run
'TestE2E_FailoverGoopgToPG/async$' -timeout 600s ./internal/testport/`,
captured to `tmp/m0106-step3cy/run1.log` (~38 K lines, mostly PG
DEBUG output the standby produced over its retry cycles).

The standby gets significantly further than under Step 3cw:

1. ✅ `28000: role "ryo" does not exist` is GONE — no occurrences in
   38 K lines of standby log. Step 3cx confirmed working at the
   integration layer.
2. ✅ `StartupXLOG`, `read_backup_label`, and `InitWalRecovery`
   proceed normally. Backup-label parses; redo LSN 0/4210 reached.
3. ✅ `consistent recovery state reached at 0/4288` — the standby
   transitions to `PM_HOT_STANDBY`.
4. ✅ Walreceiver connects to the goopg primary and streams:
   `started streaming WAL from primary at 0/0 on timeline 1` plus
   `sending write 0/6370 flush 0/6370 apply 0/6370` reply traffic.
5. ❌ **NEW blocker (first order)** — the very first client backend
   that runs the test's `SELECT 1` probe via `WaitReady`/QueryScalar
   errors out:
   ```
   ERROR:  XX000: cache lookup failed for type 23
   LOCATION:  TupleDescInitEntry, tupdesc.c:896
   STATEMENT:  SELECT 1
   ```
6. ❌ **NEW blocker (second order)** — the *next* backend on the
   same postmaster crashes harder:
   ```
   LOG:  client backend (PID 1130044) was terminated by signal 11:
   Segmentation fault
   LOG:  terminating any other active server processes
   ```
   That SIGSEGV pushes the postmaster into `HandleChildCrash` →
   `terminating any other active server processes` → reinitialise.
   The cycle then repeats indefinitely (every ~1.5s a new postmaster
   generation: re-`StartupXLOG`, hit consistent recovery, accept a
   client, ERROR XX000, SIGSEGV, restart).

The test wall-clock symptom is therefore: `WaitReady` returns
"not ready within 1m30s" because every `SELECT 1` returns an ERROR;
`waitForPhysicalStreamingGoopgToPG` then t.Fatalf's because the
standby keeps cycling out of `streaming`; its deferred
`standby.Stop()` blocks in `cmd.Wait()` because pg_ctl can't gracefully
stop a postmaster mid-crash-recovery, so the Go test deadline (10
minutes) is what finally kills the run. That matches the Step 3cx
"300s timeout, stack dump only" observation — under the previous
300s budget the dump landed inside `standby.Stop`'s `Wait()` rather
than inside the underlying failure, and the standby PG log was
discarded with `t.TempDir`.

## Interpretation of the type-23 cache miss

OID 23 is the canonical pg_type oid for `int4`. `TupleDescInitEntry`
at `postgres/src/backend/access/common/tupdesc.c:896` is the path
that fills `TupleDesc->attrs[i].atttypid` etc. while building a
tuple descriptor — typically from `pg_attribute` rows read out of
the relcache. The lookup it performs is
`SearchSysCache1(TYPEOID, ObjectIdGetDatum(typeOid))`, which goes
through the pg_type heap via `pg_type_oid_index` (oid 2703). The
ERROR message comes from the `getBaseTypeAndTypmod` path when
syscache TYPEOID returns no row.

`int4` is the canonical built-in type that *every* catalog tuple
uses (oid, attnum, etc.), so a syscache miss for OID 23 is not a
"missing exotic type" gap — it is a structural failure to read
pg_type at all. Three candidate root causes, in decreasing
likelihood:

A. **`pg_type_oid_index` (oid 2703) heap-OID encoding bug** — the
   index is populated but a leaf entry for `(23, …)` is missing, or
   the BlockId/OffsetNumber encoding for one of the leaf tuples is
   wrong such that the syscache scan finds zero matches. Step 3s's
   on-disk LE-uint32 trap, and the Step 3cx note that
   `TestPgBuildIndexTupleNameKeyLayoutMatchesPG18` was added
   *specifically* to catch a similar regression in the name-keyed
   builder, both point at index-tuple-encoding being the riskiest
   surface in this family.

B. **`pg_type` heap missing the int4 row at the standby** — would
   imply the basebackup or the WAL stream is overwriting the OID-23
   tuple, or the bootstrap pg_type seed is no longer reachable
   through the relfilenode visible to the standby's recovery view.

C. **`pg_internal.init` mismatch** — the relcache init file we
   copy in (`copyInitFiles`) advertises a tuple descriptor for some
   nailed relation that references a type oid pg_type doesn't have.
   Less likely because the symptom is "SELECT 1" — which goes
   through pg_class/pg_attribute for plain VALUES, not a nailed rel
   exotic enough to need init-file overrides.

Following Step 3cw/3cx, byte-exact verification of the populated
`pg_type_oid_index` against PG18 (per (A)) is the lowest-risk first
move and is queued as Step 3cz. The (B) and (C) hypotheses get
verified by `pg_filedump`/`hexdump` on the standby's pg_type
relfilenode + leaf scan of `global/2703` (pg_type is per-database
not shared, so this is `base/<dbid>/2703` — and the test
basebackup's exact path is captured in run1.log's
`backup label TestE2E_FailoverGoopgToPG` lines).

The follow-on SIGSEGV (item 6) is *not* a separate blocker yet — a
crash inside an InitPostgres-time backend that just failed type
lookup very plausibly explains it (NULL TupleDesc passed downstream
to an Assert-less path on a Release build, or shared-memory state
left half-initialised). The triage decision in Step 3cz will be
"fix the type-23 lookup, then re-run; if SIGSEGV survives, treat as
3da."

## Verification

* `go vet ./internal/testport/` — clean.
* `go test -v -run 'TestE2E_FailoverGoopgToPG/async$' -timeout 600s
  ./internal/testport/` — fails as expected with the
  `cache lookup failed for type 23` ERROR + SIGSEGV crash loop now
  visible under the new `[m0102-pg-standby-log]` tag. Output
  archived to `tmp/m0106-step3cy/run1.log`.
* Cross-package smoke not run (no production-code change other than
  the always-on log dump in a test helper).

## Files touched

* `internal/testport/e2e_failover_goopg_to_pg_test.go` —
  `t.Cleanup` block added immediately after `standbyDir` is set.
* `docs/design/0106-0010-step3cy-e2e-standby-log-capture-and-type-23-cache-miss.md`
  (this file).
* `docs/design/README.md` — index entry.
* `.ralph/fix_plan.md` — Step 3cy LANDED entry + Step 3cz queued.
