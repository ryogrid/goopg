# R2 results — the instrument, and what it was measuring against

*Round 2 of `../TODO.md`. Design: `DESIGN.md` (committed `cec1ce7fc`).
Implemented, gated and measured 2026-09-08.*

## 1. Verdict

The round did what it set out to do — `UNPARSED` is now **0 on both
corpora** — and, in the course of validating the instrument, found that
**the TPC-H parity target was the wrong file** and **the TPC-DS
comparison was running at 128x different `work_mem`**. Both are fixed.
Every parity number reported by R0 and R1 is superseded.

### The honest baseline

| corpus | match | shape-diff | unparsed | missing-node | error |
|---|---|---|---|---|---|
| **TPC-H** (22 sections) | **2** | 20 | **0** | **0** | 0 |
| **TPC-DS** (99 sections) | **0** | 72 | **0** | 24 | 3 |

For comparison, what R0/R1 reported and why it was wrong:

| corpus | R0/R1 reported | corrected | cause of the difference |
|---|---|---|---|
| TPC-H | 1 / 12 / 9 (match/shape/missing) | 2 / 20 / 0 | stale serial PG fixture + tool vocabulary |
| TPC-DS | 0 / 34 / 62 | 0 / 72 / 24 | tool vocabulary + `work_mem` 512MB vs 4MB |

The `MISSING-NODE` column is the headline: on TPC-H it was **9, and the
true value is 0**. Not one of those nine was a node goopg failed to
produce.

## 2. Finding 1 — the TPC-H parity target was a stale fixture

R0 compared goopg against `bench/tpch/plans-pg/`, whose plans are
**serial**: `Aggregate -> Seq Scan on lineitem`. Live PG 18.3 on the
reference cluster (`:65432`, `max_parallel_workers_per_gather = 4`,
`shared_buffers = 2GB`, `work_mem = 64MB` — read from `pg_settings`,
not assumed) plans the same query as

```
Finalize Aggregate -> Gather (Workers Planned: 4)
  -> Partial Aggregate -> Parallel Seq Scan on lineitem
```

The fixture was captured under different settings. Every `parallelism`
category hit, and all nine TPC-H `MISSING-NODE` verdicts, were artefacts
of comparing a parallel plan against a serial reference.

R0 had in fact captured a live PG reference too
(`/tmp/parity-r0/tpch-pg.plans.txt`, correctly parallel) — and then
diffed against the fixture file instead. Two files, one comparison, the
wrong one chosen.

**TPC-H Q6 is now a MATCH with zero divergence categories.** It was
filed as `MISSING-NODE` for two rounds. It had been planning identically
to PG the whole time.

## 3. Finding 2 — the TPC-DS pair was not comparably configured

`work_mem`, read live from both engines:

| | goopg SF0.5 clone | PG reference `:65438` |
|---|---|---|
| `work_mem` | **512MB** | **4MB** |
| `shared_buffers` | 2GB | 2GB |
| `max_parallel_workers_per_gather` | 4 | 4 |

`work_mem` is a first-order planner input — it decides hash-vs-sort,
HashAggregate-vs-GroupAggregate, and every spill estimate. A 128x gap
means the TPC-DS comparison was measuring configuration, not planning.

Both capture scripts now pin `work_mem = 64MB` and
`max_parallel_workers_per_gather = 4` **inside the capture session** on
whichever engine they are pointed at, so no cluster's ambient config can
decide a comparison again. (`capture-tpch.sh`, `capture-tpcds.sh`, in
this directory.)

## 4. Finding 3 — the instrument itself (the round's original subject)

`plan_diff` returned `MISSING-NODE` whenever any node name was
unrecognised, **even when the trees compared identically**. Taught the
comparator the names both engines print — `Partial`/`Finalize` aggregate
phases (prefix kept in the kind, so a real phase divergence stays
visible), `CTE <name>`, PG's spaced `Merge Append`, `SetOp`/`HashSetOp`
strategies — and split the verdict: `GOOPG_UNEMITTABLE` keeps
`MISSING-NODE` (a claim about the plans), an unrecognised name is now
`UNPARSED` (the tool declining to answer).

One class was **not** taught to the tool. goopg's EXPLAIN printed
`WindowAgg (N funcs)`; PG prints bare `WindowAgg`
(`explain.c:1575`). That is a PG-faithfulness defect in goopg and was
fixed in goopg (`operators_explain.go`), because the two engines' plan
text should coincide when the plans do.

### The prediction held

DESIGN §5 predicted, in advance, that most reclassified verdicts would
become SHAPE-DIFF rather than MATCH, and that a large migration to MATCH
would indicate a weakened comparison. Measured on the unchanged R1
captures, isolating the tool change:

