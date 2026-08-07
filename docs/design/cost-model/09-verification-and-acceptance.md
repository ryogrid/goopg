# 09 — Verification and Acceptance

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | all preceding chapters |

## 0. Why this chapter exists

A cost model changes plan *choice* across the whole workload, so "it looks faster"
is not evidence and "it matches PostgreSQL" is neither achievable nor the goal.
This chapter defines a measurable acceptance bar with three tiers — a cheap
mechanical invariant, a self-relative performance recovery, and a divergence-aware
plan gate — and states the honest-measurement discipline that produces the
numbers. It follows the precedent of the two prior cost-model docs, neither of
which defined success as matching PG's plan
([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §8 uses
named plan-shape properties; [fix-for-q5/02](../fix-for-q5/02-cost-model-and-selective-equivalence.md) §9
uses structural properties plus self-relative runtime).

## 1. Tier 1 — the rows-invariant gate (cheap, strong, mechanical)

The cost model re-*selects* plans; it must never re-*estimate* cardinality
(invariant #2, [03](03-path-substrate-and-plan-creation.md) §1.1). The mechanical
enforcement:

> Across the TPC-H reference set, `EXPLAIN`'s `rows=` for every node is
> **byte-identical** before and after the cost model lands.

This is a strong, almost-free check. It fails the instant `costOf` re-derives a row
count instead of reading `RelOptInfo.Rows`, which is exactly the mistake that would
reintroduce Round-4's one safe property as a bug. It is run in CI as a plan-snapshot
diff restricted to the `rows=` column.

A second mechanical check guards [07](07-cost-driven-join-order.md) §4: run the
planner **twice** on the same input with the same statistics and assert an
identical chosen plan. This catches a float tie-break that is not deterministic
before it reaches the plan gate.

## 2. Tier 2 — self-relative regression recovery (the milestone bar)

Measured on real SF1 data, against **goopg-self** baselines (not against PG's
absolute times):

> The five Round-4 statistics regressions recover — Q22 (was 128×), Q4 (79×), Q8
> (53×), Q2 (26×), Q12 (4.4×) — to within a small factor of their R3 (no-stats)
> times, **without** losing the Q5 win (415 s → ~18 s must be preserved, not
> reverted).

The paired condition is the whole point: a cost model that recovers the five by
reverting to the pre-statistics small-dimension heuristic would also revert Q5,
which is a regression wearing a fix's clothes
([07](07-cost-driven-join-order.md) §3). Both directions are asserted.

Correctness anchors ride along: `scripts/tpch-spotcheck.sh` must still show
**Q12 = 2 rows / Q13 = 33 rows** (the group-count anchors the whole tree uses),
proving the plan changes did not alter results. And every one of the 24 TPC-H row
counts must still equal R3 — the same property Round 4 verified, now under a plan-
*changing* model rather than a plan-*preserving* one, which makes it a stronger
statement.

**Measurement configuration** ([memory: cgroup-capped bench server; sequential
gates]): capped bench server (`scripts/csq-bench-server.sh`), one server start,
`ANALYZE` the eight tables in-session before the stream (so `RowCount` is live in
memory — [05](05-statistics-and-estimation-inputs.md) §4), `--per-query-timeout`
generous, bench server and pgbench smoke run **sequentially, never concurrently**.
Report the worker count with every number.

## 3. Tier 3 — two plan gates: self-snapshot regression, then vs-PG divergence

There are **two distinct gates**, and an earlier draft of this chapter conflated
them. Both are used, at different phase boundaries:

- **`make plan-gate` is a goopg-vs-*self-snapshot* regression gate**, not a vs-PG
  diff. `cmd/plan-snapshot` captures goopg's own `EXPLAIN` for every TPC-H query
  against the running goopg bench server (`127.0.0.1:65433`) and diffs the current
  capture against the newest saved baseline `plan_snapshots/*.txt` (it SKIPs, exit
  0, if no baseline or no server). This is the gate the **plan-preserving** phases
  (C0–C3, [11](11-roadmap.md)) use: capture a baseline before C0, then require
  **zero diffs** — byte-identical `EXPLAIN`, mock `cost=0.00..0.00` included, since
  the `structural` mode's `(rows=N)` regex does not match the current PG-style
  annotation. Plan-*changing* phases (C4, C6) re-baseline and review the diff.
- **The vs-PostgreSQL comparison is `scripts/pg-oracle-diff.sh` /
  `scripts/pg-regress-runner.sh`**, which diff goopg's plan/output against a real
  PostgreSQL. This is the gate that classifies plan *divergence* at C4. Under a
  cost model it **cannot** be an equality gate: goopg legitimately chooses plans PG
  cannot express. It is used as a **classifier**:

- Every place goopg's chosen plan differs from PG's is recorded and classified
  against a **Divergence-from-PostgreSQL allow-list**, assembled from the per-
  chapter divergence sections:
    - hash semi/anti where PG uses an index-nested-loop semi/anti
    ([06](06-scan-and-join-path-costs.md) §2.4) — **allowed**;
  - a redundant sort a syntactic pathkey comparison did not elide
    ([04](04-pathkeys-and-ordering.md) §2.1) — **allowed**;
  - the mutex-merge partial-aggregate shape
    ([08](08-parallel-paths-and-degree.md) §5) — **allowed**.
- A divergence **not** on the allow-list is a **new, unexplained** plan difference
  and **fails** the gate for investigation. This is where a costing bug surfaces:
  if goopg picks a plan PG would never pick and it is not a known structural
  divergence, the cost function is wrong.

Crucially, the vs-PG comparison is on plan **shape**, not cost **numbers**: goopg's
`cost=` need not equal PG's (§3.1). The surfaced costs are for human debuggability
and internal consistency ([03](03-path-substrate-and-plan-creation.md) §5). And the
self-snapshot `make plan-gate` never looks at PG at all — it only guards goopg
against *unintended* plan drift between commits.

### 3.1 PG is the oracle for functions, never for plans

The distinction the whole bundle rests on: PG is the authority for the cost
*functions and constants* (reproduced in [02](02-pg-path-and-cost-oracle.md)), and
those are validated by reading PG source and by unit tests that check goopg's
`cost_seqscan` etc. produce PG's numbers for known inputs. PG is **not** the
authority for the final *plan* — goopg's executor makes some PG plans impossible
and some goopg plans unavailable to PG. Conflating the two would make the
gate demand goopg abandon its own operators, which is not the goal.

## 4. Honest measurement discipline

Inherited from [parallel-query/09](../parallel-query/09-verification-and-measurement.md) §8.3
and the Round-4 doc's own discipline:

- **State the worker count with every number.** A time without a degree is
  meaningless once parallelism is a cost decision.
- **Run serial and parallel sweeps back to back**, same server, same in-session
  ANALYZE, so a parallelism effect is not confounded with a statistics effect —
  the exact decomposition Round 4 used to separate the two stories.
- **Watch for the known measurement traps** ([memory: throttle-trap; orphaned
  test servers]): a collapsed sweep tail or a CREATE-VIEW ballooning is an
  environment artefact (cgroup throttle band, leaked server), not a code
  regression; re-run rather than record.
- **No false precision.** Single-sweep numbers are reported as single-sweep; a 1.1×
  move is noise, a 10× move is signal.

## 5. Standard gates

Every implementation phase ([11](11-roadmap.md)) also carries the standing gates,
listed once: unit tests, `make race-gate`, `scripts/tpch-spotcheck.sh` (Q12 = 2 /
Q13 = 33), `make plan-gate` (the §3 self-snapshot regression gate; and
`scripts/pg-oracle-diff.sh` for the vs-PG classification at C4), and the pre-commit
pgbench smoke ([memory: never `--no-verify`; bench server stopped before commit]).
Only the *additional* gate per phase is called out in the roadmap.

## 6. Unit-level cost checks

Beyond the workload gates, each cost function gets a focused unit test asserting it
reproduces the oracle for hand-computed inputs — e.g. `cost_seqscan` on a
1000-page, 100 000-row relation returns `seq_page_cost·1000 + (cpu_tuple_cost +
…)·100000`, and `get_parallel_divisor(2)` returns `2.4`
([02](02-pg-path-and-cost-oracle.md) §4.8). These pin the constants and formulas so
a later refactor cannot silently drift them from PG, the same discipline
[memory: GUC defaults must match PG] applies to the boot values.

## 7. Divergence from PostgreSQL

- **Acceptance is self-relative and shape-classified, never plan-equality** (§2,
  §3). This is the necessary consequence of goopg having a different executor; it
  is a divergence in *how success is defined*, chosen so goopg is not forced to
  abandon its own operators to satisfy a gate.
- **Cost *numbers* are not gated against PG** (§3) — only cost *functions* are
  (§6). goopg's `cost=` is validated for internal correctness, not for equality
  with PG's, because the two run different executors.
