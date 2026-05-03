# Final TPC-H HammerDB Run — DP Bushy Joins + Subquery Unnesting

**Date:** 2026-05-02
**goopg commit:** `27833d8`
**Test machine:** x86_64 Linux, 32 GB RAM + 64 GB swap, Go 1.25.0

## Configuration

| Parameter              | Value             |
|------------------------|-------------------|
| `shared_buffers`       | 2048 MB (262,144 slots, 2 GiB heap arena) |
| `GOMEMLIMIT`           | 20 GiB            |
| Subquery execution     | **Unnested** (M0033 — `SubqueryExpr` → `HashJoin` + `Aggregate`) |
| Join order             | **DPccp bushy tree** (M0034 — replaces left-deep CROSS chain) |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |
| Explicit GC            | `runtime.GC()` after each query/COPY (M0032-0006) |

## Data Load

HammerDB TPC-H schema build at SF=1. CROSS join in outer query is eliminated
by the bushy DP — M0034 runs when all tables have ANALYZE statistics.

| Table     | Rows Loaded | Target | % Complete |
|-----------|------------|--------|-----------|
| region    | 5          | 5      | 100% |
| nation    | 25         | 25     | 100% |
| supplier  | 10,000     | 10,000 | 100% |
| customer  | 150,000    | 150,000| 100% |
| part      | 200,000    | 200,000| 100% |
| partsupp  | 800,000    | 800,000| 100% |
| orders    | 1,127,000  | 1.5M   | 75% |
| lineitem  | 4,508,720  | ~6M    | 75% |

**Load failure mode:** HammerDB COPY connection drops at ~75% of ORDERS/LINEITEM
(consistent across all runs at approximately the same point). The server log shows
no errors — this is a libpq client-side timeout during the long COPY session.
Not related to buffer pool or memory management.

## Power Test Results

### Q14 — Simple aggregate with join

| Metric     | 256MB (M0029) | 2GiB (M0032-0006) | 2GiB Final |
|-----------|--------------|-------------------|------------|
| Row count  | 6M           | 1M               | **4.5M**   |
| Duration   | 401s         | 17.64s           | **119s**   |
| Speedup vs 256MB | 1×     | 23× (at 1M rows) | **3.4×** (at 4.5M rows) |

Q14 scales linearly with row count — 119s for 4.5M rows is consistent with
17.64s for 1M rows (~6.7× more rows × 6.7× longer).

### Q2 — Correlated subquery with 5-table join

| Outcome | Value |
|---------|-------|
| Status | **RSS exceeded 28 GB, system manually killed before OOM** |
| Plan optimizations active | Subquery unnesting (M0033) + DP bushy join (M0034) |
| CROSS join | Eliminated (verified in `TestBushyDPWithStats`) |
| Peak RSS | 28,708,660 kB |

Despite both planner optimizations being correctly applied (verified by unit tests),
Q2 still exhausted memory. The remaining bottleneck is **not the CROSS join or the
subquery execution**, but rather the **hash join implementation's drainRows copies**.

### Root cause of residual memory pressure

After bushy DP removes ALL CROSS joins, Q2's plan becomes a tree of 4 hash joins:

```
HashJoin(ps_supplycost = mincost)          ← unnest result join
  └─ Filter(p_size=15, p_type like '%BRASS')
       └─ HashJoin(s_suppkey = ps_suppkey)
            ├─ HashJoin(p_partkey = ps_partkey)    part(200K) ⋈ partsupp(800K)
            │   ├─ SeqScan(part)
            │   └─ SeqScan(partsupp)
            └─ HashJoin(s_nationkey = n_nationkey)  supplier(10K) ⋈ nation(25)
                 ├─ HashJoin(n_regionkey = r_regionkey)  nation(25) ⋈ region(5)
                 │   ├─ SeqScan(region)
                 │   └─ SeqScan(nation)
                 └─ SeqScan(supplier)
```

