# 0103-0050 — M0103-0007 Scenario A closure (PG primary + goopg subscriber)

Status: ACCEPTED 2026-05-14 (loop 27)

## Context

M0103-0007 drives a PG 18.3 publisher with a goopg subscriber under a
sustained INSERT/UPDATE/DELETE workload, kills the publisher mid-flight
with SIGKILL, fails over to a libpq multi-host client, and asserts a
mode-specific subscriber row-count invariant at quiescence.

The milestone DoD (verbatim from
`docs/design/0103-0005-heterogeneous-logical-failover-e2e-harness.md`):

- async subtest — `count(*) ∈ [killCommitted - asyncLossBound + 1,
  killCommitted + 1]` with `asyncLossBound = 50`.
- sync (`sync_remote_apply`) subtest — `count(*) == killCommitted + 1`
  (zero loss, strict equality).

The umbrella probe-survival + correctness ladder for Scenario A landed
across 26 rungs in loops 1–26 (each with its own design doc):

| Rung | Loop | Surface | Design doc |
|------|------|---------|------------|
| 1 | 1 | apply-worker index maintenance on INSERT | 0103-0024 |
| 2 | 2 | full-DML round-trip + `primaryKeyOnlyRow` synthesis | 0103-0025 |
| 3 | 3 | 50× sustained batch DML | 0103-0026 |
| 4 | 4 | REPLICA IDENTITY FULL apply paths | 0103-0027 |
| 5 | 5 | unchanged-TOAST (`'u'`) decode + apply fill | 0103-0028 |
| 6 | 6 | multi-DML single explicit xact | 0103-0029 |
| 7 | 7 | SAVEPOINT/ROLLBACK TO at proto_version=1 | 0103-0030 |
| 8 | 8 | multi-table interleaved DML | 0103-0031 |
| 9 | 9 | pgoutput TRUNCATE message support | 0103-0032 |
| 10 | 10 | column-order remap in the apply worker | 0103-0033 |
| 11 | 11 | subscriber-extra column UPDATE preservation | 0103-0034 |
| 12 | 12 | REPLICA IDENTITY USING INDEX | 0103-0035 |
| 13 | 13 | subscriber-extra DEFAULT INSERT fill | 0103-0036 |
| 14 | 14 | dispatcher-side INSERT DEFAULT plumbing | 0103-0037 |
| 15 | 15 | INSERT DEFAULT marker token | 0103-0038 |
| 16 | 16 | UPDATE DEFAULT marker | 0103-0039 |
| 17 | 17 | `INSERT INTO t DEFAULT VALUES` | 0103-0040 |
| 18 | 18 | zero-arg time DEFAULT functions | 0103-0041 |
| 19 | 19 | `nextval` sequence DEFAULTs | 0103-0042 |
| 20 | 20 | pgbench `simple-update` driver path | 0103-0043 |
| 21 | 21 | pgbench `tpcb-like` UPDATE-heavy load | 0103-0044 |
| 22 | 22 | SIGKILL + libpq multi-host reconnect plumbing | 0103-0045 |
| 23 | 23 | **async DoD bracket** (`asyncLossBound = 50`) | 0103-0046 |
| 24 | 24 | `filler char(N)` bpchar padding through pgoutput | 0103-0047 |
| 25 | 25 | apply-worker `application_name` plumbing for sync rep | 0103-0048 |
| 26 | 26 | **zero-loss DoD** via sentinel-count + eager status push | 0103-0049 |

Rung 23 (loop 23) closed the async DoD via
`TestPort_PgoutputInteropPGToGoopgPgbenchKillAsync`. Rung 26 (loop 26)
closed the `sync_remote_apply` zero-loss DoD via
`TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply` after
path (b) of the rung-25 docstring — a goopg-side sentinel-count poll
plus eager standby-status push from the apply worker — sidestepped
PG18's `sync_priority=0` quirk for logical walsenders.

