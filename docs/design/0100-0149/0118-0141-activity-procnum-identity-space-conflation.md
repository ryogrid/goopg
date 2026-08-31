# 0118-0141 — pg_stat_activity dynamic updates silently no-op'd (procNum identity-space conflation)

**Milestone:** M0118 (isolation-spec triage, nightly regression follow-up)
**Trigger:** nightly batch 20260715-010036, `AI-20260715-010036-006/007/008` — three
previously-`pass_required` isolation specs regressed: `partition-drop-index-locking`,
`insert-conflict-specconflict`, `detach-partition-concurrently-4`.
**Status:** landed (root-caused and fixed; all three specs pass again).

## Symptom

All three specs diverged on `pg_stat_activity`-derived output: `s.query` blank for
every backend regardless of state, `s.state` frozen at its connection-time default,
and (for `detach-partition-concurrently-4`) permutation step ordering shifted —
each is a downstream symptom of the SAME root cause, not three independent bugs.
Reproduced live with `psql` against a manually started server: a session running
`select pg_sleep(1);` showed `state=active` (correct, coincidentally) but
`query=''` even for its OWN currently-executing statement — not just on idle,
which ruled out 0118-0073's "clear on idle" bug class outright (already fixed,
and unit-tested in isolation) and pointed at the write never reaching the row a
concurrent reader later scans.

## Root cause

`ActivityRegistry` has always used **two independent slot-index spaces** for the
same live connection, and nothing enforced they agree:

1. `ActivityRegistry.Register(b)` (registry.go) computes the backend's own slot
   via `procNumForPID(b.PID)` — `(pid-1) % nReg`, a hash of the wire-protocol PID.
2. Every dynamic per-statement call — `UpdateState`, `WaitEventStart/End`,
   `PIDForProcNum` (`internal/executor/context.go`, lock-wait bookkeeping) — is
   keyed off `connTx.ProcNum`, which `internal/server/server.go`'s connection
   setup assigns from `s.cfg.TxnMgr.AcquireConnSlot()`: an MVCC proc-array slot,
   unrelated to the PID hash.

`server.go`'s own comment documents *why* `AcquireConnSlot()` exists — it
replaced a historical `(pid-1) % ConnSlotCount` assignment that wrapped and
clobbered live sessions' MVCC slots under connection churn. That MVCC-side fix
correctly solved the wrapping bug, but left `reg.Register(b)` on the OLD
PID-hash scheme, so from that point on `connTx.ProcNum` (the real, live,
collision-free slot) and the activity registry's own internally-chosen slot
silently diverged for most connections. Every `UpdateState(connTx.ProcNum, …)`
call wrote to whatever unrelated (usually-unregistered) slot the MVCC
allocator happened to hand out, while `Snapshot()` kept reading the backend's
*actual* registered slot (from the PID hash) — which never received a write
past its `Register()`-time defaults (`state="active"`, `query=""`). Since a
fresh connection's default state already reads `"active"`, the state column
looked plausible by accident; only `query` (default `""`, never legitimately
`"active"` by coincidence) made the gap visible.

This is a correctness gap in the activity registry's connection-identity
wiring, not in `UpdateState`'s idle-retention logic from 0118-0073 (which is
still correct and unit-tested) — the primitive worked, the wiring to it did
not. Confirmed no capacity conflict blocks unifying the two spaces:
`internal/initdb/open.go` sizes the production registry as
`NewActivityRegistry(mvcc.DefaultProcArraySize)` (1024, `nReg=1024`), and
`TxnMgr.AcquireConnSlot()` only ever returns values in
`[0, mvcc.ConnSlotCount)` = `[0, 960)` — well inside `nReg`, with the
background-worker range (`bgBase=1024`) untouched.

## Fix

`internal/activity/registry.go`: added `RegisterAt(procNum int32, b *Backend)`,
mirroring the existing `RegisterBackground(idx, b)` pattern (an explicit,
caller-chosen slot instead of a derived one). `Register(b)` now delegates to
`RegisterAt(r.procNumForPID(b.PID), b)`, so its behavior — and every existing
caller/test using the PID-hash slot — is unchanged.

`internal/server/server.go`: the one production call site now calls
`reg.RegisterAt(procNum, &activity.Backend{…})`, reusing the SAME `procNum`
already computed via `TxnMgr.AcquireConnSlot()` a few lines above (and already
used for `WaitEventStart`/`gls.SetBackendID`/`connTx.ProcNum` itself) — closing
the two index spaces into one. `pidMap[pid]` still resolves to the correct
slot for all "ByPID" lookups (`UpdateStateByPID`, `GetBackendTypeByPID`,
`Unregister`), since those go through `pidMap`, never through
`procNumForPID` directly.

## Verification

- Manual repro: built a standalone server, confirmed via raw `psql` that
  `pg_stat_activity.query`/`state` now update correctly both mid-statement and
  on return to idle (a second, concurrent connection sees the first's retained
  last-query text once idle).
- `go test ./internal/activity/... ./internal/server/... ./internal/executor/... ./internal/initdb/...`: PASS.
- `go test -v -run 'TestPort_IsolationPartitionDropIndexLocking|TestPort_IsolationInsertConflictSpecconflict|TestPort_IsolationDetachPartitionConcurrently4' ./internal/testport/`: all three PASS (were FAIL in the nightly run).
- Full isolation battery: `go test -run 'TestPort_Isolation' ./internal/testport/ -v` — 0 `--- FAIL` lines.
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`: PASS, 0 failed transactions (all 3 pgbench workloads).

## Other nightly items from run 20260715-010036 (not this bug)

The same nightly run also flagged `regress/errors`, `regress/portals_p2`,
`regress/select` as regressed, and `units` timeouts across `cmd/goopg`,
`internal/amcheck`, `internal/initdb`, `internal/mvcc` (each killed at their
33-minute per-package `go test` timeout with a near-empty goroutine dump —
consistent with host CPU starvation, not a hang in the tested code) plus a
`internal/wal` flake (`TestStripeAppendConcurrentDrainConsistency`: "drain
goroutine never ran"). All three regress suites and a full `internal/initdb`
re-run reproduced clean and fast locally (`errors`/`portals_p2`/`select` PASS
in isolation; `internal/initdb` completed in ~4 minutes, not 33). These are
recorded as an open deferral-ledger row (environmental, not this loop's fix)
rather than silently dropped — see `.ralph/deferral_ledger.md`.