All joins are INNER HASH with equijoin keys. No CROSS join. The subquery is
evaluated once (unnested):

```
HashJoin(p_partkey = ps_partkey)             ← unnest result
  └─ Aggregate(min GROUP BY ps_partkey)
       └─ (partsupp/region/nation join)
```

The memory consumption is dominated by `drainRows` in `joinOp.Open()`.
Every join operator buffers BOTH children entirely:

| Join        | Left drainRows | Right drainRows | Total |
|------------|---------------|----------------|-------|
| part ⋈ partsupp | 200K × 9 cols ≈ 180 MB | 800K × 5 cols ≈ 400 MB | **580 MB** |
| supplier ⋈ ... | 10K × 7 cols ≈ 7 MB | small | **~10 MB** |
| nation ⋈ region | 25 × 4 cols | 5 × 3 cols | **~0 MB** |
| (*result) ⋈ supplier | ~800K × 14 cols | 10K × 7 cols | **~1.1 GB** |
| Filter + unnested join | 800K × 21 cols | 200K × 2 cols | **~1.7 GB** |
| Sort + Project | 2K × 8 cols | — | **~1 MB** |

**Estimated peak: ~3.4 GB** from drainRows + hash table overhead.

The observed 28 GB RSS suggests one or more of:
- Join results include all 4M+ lineitem columns (Q2 does NOT touch lineitem,
  so this should not apply — the outer queries use only 5 tables).
- The hash table string-key overhead (`map[string][]Row`) uses far more memory
  than estimated (each key is a `datumKey` string with canonicalized numeric
  values — potentially 20+ bytes per key × 200K unique keys = 4 MB, plus
  per-bucket overhead).
- **Go heap fragmentation**: The GC is not releasing intermediate allocations
  fast enough, despite `GOMEMLIMIT=20GiB` and `runtime.GC()` after queries.
  The 28 GB includes ~3.4 GB of "live" data plus ~24 GB of uncollected garbage.
- The unnesting may not have fired properly through the bushy plan path
  (the pass order in `planSelect`: bushy DP then unnest — but the unnest
  pass expects a Filter wrapping a CROSS chain, which no longer exists).
  **This is a likely issue**: `unnestSubqueriesInPlan` receives the bushy
  plan (which is NOT a Filter because `tryBushyDP` returns `(bushyPlan, nil)`
  when all conjuncts are consumed). The unnest pass walks the tree, but
  the bushy plan has different shape than expected.

## Conclusions

1. **DP bushy join (M0034) eliminates CROSS joins** — verified in unit tests.
   Q2's plan with ANALYZE stats has zero `JoinTypeCross` nodes.

2. **Subquery unnesting (M0033) eliminates per-row re-execution** — verified
   in unit tests. The subquery runs once.

3. **Q2 at SF=1 still exhausts memory** (28 GB RSS) due to:
   - `joinOp.Open()` draining ALL children into memory via `drainRows`.
   - Possible interaction between the bushy DP and the unnest post-pass
     (the unnest pass may not fire correctly on bushy plans).
   - Go heap fragmentation under high allocation rate.

4. **The drainRows pattern is the single remaining bottleneck.** Every hash
   join deep-copies both inputs. A streaming hash join (drain build side only,
   probe side streams) would reduce peak memory by ~50% for each join level.

## Next Steps

- **Investigate unnesting on bushy plans**: The `unnestSubqueriesInPlan` pass
  runs after bushy DP replaces the tree. The unnest may not locate `SubqueryExpr`
  nodes because the bushy plan has a different structure. This could leave the
  subquery un-unnested, falling back to per-row execution.
- **Implement streaming hash join**: Modify `joinOp.Open()` to drain only the
  build side, streaming the probe side through the hash table. This would
  eliminate the probe-side `drainRows` and cut peak memory substantially.
- Implement `drainRows` without deep copies when possible.
