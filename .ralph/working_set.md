# Working set — M-NIGHTLY AI-20260817-011734-004/-005 (LANDED `c90da521`)

**Task:** M-NIGHTLY nightly triage, items -004 and -005. Selected per the Current
Priority banner (M-NIGHTLY outranks M0134).

**Landed (test-harness only, zero production-code change):**
- **-005** `TestPort_PgoutputInteropPGToGoopgBpcharPadding` — a **harness race**,
  not an engine bug. The test does 4 INSERTs then a value-only `UPDATE ... SET
  filler='new' WHERE id=1`, but `PubSubCluster.WaitForRow` polls only
  `count(*)`, which the INSERTs already satisfy — so it returned pre-UPDATE and
  the assertion read `'ab'` (length 2, want 3). Deterministic (3/3), not flaky.
  Fix: new `PubSubCluster.WaitForScalar(t, query, want, timeout)` (reports the
  last-observed value on timeout, so a genuinely wrong applied value still
  surfaces) + unit test; the test waits on it before its unchanged assertions.
  Swept all other `WaitForRow` callers in `pgoutput_interop_test.go` — no other hit.
- **-004** `TestPort_IsolationIndexOnlyBitmapscan` — **stale**, closed with no code
  change: PASSes at HEAD in 3.66 s, already fixed by `6536bdf5`.

**Ruled out (do not re-investigate):** the bpchar codec path is correct.
`parsePgoutputText` (`internal/executor/applyworker.go`) deliberately has no
bpchar branch — the padded `bpcharout` wire value is re-trimmed by
`coerceTextLikeDatum` (`internal/executor/codec.go`), matching
`varchar.c::bpcharin`/`bpcharlen`. A bpchar branch there is a no-op "fix".

**Invariant (do not regress):** `WaitForRow` can only observe a change that moves
a row count. Any assertion about an UPDATE — or anything leaving `count(*)`
unchanged — needs `WaitForScalar`.

**Files:** `internal/testutil/pubsubcluster/cluster.go` + `cluster_test.go`,
`internal/testport/pgoutput_interop_test.go`,
`docs/design/0103-0047-m0103-0007-rung-24-bpchar-padding.md` (new §"Follow-up
2026-08-17"), `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

**Gates run:** `go build ./...` PASS; `internal/testutil/pubsubcluster` PASS;
target test FAIL-pre / 3/3 PASS-post; `RALPH_PRECOMMIT_SCOPE=units` PASS (warm
cache, 3.2 s); pre-commit pgbench smoke PASS. No tpch-spotcheck — no
planner/executor/codec file was touched.

**Deferral ledger:** 1 row (2026-08-17) — the harness has **no apply-position
(LSN) wait primitive**; all sync is value polling, so an event changing NO
subscriber-visible value stays unwaitable. Resume: add `WaitForCatchup` modeled
on `Cluster.pm::wait_for_catchup`, but first check whether goopg exposes
`pg_stat_subscription.latest_end_lsn` / `pg_replication_origin_progress` at all.

**Next step:** M-NIGHTLY run `20260817-011734` has 2 unchecked items left.
Take **AI-20260817-011734-006** (`TestPort_RegressWedgeProbeNamesTheStuckStatement`)
next — already triaged this loop and still failing at HEAD: a **probe-tooling**
bug, not an engine bug. Its goroutine dump captures the test harness's own
`runtime/pprof.writeGoroutineStacks` frames instead of the goopg server
subprocess (`regress_wedge_probe_guard_test.go:94`); the probe's
`pg_stat_activity` + RSS/log-tail capture already work. Fix = point the dump
collector at the server subprocess. Item -001 (race/internal/initdb) is still
expected stale (run predates `83dd7ae8`); verify with
`make race-gate RACE_TIMEOUT=45m RACE_SHARD_ONLY=1` when a ~20 min slot is free.

**Delegation:** `tmp/ralph-handoffs/m-nightly-20260817-testport-triage/` (tester,
DONE — 3 verdicts) and `tmp/ralph-handoffs/m-nightly-20260817-bpchar-pgoutput/`
(researcher DONE, tester determinism+timing probe DONE, implementer DONE 1 round).
The researcher's race hypothesis was NOT taken on trust — the tester probe
discriminated it from an apply-path bug before any code was written.

**In-flight:** none.
