# Q9 in PARALLEL mode — the plan baseline the tree did not have

Captured 2026-09-07 because every plan artifact in this repository is a
**serial** capture (`estimate-audit --serial` defaults true and sets
`max_parallel_workers_per_gather = 0`; the PG reference goes through the same
function, so `bench/tpch/plans-pg/` contains **zero `Gather` nodes**), while the
timing tables in `bench/tpch/timings/` are measured with parallelism ON. A
parallel runtime cannot be explained with a serial plan, so this pair was taken.

Conditions: both engines `max_parallel_workers_per_gather = 4` (read back from
the live servers), TPC-H SF=1, db `tpch`. goopg = branch tip binary
(`/tmp/acc/goopg-tip`, contains B-01c's `GOOPG_NARROW_UPPER`), PG 18.3 on :65432.

## PG 18.3

```
Gather (Workers Planned: 4)
  -> Partial HashAggregate
      -> Parallel Hash Join          (cost=44343.58..77747.55 rows=75748)
           Hash Cond: orders.o_orderkey = lineitem.l_orderkey
           -> Parallel Seq Scan on orders
           -> Parallel Hash          (cost=43121.83..43121.83 rows=97740)
                -> Nested Loop
                     -> Hash Join (partsupp x part x supplier x nation)
```

## goopg (branch tip)

```
Gather (Workers Planned: 4)
  -> Partial HashAggregate
      -> Hash Join                   (cost=514811.50..829354.96 rows=303093)
           -> Hash Join
                -> Parallel Seq Scan on orders
                -> Hash Join
                     -> Seq Scan on partsupp     <- NOT parallel, 800k rows
```

Node counts, measured: PG has `Parallel Seq Scan` x3 and `Parallel Hash` x4.
goopg has `Parallel Seq Scan` x1 and `Parallel Hash` **x0**.
Top-level cost 836,644 (goopg) against 113,104 (PG) — 7.4x.

## What the gap actually is

**Not N-fold duplicate building.** goopg's executor builds the hash table ONCE
in the leader and shares it with the workers by pointer, and since E-09a/E-09b
that holds for spilling builds too (Build Time went from five to one). The
C-19f row states it directly: *goopg has NEITHER of PG's two parallel hash
joins*.

**It is that the build cannot be SPLIT.** In goopg's Q9 the only parallel node
is the `orders` scan; the inner chain — 800,000 `partsupp` rows folded against
part/supplier/nation — runs single-threaded while the four workers wait. PG
splits exactly that work across the four workers with `Parallel Hash` over a
`Parallel Seq Scan on partsupp`.

So goopg is already close to PG's `parallel_hash = true` in the respect that
matters for memory (one table, not N). What is missing is dividing the *work*
of building it.

**Correction this artifact carries:** the account in the perf report's §5.34,
that "each worker rebuilds the whole inner", is **wrong** — it came from a
sub-agent's summary and was repeated without checking. The corrected statement
is the one above.