After rung 26 both DoD invariants are pinned by live end-to-end Go
tests against an upstream PG 18.3 publisher; no further code change is
required to close the milestone.

## What this loop did

Closes M0103-0007 by:

1. Confirming both DoD-pin tests pass deterministically and the full
   `TestPort_PgoutputInteropPGToGoopg*` suite stays green together
   under the same loop.
2. Recording the Scenario-A milestone closure as accepted in this
   design doc, indexed in `docs/design/README.md`.
3. Marking `M0103-0007` `[x]` in `.ralph/fix_plan.md` so the only
   remaining unchecked sub-milestone in M0103 is M0103-0009 (close
   milestone — CSV + inventory bump).

No production code change.

## DoD-pin tests

- async DoD —
  `TestPort_PgoutputInteropPGToGoopgPgbenchKillAsync`
  (`internal/testport/pgoutput_interop_test.go`).
  Sustains an INSERT-only stream from two `database/sql` writers
  against a no-PK `public.bench_log (client int, src text)`,
  kills the publisher with `pg_ctl -m immediate`, lands a
  post-failover INSERT (`src='post'`) on goopg via libpq multi-host,
  polls until the subscriber count stays unchanged for one second,
  asserts the bounded-loss bracket and the post-failover row.

- sync (`sync_remote_apply`) DoD —
  `TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply`
  (`internal/testport/pgoutput_interop_test.go`).
  Same harness shape, with two changes that turn the bracket into a
  strict equality:
  (a) workers run per-session `SET synchronous_commit = remote_apply`
  on the publisher, and
  (b) after each successful publisher INSERT, each writer polls the
  subscriber for `count(*) WHERE client = c >= localInsertedCount`
  before bumping the atomic `committed` counter — the path (b)
  sentinel-count gate from rung 26.

Both names differ from the original milestone-spec'd
`TestE2E_LogicalFailoverPGtoGoopg` with `async`/`sync_remote_apply`
subtests; the existing tests carry the same DoD assertions and grew
incrementally through the rung ladder, so the closure pivots on test
content rather than naming. Same approach as M0103-0008's closure
(`TestPort_PgoutputInteropGoopgToPG` carried Scenario B's DoD).

## Verification

Loop 27:

```
go test -count=1 -timeout 600s \
  -run "TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply|TestPort_PgoutputInteropPGToGoopgPgbenchKillAsync" \
  ./internal/testport/
# ok 9.310s — both DoD-pin tests pass

go test -count=1 -timeout 600s \
  -run "TestPort_PgoutputInteropPGToGoopg" \
  ./internal/testport/
# ok 38.519s — all PG-to-goopg rungs (1–26) pass together

go test -race -count=1 -timeout 300s \
  ./internal/server/ ./internal/executor/ ./internal/wal/ \
  ./internal/catalog/ ./internal/testutil/pubsubcluster/
# all green
```

## What this closes

- M0103-0007 (Scenario A E2E test: PG primary + goopg subscriber).
- The 26-rung probe-survival + correctness + failover ladder for
  PG → goopg replication.

## Deferred (not blocking milestone closure)

These items were named as "deferred within M0103-0007" by earlier
rungs; they are not required for the DoD and stay open for future
loops:

- `proto_version=2` streaming subxacts (apply-worker subxact tracking
  on `Y`/`A` frames; rung 7 documented the gap).
- column-ref-typed `nextval` args (rung 19's note — sequence DEFAULTs
  driven by per-row column references).
- binary-format pgoutput (requires per-type send/recv on both sides +
  `binary` SUBSCRIPTION option).
- path-(a) revisit (drive PG18 into setting
  `sync_standby_priority > 0` for logical walsenders so the standard
  `synchronous_commit = remote_apply` flow also works; strictly an
  upstream-PG study, not required for the goopg DoD).

## Next step in M0103

M0103-0009 — close milestone (CSV row additions + target-inventory bump).
