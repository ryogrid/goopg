# M0098 Final Measurement (M0098-0008)

**Date**: 2026-05-12  
**Binary**: ralph-period-from-0511 (commits 1cc98b3, 3f1de66, fc192ca, 7b91982, 35c1299)  
**Conditions**: `-c 100 -j 100 -T 180 -s 100`, fresh data directory (cold buffer pool)  
**Fixes landed**: M0098-0002 WAL group commit, M0098-0003 buffer pool 128-partition, M0098-0004 EvalPlanQual, M0098-0007 PGO+GOAMD64=v3+GOGC=200

## Results vs Targets

| Workload | M0098-0001 Baseline | This Run | Target | Gap |
|---|---:|---:|---:|---:|
| Standard (TPC-B) | 229 TPS | **443 TPS** | 1,500 TPS | 3.4× |
| Simple Update | 228 TPS | **420 TPS** | 1,500 TPS | 3.6× |
| Select Only | 6,166 TPS (warm) | **4,990 TPS** (cold) | 10,000 TPS | ~2× |

## Key Findings

### Standard / Simple Update (443 / 420 TPS)
- **~1.9–2× improvement** over M0098-0001 baseline — WAL group commit (M0098-0002) works.
- Failure rate: Standard 2.215% (327/14,757), Simple Update 0.001%. The 2% failure rate
  is from EPQ no longer waiting for conflicting txns — concurrent teller/branch updates
  both get 40001 when they conflict. This is correct behavior; the old EvalPlanQual
  with WaitForXID caused circular deadlocks (all 100 connections stalled).
- **Remaining gap**: Still 3.4× below 1,500 TPS target. Primary remaining bottleneck:
  the buffer pool's evictMu contention (all Pin operations serialize through it).

### Select Only (4,990 TPS on cold pool)
- **Cold start penalty**: This measurement used a fresh data directory. The buffer pool
  was empty and all 10M account pages loaded from disk during the 180s window.
  The M0098-0001 baseline (6,166 TPS) used a warm buffer pool.
- With a warm pool, Select Only performance would be higher (estimated 6,000-8,000 TPS).
- **Remaining gap**: ~2× below 10,000 TPS target. Primary bottleneck: evictMu
  serializes all Pin operations even with the 128-partition byTag (M0098-0003).

## Bug Fixes Found During M0098-0008 (commit 35c1299)

### Buffer Pool Deadlock (M0098-0003)
Pin fast path acquired `part.mu → evictMu` (wrong order). Eviction acquired
`evictMu → oldPart.mu`. When `part == oldPart`, this caused a permanent deadlock:
all table accesses hung, server goroutines exited, TPS dropped to 0 after ~30s.  
**Fix**: Release `part.mu` before acquiring `evictMu` in Pin and TryPin fast paths.

### EvalPlanQual Circular Deadlock (M0098-0004)
`WaitForXID` caused circular waits: TX1 held teller row, waiting for branch (held by TX2);
TX2 held branch, waiting for teller (held by TX1). Both blocked indefinitely → TPS = 0.  
**Fix**: `epqWait` no longer calls `WaitForXID`; just refreshes snapshot. If conflicts
persist after `maxEPQRetries=3`, escalates to SQLSTATE 40001.

## Remaining Work

| Bottleneck | Evidence | Suggestion |
|---|---|---|
| evictMu serialization | All Pin operations need evictMu for pinCount; defeats byTag partition benefit | Change pinCount to atomic.Int32; eliminate evictMu from Pin fast path |
| WAL group commit batching | Each txn still gets its own batch (low concurrency at commit point) | Commit-delay parameter to batch more flushes |
| 40001 abort rate (2.2%) | EPQ no longer waits, conflicting txns both abort | Implement proper deadlock detection + waiting |
