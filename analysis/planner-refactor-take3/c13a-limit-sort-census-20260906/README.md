# C-13a Probe P2 — the goopg `Limit → Sort(N)` census

**Verdict: NO-GO. C-13a is not implemented.** The structural hypothesis the
probe was written to test is **confirmed** — goopg does stack a bindable
`Limit → Sort` in far more plans than PostgreSQL does — and it turns out not
to matter, because the sorts it stacks are small. The total time a perfect
bounded sort could remove from the whole 99-query corpus is **at most 120 ms
out of 802 s (0.015 %)**, and **not one sort in the corpus spills**, so the
mechanism the item's value rested on (bound → no spill) has no witness at all.

Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md` C-13. Probe: **P2**,
`docs/design/planner-p4-upper-rels/DESIGN.md` §9 — "This list is C-13a's
timing gate and its go/no-go." §6.6 of that document states this outcome in
advance as a real possibility and prescribes the disposition; §10 records that
C-13a was ranked first *on the strength of this unmeasured number*.

```
label:         c13a-p2-limit-sort-census
date:          2026-09-06T10:46:28+09:00
goopg:         00688e96c (binary built from a clean detached worktree at that
               commit — the working tree carried three other agents' WIP)
binary:        tmp/goopg-c13a-base sha256/16 = 76740bed9b6075c7
suite:         tpcds-sf05 (99 queries), goopg :65437, data-sf05
regime:        stats=S-cold parallel=on GOGC=off GOMEMLIMIT=12GiB
               (bench/tpcds/env_tpcds.sh defaults)
cgroup:        GOOPG_CG_UNIT=goopg-c13a-census via scripts/goopg-test-run.sh
timeout:       300s per query — 99/99 returned rc=0, no timeout, no error
host-load:     3.5-5.5 (1-min) — a TPC-H acceptance arm owned :65433 throughout
```

The host-load caveat is stated plainly because it is real: absolute
per-query wall times in this capture are inflated and noisy. It does not
touch the go/no-go, which rests on **row counts** and on the **ratio** of
sort time to query time, both of which survive a loaded host. Nothing here
is offered as a benchmark result.

---

## 1. What was measured, and why it had to be `EXPLAIN ANALYZE`

`EXPLAIN`-only would have produced the opposite answer. goopg's estimated
row counts above the aggregate seam are wrong by up to **789×**, in both
directions, on exactly the nodes this probe is about:

| query | goopg `Sort` est. rows | goopg `Sort` **actual** input | ratio |
|---|---:|---:|---|
| Q22 | 9 460 201 | 11 987 | 789× **over** |
| Q99 | 720 657 | 90 | 8007× over |
| Q62 | 359 432 | 150 | 2396× over |
| Q93 | 41 131 | 0 | ∞ over |
| Q78 | 1 | 245 587 | 245 587× **under** |
| Q47 | 1 | 43 626 | 43 626× under |
| Q51 | 2 246 | 324 249 | 144× under |

An estimate-based census of this corpus reports 8 sorts over 10 000 rows,
headed by a 9.46 M-row Q22. The measured census reports 10, headed by a
324 K-row Q51 — a different set, a different ranking, and a top entry 29×
smaller. The estimates are not a usable proxy here.

So the probe ran `EXPLAIN (ANALYZE, VERBOSE OFF)` over all 99 queries and
read, for every `Sort` node:

- its **input** row count, off the Sort's **child** — never off the Sort
  itself. A `Sort` under a `Limit` stops emitting at the limit, so its own
  `actual rows` is 100 in almost every plan and says nothing about the work
  it did. Reading the wrong one is how a census of this shape reports 100
  rows everywhere and concludes there is nothing to optimise;
- its **cost in milliseconds**, as `Sort.actual_start − child.actual_end`:
  the child is fully drained before the first sorted row can be emitted, so
  that difference is the sort itself. This is an **upper bound**: goopg's
  hash-join and nested-loop children under-report their own end time (Q94's
  anti-join ends at 2150 ms while its own child ends at 2520 ms), which can
  only inflate the subtraction;
- its `Sort Method` line, i.e. whether it spilled.

## 2. The census

| population | goopg | PostgreSQL 18.3 |
|---|---:|---:|
| queries | 99 | 99 |
| plan roots captured | 100 | 99 |
| `Limit`-rooted plan roots | 82 (in 80 queries) | 83 |
| **`Sort` that is a `Limit`'s DIRECT child** (what C-13a stamps) | **77** | **54** |
| `Sort` a `Limit` reaches only through PG's descent whitelist | 1 | 1 |
| `Sort` no `Limit` can bound | 22 | 149 |
| direct-child sorts with input ≥ 10 000 rows | 10 | 2 (by estimate) |
| direct-child sorts with input ≥ 100 000 rows | 3 | 0 (by estimate) |
| **sorts that spilled** | **0** | — |

Both columns are counted by the same tool (`census.py`) with the same
definition of "direct child", so the two are comparable; the PG column is
`bench/tpcds/plans-pg/*.txt` and is necessarily **estimated** rows, since
those captures carry no `ANALYZE`. The design's §6.5 figures (81 / 39 / 2)
were counted by hand at plan-root level only; the difference is that this
tool also counts `Limit → Sort` pairs inside CTE bodies and sub-plans.

**The hypothesis is confirmed: 77 vs 54.** goopg does stack a bindable full
`Sort` where PG does not, and for the predicted reason —
`planner.go:1720` wraps one unconditionally, and goopg has no index-ordered,
incremental-sort or GroupAggregate path above the seam to avoid it. The
structural half of the design's argument (§6.5, §10) is correct.

**And it buys nothing**, because of a fact the design did not anticipate:

| | direct-child sorts |
|---|---:|
| whose input is an aggregate or window (`HashAggregate`, grouping-sets `HashAggregate`, `WindowAgg`, `Aggregate`, `Unique`, `HashSetOp`) | **54 of 77** |
| whose input is a join or scan | 23 of 77 |

TPC-DS's `order by … limit 100` is almost always applied to a **grouped**
result, so the row count has already collapsed before the Sort sees it. The
median direct-child sort input in this corpus is **145 rows**.

### Distribution — every direct-child sort with input ≥ 1 000 rows

Full table for all 100 sorts: `census.tsv`. Plan excerpts: `sort-nodes.txt`.

| query | actual input rows | width | **sort ms** | query ms | sort as % of query | memory | sort's input node |
|---|---:|---:|---:|---:|---:|---:|---|
| Q51 | 324 249 | 136 | 1.9 | 12 710 | 0.01 % | 7 362 kB | WindowAgg (2 funcs) |
| Q78 | 245 587 | 240 | 0.0 | 17 912 | 0.00 % | 38 kB | Hash Left Join |
| Q67 | 115 150 | 212 | 1.0 | 11 532 | 0.01 % | 559 kB | WindowAgg (1 funcs) |
| Q47 | 43 626 | 196 | **35.7** | 12 094 | 0.30 % | 12 553 kB | CTE Scan on v2 |
| Q1 | 25 329 | 1 076 | 9.5 | 2 879 | 0.33 % | 26 210 kB | Hash Join |
| Q41 | 18 000 | 480 | 0.0 | 8 824 | 0.00 % | 2 kB | Seq Scan on item |
| Q57 | 17 189 | 228 | 12.5 | 6 775 | 0.18 % | 6 146 kB | CTE Scan on v2 |
| Q14 | 17 170 | 84 | 7.9 | 52 386 | 0.02 % | 4 916 kB | HashAggregate (5 grouping sets) |
| Q59 | 15 288 | 584 | 4.1 | 10 550 | 0.04 % | 14 930 kB | Hash Join |
| Q22 | 11 987 | 160 | 12.0 | 10 788 | 0.11 % | 3 223 kB | HashAggregate (5 grouping sets) |
| Q79 | 7 575 | 428 | 8.4 | 80 383 | 0.01 % | 8 757 kB | Nested Loop |
| Q27 | 6 175 | 196 | 0.9 | 9 642 | 0.01 % | 2 137 kB | HashAggregate (3 grouping sets) |
| Q46 | 4 958 | 816 | 4.7 | 35 334 | 0.01 % | 9 202 kB | Nested Loop |
| Q18 | 4 130 | 352 | 2.5 | 31 715 | 0.01 % | 2 314 kB | HashAggregate (5 grouping sets) |
| Q7 | 2 847 | 160 | 0.2 | 10 518 | 0.00 % | 716 kB | HashAggregate |
| Q26 | 1 822 | 160 | 0.1 | 6 625 | 0.00 % | 458 kB | HashAggregate |
| Q21 | 1 765 | 72 | 0.1 | 2 172 | 0.00 % | 209 kB | HashAggregate |
| Q89 | 1 730 | 228 | 1.6 | 1 638 | 0.10 % | 662 kB | WindowAgg (1 funcs) |
| Q53 | 1 686 | 68 | 1.0 | 1 005 | 0.10 % | 214 kB | WindowAgg (1 funcs) |
| Q20 | 1 506 | 224 | 0.7 | 2 374 | 0.03 % | 686 kB | WindowAgg (1 funcs) |
| Q60 | 1 450 | 64 | 0.1 | 14 780 | 0.00 % | 159 kB | HashAggregate |
| Q65 | 1 422 | 1 232 | 1.5 | 14 869 | 0.01 % | 4 037 kB | Hash Join |
| Q35 | 1 286 | 320 | 0.5 | 11 708 | 0.00 % | 975 kB | HashAggregate |
| Q68 | 1 045 | 848 | 0.7 | 14 322 | 0.00 % | 1 989 kB | Nested Loop |

The remaining 53 direct-child sorts have inputs from 932 rows down to 0.

### The three numbers that decide the item

| | |
|---|---:|
| **Total sort time across all 77 bindable sorts** (upper bound) | **119.8 ms** |
| Total sort time across all 100 sorts in the corpus | ≤ 1 542 ms, of which ~1 300 ms are the Q94/Q73 instrumentation artifacts above |
| Corpus wall time (sum of per-query root `actual time`, 88 queries) | **801 745 ms** |
| Bindable sort time as a share of the corpus | **0.015 %** |
| Best single query: Q47 | 35.7 ms of 12 094 ms = **0.30 %** |
| **Sorts that spilled** | **0 / 100** |
| Largest in-memory sort footprint | 26 210 kB (Q1) — **10 % of** `sortChunkBytes` = 256 MiB |

## 3. Go / no-go

**NO-GO for C-13a.** The decision rule in the task and in the design is
whether the census shows "a meaningful population of `Limit → Sort` over
large inputs". The population is large (77) and the inputs are not.

C-13a would still be a *correct* change, and a cheap one; it is not being
rejected as wrong or risky. It is being rejected because the corpus that is
supposed to demonstrate it contains **120 ms** of addressable work, which is
below the ±17 % noise band on any single query in it (take3 09 §6) and three
orders of magnitude below anything the acceptance bars can resolve. Landing
it would mean adding a k-heap, a plan-tree stamping pass, a `WITH TIES`
exclusion, a `liftLimitAboveLockRows` interaction, a ctid-side-channel
interaction and a per-worker-bound interaction under `GatherMerge`, and then
reporting "no measurable change" as the result — while claiming the gate that
would have caught a mistake (the SF0.5 checksum arm) had actually exercised
it. It had not: the SF0.5 harness deliberately reports `ck=n/a` for any query
whose result **saturated its LIMIT window** (`scripts/tpcds-sf05-regression.sh`
oracle header: "the row SET is ambiguous at its edge"), which is precisely
every query C-13a touches. The one gate that looks like it covers this item
is blind to it by construction.

Two supporting facts, each sufficient on its own:

1. **The spill-removal mechanism has no witness.** The design's strongest
   argument (§10) is that a bound "does not merely change `N log N` to
   `N log k` — it can remove the spill entirely", the disk-branch-to-bounded-
   branch transition at `costsize.c:1930/1960`. Zero of 100 sorts in this
   corpus spill, and the largest is at 10 % of the spill threshold with a 26×
   margin. There is nothing to remove.
2. **`N log N → N log k` is worth ~120 ms here.** goopg's sort already
   precomputes its key values (`sortOp.keyvals`, M0134-0191), which is what
   made the sort cheap: 324 249 rows sort in 1.9 ms. The parallel-sort design
   recorded the same effect from the other side — its stage 3 was refuted
   *because* stage 1 had already removed the sort's dominance
   (`docs/design/not_ralph/parallel-sort/DESIGN.md` §9.1). C-13a is the third
   item to be measured against a sort that the keyvals work already made
   cheap.

### Disposition

Per the design's own §6.6 negative-result branch, and recorded in TODO_ALL:

- **C-13a — DEFERRED, not cancelled.** No corpus witness in either
  benchmark: TPC-H has no `LIMIT` at all (design F2), and TPC-DS's LIMITs sit
  over grouped results. It should be reconsidered when a corpus with a
  top-N-over-raw-rows shape exists (`ORDER BY <ungrouped column> LIMIT k` over
  a large scan or join output), which is the shape the optimisation is for and
  which neither benchmark contains. It stays cheap and correct; it is the
  *evidence* that is missing, not the mechanism.
- **C-13b — keep, re-scoped to a cost-model correctness item with C-12, and
  with no timing claim.** `cost_tuplesort`'s middle branch is a real hole in
  the model (`cost_funcs.go:254-259` ledgers it), and closing it is worth
  doing for the model's sake. But this census adds a warning the design did
  not have: the `limit_tuples` branch would be evaluated against
  `tuples` estimates that are wrong by 789× (Q22), 8007× (Q99) and 245 587×
  the other way (Q78). `output_tuples < tuples` and `tuples > 2 * output_tuples`
  are both *comparisons against those estimates*. C-13b will change plan costs
  on that basis, so it must land with a plan-gate re-pin and its diff read
  line by line — it is not the inert cost tidy-up the split implied.
- **C-14 (Incremental Sort) is NOT the better item here either.** The design
  (§11) offered it as the alternative if P2 came back thin, on the grounds
  that PG uses an incremental sort on Q67. goopg's Q67 sort takes **1.0 ms**
  over 115 150 rows. Replacing it with an incremental sort would win the same
  nothing. C-14's case has to be made on plan parity or on a different corpus,
  not on this one.

The general finding, stated once: **no sort-side item has a runtime witness in
goopg's TPC-DS corpus.** All sorting in all 99 queries is under 0.2 % of the
corpus wall time. Time in this corpus is in the joins and aggregates.

## 4. Threats to validity

- **Scale.** SF 0.5. At SF 1 the fact-table-driven sorts roughly double; the
  aggregate-driven ones (54 of 77) barely move, since group counts are
  dimension-driven. A generous doubling puts the addressable total at ~240 ms
  of ~1 600 s. It does not change the verdict.
- **Loaded host.** A concurrent TPC-H arm inflated wall times. This makes the
  *percentages* in §2 conservative in the direction that favours C-13a (a
  smaller denominator would raise them), and the absolute 119.8 ms is
  independent of the denominator.
- **Sort-ms is an upper bound, not a point estimate.** Under-reported child
  end times inflate it (§1). The true addressable total is ≤ 119.8 ms.
- **The two `none`-class outliers** (Q94 774 ms over 2 rows, Q73 534 ms over
  1 row) are that artifact, not sort time. They are excluded from every
  conclusion; neither is bindable anyway.
- **This is a corpus verdict, not a claim about bounded sorts.** A bounded
  sort is the right implementation of `ORDER BY … LIMIT` and PG is right to
  have one. The finding is only that goopg's two benchmark corpora cannot
  measure it.

## 5. Reproducing

The full 516 KB `EXPLAIN ANALYZE` capture is a run artefact and is not
committed (`bench/tpcds/runtime_goopg/tpcds-results-sf05/*` is gitignored for
the same reason). `sort-nodes.txt` carries every plan line the census reads;
regenerate the whole thing with:

```bash
# 1. a binary with known provenance, off a clean worktree at the commit
git worktree add /tmp/wt-head --detach HEAD
( cd /tmp/wt-head && go build -o "$PWD_OF_REPO"/tmp/goopg-c13a-base ./cmd/goopg )

# 2. capture, under the lock and the cgroup cap, on the SF0.5 cluster
source bench/tpcds/env_tpcds.sh          # SF05_PORT=65437, SF05_GOOPG_DATA
flock /tmp/goopg-65437.lock bash -c '
  GOOPG_CG_UNIT=goopg-c13a-census scripts/goopg-test-run.sh \
      tmp/goopg-c13a-base start -D bench/tpcds/runtime_goopg/data-sf05 \
      --listen 127.0.0.1:65437 \
      --hba bench/tpcds/runtime_goopg/data-sf05/pg_hba.conf &
  until pg_isready -h 127.0.0.1 -p 65437 -U postgres; do sleep 1; done
  for q in $(seq 1 99); do
    echo "===== Q${q} ====="
    python3 -c "
import sys
for s in open(sys.argv[1]).read().split(chr(59)):
    if s.strip(): print(\"EXPLAIN (ANALYZE, VERBOSE OFF) \" + s.strip() + chr(59))
" bench/tpcds/runtime_goopg/tpcds-data/queries/query${q}.sql > /tmp/ea.sql
    timeout 300 psql -h 127.0.0.1 -p 65437 -U postgres -d postgres -f /tmp/ea.sql
  done
  tmp/goopg-c13a-base stop -D bench/tpcds/runtime_goopg/data-sf05
' > ea-base.txt

# 3. census
python3 analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/census.py \
    ea-base.txt bench/tpcds/runtime_goopg/tpcds-data/queries
```

The one non-obvious step is splitting each query file on `;` and prefixing
every statement: several TPC-DS files carry more than one statement, and
`EXPLAIN` applied to the file as a whole explains only the first.

`census.py` also runs against the PG captures in `bench/tpcds/plans-pg/` —
prefix each root line with a space first, since those files start the root
node in column 0 while a goopg `psql` capture indents it by one.

## 6. Files

| file | what |
|---|---|
| `README.md` | this write-up |
| `census.tsv` | all 100 `Sort` nodes: bindability, estimated vs **actual** input rows, sort ms, query ms, method, memory, input node |
| `sort-nodes.txt` | the plan lines the census is read from, per query — the evidence |
| `census.py` | the measurement instrument (tree reconstruction, classification, timing) |

---

## Addendum 2026-09-06 — the cluster this was measured on was I/O-bound

Discovered after this census was written: the TPC-DS SF0.5 goopg cluster was
running on the **128MB `shared_buffers` default** (16,384 slots) against a
**1.113 GiB** working set, because its `postgresql.conf` left `shared_buffers`
commented out. `store_sales` alone is 232 MiB — 1.8x the entire pool — and two
sequential scans of it produced **59,522 reads / 43,138 evictions**. Nothing
was resident; every scan re-read from the OS. Fixed to `2048MB` (commit
`0df7dc930`), matching TPC-H and the PG TPC-DS reference.

**This strengthens the census's conclusion rather than weakening it.** The
finding was that all sorting is ≤ 119.8 ms of 802 s = 0.015% of corpus wall
time. That denominator, 802 s, was inflated by I/O the fix removes; the sort
times themselves are CPU work and are not. So the true sort share is *higher
than 0.015% but still bounded by the same absolute 119.8 ms* against a smaller
total — and the go/no-go argument never rested on the ratio anyway. It rested
on three facts the residency defect cannot touch:

- median sort input is **145 rows**, and 54 of 77 bindable sorts read
  already-collapsed aggregate or window output;
- the largest single sort is Q51 at 324,249 rows = **1.9 ms**;
- **0 of 100 sorts spilled**, largest footprint 26 MB against the 256 MiB
  `sortChunkBytes` threshold — so the design's strongest argument, that a bound
  removes a spill outright, still has no witness.

**What must NOT be reused from this artifact**: the per-query and total wall
times in `census.tsv` (`sort_ms` is fine; `query_ms` is I/O-inflated), and any
goopg-vs-PG timing comparison. The est-vs-actual ROW COUNTS are unaffected —
row estimates and actual row counts do not depend on buffer residency — so the
row-estimate defect this census found (ledger
`take3-tpcds-rowest-3-to-5-orders`, diagnosis
`docs/design/planner-rowest-collapse/DESIGN.md`) stands entirely.
