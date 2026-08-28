# VACUUM / Autovacuum Parity Bundle (not_ralph)

Task: verify goopg's VACUUM / autovacuum / ANALYZE / freeze behavior against
vanilla PostgreSQL 18.3 (`./postgres/`, read-only oracle), document every
divergence, and fix them.

Branch: `waitevent-impl`. Base audit performed 2026-08-26.

## Verdict summary

| area | verdict |
|---|---|
| Heap scan VM skip | **DIVERGE — goopg scans every block** (the reported concern; confirmed) |
| Aggressive / anti-wraparound mode | DIVERGE (absent as a scan mode) |
| FREEZE option | DIVERGE (parsed, never executed) |
| Freeze cutoff math | PARTIAL (no `min(max_age/2)` cap, no OldestXmin clamp, hardcoded launcher value) |
| relfrozenxid skip-guard | MATCH-contingent (must add guard when skipping lands) |
| Multixact freeze bookkeeping | DIVERGE (no relminmxid/cutoff) |
| Autovacuum vacuum trigger | DIVERGE (every non-empty table each ≥5 min tick; no threshold formula) |
| Autovacuum analyze trigger | DIVERGE (unconditional per tick) |
| Anti-wraparound priority / enabled=off override | PARTIAL (override yes; ordering no) |
| Launcher wiring | **DIVERGE — launcher is never started in production** |
| Naptime/worker model | DIVERGE (hardcoded 60s/5m/5m/1, not GUC-fed) |
| Cost-based throttling | DIVERGE (GUC registered but inert) |
| ANALYZE sampling & column stats (statement) | MATCH (executor path reservoir-samples and builds MCV/histogram) |
| autoanalyze quality | DIVERGE (launcher calls simplified full-scan Rows/AvgWidth helper) |
| relallvisible publish | PARTIAL (VM knows it; not published) |
| VACUUM FULL / CLUSTER rewrite | DIVERGE (lock-only stubs; documented deferral) |
| Truncation (vacuum_truncate) | DIVERGE (inert GUC) |
| VM set/clear lifecycle on DML | MATCH |
| datfrozenxid bookkeeping | MATCH |

## Documents

| file | content |
|---|---|
| [01-upstream-behavior.md](01-upstream-behavior.md) | PG 18.3 mechanics with citations |
| [02-goopg-current-state.md](02-goopg-current-state.md) | Full parity matrix + missing-GUC checklist + wiring gap |
| [03-design.md](03-design.md) | Detailed designs for the fixes |
| [04-execution-plan.md](04-execution-plan.md) | Implementation phases, gates, risks |
| [TODO.md](TODO.md) | Work checklist |

## Scope decisions

In scope this task: VM-based page skipping (+skip-guard), aggressive /
anti-wraparound semantics, FREEZE option execution, freeze cutoff math via
GUCs/reloptions, real autovacuum start + trigger formula with new
dead-tuple/mod-count accounting, autoanalyze upgraded to the sampled
analyzer, cost-based throttling, missing GUC registrations + sample-file
sync, relallvisible publish.

Follow-up round also implemented: tail truncation (WAL-first,
RecordKindSmgrTruncateTo), relallvisible publish, failsafe escalation,
partitioned-parent row/page rollup.

Still deferred deliberately: VACUUM FULL / CLUSTER physical rewrites (require
transactional relfilenode-swap WAL machinery — a dedicated milestone; doing
them without it would be crash-unsafe), multixact freeze bookkeeping,
eager scanning, parallel vacuum workers, parent-level column-stat merges.

## Implementation status

All in-scope fixes implemented and verified on branch `waitevent-impl`
(single commit). Live evidence (throwaway :5533 cluster, conf-driven
naptime=3s and lowered thresholds): autovacuum fired from dead-tuple math
within one naptime; the following pass logged `skipped_frozen=51 pages=1`
(two-bit VM skipping working as designed); autoanalyze produced sampled
column stats. Full write-up in TODO.md Phase F.