| corpus | before | after (tool only) |
|---|---|---|
| TPC-H | 1 / 12 / 9 | 1 / 19 / 0 missing, 0 unparsed |
| TPC-DS | 0 / 34 / 62 | 0 / 64 / 26 missing, 6 unparsed |

Zero queries moved to MATCH from the tool change alone. The 6 residual
TPC-DS `UNPARSED` were all `WindowAgg (N funcs)` and went to 0 with the
renderer fix.

## 5. Hand adjudication (design §4 required 5 per corpus)

Each was adjudicated by reading both plan texts, not by trusting the
verdict.

**TPC-H** — Q6 `MISSING-NODE -> MATCH`: identical tree
(`Finalize Aggregate / Gather 4 / Partial Aggregate / Parallel Seq Scan`),
correct. Q1: both parallel now, but goopg `Finalize HashAggregate` over
`Gather` vs PG `Finalize GroupAggregate` over `Gather Merge` — real.
Q9: **tree matches; the only difference is key rendering** — goopg
prints `Sort Key: nation, o_year` (output aliases), PG prints
`nation.n_name, (EXTRACT(year FROM orders.o_orderdate))` (source
expressions). Q14: goopg `Hash Join` vs PG `Parallel Hash Join`. Q19:
goopg `Hash Join` + 4 workers vs PG `Nested Loop` + 2 workers.

**TPC-DS** — Q12: goopg `HashAggregate` + `WindowAgg`, PG
`Sort` + `GroupAggregate` + `WindowAgg` (with a `Window:` line goopg
does not print). Q44: goopg nested-loop/hash tree, PG `Merge Join` over
sorted `Subquery Scan`s. Q96: goopg `Hash Join` + 4 workers, PG
`Nested Loop` over `Hash Join` + **3** workers. Q38: goopg
`HashSetOp Intersect` twice nested, PG one `SetOp Intersect` over
`Gather Merge`. Q81: goopg `HashAggregate`, PG `Sort` +
`GroupAggregate`.

All five per corpus are genuine divergences. The new verdicts are right.

## 6. What the adjudication exposes — four systematic causes

These are the round's most valuable output and they set up R3+:

1. **goopg's planner never chooses a sorted aggregate.** goopg's own
   renderer says so: `operators_explain.go` — *"The planner does not set
   Strategy yet, so a hand-built node is the only way this renders
   GroupAggregate today."* PG picks `GroupAggregate` constantly on
   TPC-DS (Q12, Q81, Q1 on TPC-H). While `AggStrategySorted` is
   unreachable from the planner, every such query is unmatchable.
2. **Worker count is not computed.** goopg plans 4 workers; PG derives
   the count from relation size (`compute_parallel_worker`, log-scale)
   and chose 3 on Q96, 2 on TPC-H Q19, 3 on Q38. A plan can be
   structurally identical and still differ on this line.
3. **No parallel-aware hash join.** PG emits `Parallel Hash Join` /
   `Parallel Hash`; goopg emits plain `Hash Join` under a Gather.
4. **Sort/Group key rendering uses output aliases**, PG uses source
   expressions. TPC-H Q9 is blocked on this alone — a plan that matches
   structurally and reads as a divergence.

(1) and (2) are the highest-value next rounds: both are single, well
-defined PG behaviours with a named oracle function, and both block many
queries at once.

## 7. Gates

- `scripts/pg-plan-parity-diff-test.py` — 5/5 green (its rollup regex
  updated for the new `unparsed=` field).
- `go test ./internal/executor/` — green.
- TPC-H values: 22/22 byte-identical to R1 (`values-tpch-r2.txt`).
- TPC-DS SF0.5 sweep: `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=0` (`values-tpcds-sf05-r2.txt`).
- Every capture arm printed `VERIFIED :<port> pid=... exe=...`.

## 8. Evidence in this directory

`tpch-goopg.sections.txt`, `tpch-pg-live.sections.txt`, `tpch-diff.txt`,
`tpcds-goopg.sections.txt`, `tpcds-pg-live.sections.txt`,
`tpcds-diff.txt`, `values-*`, `capture-tpch.sh`, `capture-tpcds.sh`.

The two `*-pg-live.sections.txt` files are the corrected references and
supersede `bench/tpch/plans-pg/` for this workstream. That fixture is
left untouched — other gates pin against it and re-cutting it is not
this round's business — but it must not be used as a parity target
again; the TODO records this.

## 9. A note on the error class

R0 chose the wrong reference file, and R1 reported its numbers without
checking what they were measured against. Both are the same class as the
contaminated binary arm in R1 §4 and the `pathgen.go` error the
root-causes document records in its §6: **a plausible number produced by
an unvalidated input**. The specific lesson added to the TODO is that a
comparison's REFERENCE deserves the same provenance check as its
subject — the `launch-verified.sh` discipline applied to data, not just
to binaries.
