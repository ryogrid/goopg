# 04 — Improvement designs (index)

Derived from `02-bottleneck-analysis.md` (run 20260712_114859). Each fix doc
is self-contained: problem evidence → PostgreSQL's approach → goopg design at
file/function level → expected lift → risks → verification plan.

## Priority order and expected lift stack

| # | fix | target metric | expected lift (c=50 simple-update unless noted) | cost | depends on |
|---|---|---|---|---|---|
| P0 | [fix-01 WAL stripe backend-ID](fix-01-wal-stripe-backend-id.md) | 57 % of OLTP CPU / ~60 % of COPY CPU | ×1.5–2.3 TPS + a large COPY win | S–M | — |
| P0 | [fix-04 COPY multi-insert](fix-04-copy-multi-insert.md) | `pgbench -i` 56× COPY gap | ×10–30 on bulk load | L | fix-01 helps but independent |
| P1 | [fix-02 single commit record](fix-02-single-commit-record.md) | 2 WAL appends/commit → 1 | ×1.1–1.3 | M (recovery-sensitive) | — |
| P1 | [fix-03 commit-pipeline streamline](fix-03-commit-pipeline-streamline.md) | per-flush log, inert pre-flush, channel hops | ×1.1–1.2 + latency tail | S–M | best after fix-01 re-profile |
| P2 | [fix-05 startup single-pass recovery](fix-05-startup-single-pass-recovery.md) | 28 s startup, 200 GB startup allocs | startup → ~2–3 s | M | — |
| P2 | [fix-06 GC / per-statement arena](fix-06-gc-alloc-arena.md) | A2 shows +43 % headroom | ×1.2–1.4 (post-fix-01 re-measure first) | L | re-profile after fix-01 |
| P3 | [fix-07 snapshot reuse](fix-07-snapshot-reuse.md) | O(slots)+sort per RC statement | small today (1.5 % CPU); grows with backends | M | — |

Sequencing rationale: fix-01 first — it is small, dominates every profile
(OLTP *and* COPY), and every later measurement is distorted until it lands.
Re-profile after fix-01; the GC and pipeline shares will change. fix-04 is
independent and unlocks the user-visible 9-minute `pgbench -i`.

## Benchmarking-practice note (no code change)

Headline runs inherited `GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1`
from the 2026-05 suite for comparability; aux A5 measured this at **−28 % TPS**.
Future suites should record the headline with profiling rates 0 and collect
contention profiles in a separate tagged run (keep one rate=1 run for
cross-era comparison until the old baseline is retired). Also fix the
misleading `GOMEMLIMIT applied bytes=<maxint>` log in `cmd/goopg/main.go:323`
(logs the temporary swap value; the env limit is actually in effect).

## Correctness gates common to all fixes

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` + the
  pre-commit pgbench smoke hook (never `--no-verify`).
- WAL-format-affecting fixes (02, 04): full regress-port suite re-run, crash
  recovery tests, `pg_waldump` readability check, and the
  PG-standby-attach E2E where applicable.
- Executor-path fixes (04, 06): `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35)
  — row counts are the gate.
- Concurrency-touching fixes (01, 02, 03): `make race-gate`.
- Perf acceptance: re-run `analysis/perf-optimize2/scripts/run_su50.sh` and
  compare against run 20260712_114859 (same conditions; note profiling-rate
  choice explicitly).
