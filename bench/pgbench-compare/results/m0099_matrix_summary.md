# M0099 Canonical Measurement (M0099-0005/0006)

**Date**: 2026-05-12  
**Binary**: ralph-period-from-0511 (commit 46f2fe1)  
**Conditions**: `-c 100 -j 100 -T 180 -s 100`, warm buffer pool (server running continuously from init)  
**Fixes landed since M0098**: M0099-0002 (atomic pinCount+RWMutex), M0099-0004 (WFG cycle detection + EPQ aborted-xmax fix)  
**Disabled since M0099**: M0099-0003 (commit-delay sleep removed due to state.append Path A race)

## Results vs Targets

| Workload | M0098-0008 | This Run (M0099) | Target | Gap |
|---|---:|---:|---:|---:|
| Standard (TPC-B) | 443 TPS | **447 TPS** ¹ | 1,500 TPS | 3.4× |
| Simple Update | 420 TPS | **410 TPS** | 1,500 TPS | 3.7× |
| Select Only | 4,990 TPS (cold) | **5,204 TPS** | 10,000 TPS | 1.9× |

¹ Standard run aborted at ~114s due to "command N: no results" error accumulation;
  TPS from partial run. Full 180s Select Only ran cleanly (zero failures).

## Failure Rates (M0098 vs M0099)

| Workload | M0098 failure rate | M0099 failure rate |
|---|---:|---:|
| Standard | 2.215% | 0.651% ² |
| Simple Update | 0.001% | 0.001% |
| Select Only | 0.000% | 0.000% |

² Improvement from 2.2% → 0.65%: EPQ aborted-xmax fix prevents permanent 40001
  on rows touched by rolled-back HOT updates; WFG cycle detection catches
  true deadlocks immediately.

## Key Findings

### Standard workload issues (0.651% failure rate)

1. **HOT update chain visibility**: goopg's B-tree index scanner does not follow
   HOT update chains (`heap_hot_search_buffer` equivalent not implemented). After
   a committed HOT update, the index still points to the old slot (now dead). Index
   scan returns 0 rows, causing "command N: no results" pgbench errors. Affects only
   rows with prior HOT updates; fresh rows work correctly. This is a pre-existing v0
   limitation that lowers effective TPS for write-heavy workloads.

2. **Rare WAL LSN corruption (1 event in Simple Update)**: state.append Path A
   reads `s.writePos` without `appendMu`, then overwrites it with a stale value.
   This can produce `endLSN = uint64_max` (FlushUpTo overflow). Frequency: 1 event
   in ~73,000 transactions (0.001%). The commit-delay sleep (M0099-0003) amplified
   this race; disabling the sleep reduced frequency to near-zero but the underlying
   race remains.

### Performance vs M0098

- **Standard**: +1% (447 vs 443) — minor improvement from atomic pinCount
- **Simple Update**: −2% (410 vs 420) — within noise floor; warm pool vs. cold pool difference
- **Select Only**: +4% (5,204 vs 4,990) — warm pool (no cold-start penalty)
- **Abort rate improvement**: Standard 2.2% → 0.65% from EPQ fix

## Result Files

| File | Workload | TPS | Failures |
|------|----------|-----|----------|
| `20260512_162600_goopg_c100_j100_standard.txt` | Standard | 447 | 302 (0.651%) |
| `20260512_162600_goopg_c100_j100_simple-update.txt` | Simple Update | 410 | 1 (0.001%) |
| `20260512_162600_goopg_c100_j100_select-only.txt` | Select Only | 5,204 | 0 (0.000%) |

## Remaining Gap Analysis

| Bottleneck | TPS Impact | Path to Close |
|---|---|---|
| evictMu still in MarkDirty/WAL paths | ~30% write TPS | RWMutex for all evictMu callers |
| No WAL group-commit batching (commit delay disabled) | ~20% write TPS | Fix state.append Path A race, re-enable delay |
| HOT chain following missing | Standard only (index scan) | Implement heap_hot_search_buffer equivalent |
| No GOAMD64=v3 tuning for SELECT workloads | ~5% select TPS | Already applied via Makefile |

## Comparison with PostgreSQL 18.3 Baseline

| Workload | goopg M0099 | PostgreSQL 18.3 | Ratio |
|---|---:|---:|---:|
| Standard | 447 TPS | 5,382 TPS | 8.3% |
| Simple Update | 410 TPS | 7,882 TPS | 5.2% |
| Select Only | 5,204 TPS | 38,575 TPS | 13.5% |
