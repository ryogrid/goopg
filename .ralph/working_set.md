Task: M0125-0030 (warm-stats programme item 3/4) — DONE and committed this loop
(#10, 2026-07-30). Build scripts verify + CHECKPOINT; standing clusters warmed;
premise flipped; plan baseline captured.

Files:
- `bench/tpch/build_schema_goopg.sh` — post-HammerDB reltuples verification (8/8)
  + CHECKPOINT
- `scripts/tpcds-load.sh` — post-ANALYZE reltuples verification (25 tables)
  before the pre-existing CHECKPOINT
- `scripts/tpcds-sf05-regression.sh load-goopg` — stale "RowCount not restored"
  comment retired; inline reltuples verification per table
- `plan_snapshots/warm-stats-base.txt` — NEW, 22-query plan baseline under WARM
  stats (captured from TPC-H cluster 65433 after one-shot warm-up)
- `docs/design/0125-0028-warm-stats-programme.md` — §-0030a execution record
- `.ralph/fix_plan.md` — M0125-0030 ticked [x], NEXT → M0125-0031

## One-shot warm-up executed

| cluster | port | tables | result |
|---------|------|--------|--------|
| TPC-H SF=1 | 65433 | 8 | all 8 survived restart; EXPLAIN confirms planner consumption |
| TPC-DS SF=0.5 | 65437 | 25 | 24/25 survived (dbgen_version cosmetic); restart verified |
| TPC-DS SF=1 | 65436 | — | data dir absent; deferred to next SF=1 load |

## Premise flip consequences

- Row-count gates: Q12=2, Q13=35 (canonical, NOT moved — spotcheck PASS)
- Plan baseline: `plan_snapshots/warm-stats-base.txt` (22 queries WARM)
- Timed baselines: all S-cold numbers are a different regime; every future
  `analysis/` doc must state "WARM stats" or "S-cold" explicitly

## NEXT (banner order)

**`M0125-0031`** (warm-stats planning line — eliminate timeout class, optimize
TPC-H/TPC-DS runtimes) per the 2026-07-30(b) directive. M0125-0031 is gated on
-0028/-0029/-0030 — all three now landed. The first motion is a re-measurement:
warm TPC-H power sweep at HEAD.

Gates run: tpch-spotcheck PASS (Q12=2/Q13=35, 32.7 s query phase — WARM);
one-shot warm-up E2E verification PASS; go build/vet clean; pgbench smoke via
commit hook.

In-flight: none.
