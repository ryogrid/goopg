# 2026-08-04 estimate audit — P5.6-e-iii

Instrument: `cmd/estimate-audit --label 2026-08-04-p56eiii --timeout 150s`
(`--serial` and `--warm-stats` on by default; both load-bearing — nodes under a
Gather report no actual rows, and goopg's ANALYZE statistics are
per-connection).

Cluster: `GOGC=100 GOMEMLIMIT=12GiB bench/tpch/setup_goopg.sh`, TPC-H SF=1 on
127.0.0.1:65433, database `tpch`. LEGACY planner (`GOOPG_PGSHAPED_DP` OFF), so
this is a pure cardinality/cost comparison against
`2026-08-04-p56eii.txt` — the same cluster, same load, same instrument.

Files:

- `2026-08-04-p56eiii.txt` — the report (per-joinrel est vs actual + summary).
- `2026-08-04-p56eiii.plans.txt` — the raw `EXPLAIN ANALYZE` text.

Result: **5 violations → 2.** Full before/after table, the two regressions and
the Q9 attribution are in
`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §5.4.

`actual rows=` in the plan text is CUMULATIVE across loops, which is why the
report divides by `loops` before computing a ratio.

## Q9 attribution (09 §6, taken BEFORE the change landed)

Q9 exceeded the 150 s timeout where §5.3 measured it at 93.9 s. To class it,
the PLANNER half was reverted (`git checkout internal/planner/cardinality.go`),
the cluster restarted, and Q9 re-EXPLAINed with only the ANALYZE half in place:

```
->  Hash Join (INNER, build=left)  rows=481222948   # ANALYZE half only
->  Hash Join (INNER, build=left)  rows=479779280   # both halves
      Hash Cond: ((l_suppkey = ps_suppkey) AND (l_partkey = ps_partkey))
```

Identical shape, 0.3 % apart — the de-saturated ndistinct is the whole cause,
and the mechanism is the two-pair equi-join priced on one pair (P5.6-f).
