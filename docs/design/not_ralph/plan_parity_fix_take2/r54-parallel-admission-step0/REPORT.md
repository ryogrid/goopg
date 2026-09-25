# R54 Step-0 REPORT — parallel-admission measurement (H-Q5, DS-Q84, H-Q9 control, H-Q1 control)

Date: 2026-09-10. Binary: worktree `/tmp/wt-r49b` @ instrumented cut
(`goopg-r54`, final — includes the `agg-upper` upper-rel hooks; first cut
lacked them). Baseline binary: `goopg-r54-base` (same tree, trace hooks
compiled out). GUCs: `work_mem='64MB'`, `max_parallel_workers_per_gather=4`
(capture-script convention). Trace gate: `GOOPG_PGSHAPED_DP_TRACE=1`.
Evidence (tmp-only, NOT committed): `/tmp/pp2/r54/`
(`q{5,9,1}-r54f.*.plan`, `q84-r54f.*.plan`, `q{5,9}-base.*.plan`,
`q84-base.1.plan`, server logs `start-final.log` (TPC-H :5534),
`start-ds-final.log` (TPC-DS :5533)).

## 1. Plan-identity (instrumentation changed no plan)

Every instrumented EXPLAIN is byte-identical to the baseline binary's plan
AND run-stable (`.1` == `.2`):

| query | base vs r54f.1 | r54f.1 vs r54f.2 |
|---|---|---|
| H-Q5 (serial HashAgg, 0 Gather) | identical | identical |
| H-Q9 (Finalize→Gather→Partial, 1 Gather) | identical | identical |
| H-Q1 (1 Gather) | identical (probe) | identical |
| DS-Q84 (serial Limit→Sort→NL, 0 Gather) | identical | identical |

One anomaly that strengthens the claim: the 7th TPC-H run (Q1 `.2`) emitted
NO DPTRACE at all yet returned the byte-identical 872 B plan — a
server-level cross-session plan-cache hit (`internal/postmaster/plancache.go`,
M0098-0005: doorkeeper admits on second sighting, serves from the third
execution; Q1 text ran probe→mark, `.1`→admit+store+trace, `.2`→hit). The
emission pattern (6 `upper` lines / 7 runs) is exactly predicted by this
policy, and the hit returning the identical plan is independent confirmation
of plan stability. Cap consequence: at most 2 traced runs per query text per
server lifetime — the round stayed within it (Q5×2, Q9×2, Q84×2 traced).

## 2. Admission verdicts (the S0–S4 chain)

Notation: S0 session/query-wide → S1 leaf CP → S2 clause-walk veto →
S3 upper/gather admission → S4 path-level vetoes below green flags.
`failkind=none` = CP assigned true, no veto. `no-partials` = gather gate
reached with CP true but zero partial paths in the rel.

### H-Q1 (positive control) — admits, `split` on both traced runs

`DPTRACE upper gate=agg-upper verdict=split workers=4 divisor=4` on both
traced runs (probe and `.1`; `.2` was plan-cache-silent per §1, no
re-plan). The upper-rel route is live and the control behaves.

### H-Q9 (control: Gather present) — join level silent, upper route splits

- S1: all 6 base leaves `cp=1 leaf=seq`. S2: all 29 join admits
  `failkind=none` (×2 runs identical). S3-joinrel: all 29 gathers
  `verdict=no-partials` — the join-level Gather NEVER fires for Q9 either.
- S3-upper: `gate=agg-upper verdict=split workers=4 divisor=4` (×2). The
  plan's sole Gather arrives exclusively via the upper-rel split route
  (Finalize→Gather→Partial, `Parallel Seq Scan on orders` beneath).
- Lesson for the census: "plan has a Gather" does NOT imply join-level
  admission fired. Step-1 must attribute per Gather-site, not per plan.

### H-Q5 (lost its Gather) — refused at the subtree gate, join level silent

- S1: all 6 base leaves `cp=1 leaf=seq`. S2: all 30 join admits
  `failkind=none` (×2 runs). S3-joinrel: all 30 gathers `no-partials`.
- S3-upper: `gate=agg-upper verdict=refused gate=subtree` (×2).
- Attribution of the `subtree` disjunct (code reading + differential, NOT
  traced — the gate ORs three preconditions at `partialaggupper.go:266`:
  `subtreeHasUnsafeNode || subtreeHasGather || drivingScan == nil`):
  `subtreeHasGather` is false (plan has no Gather); bitmap scans cannot be
  the unsafe node (Q9 carries the same node kinds and splits); by
  elimination the firing disjunct is `drivingScan(child) == nil`. Mechanism:
  `drivingScan` (`parallel.go:614`) catches a plain Nested Loop in its
  `case *Join` but the capability predicates (`hashJoinIsPartialCapable` /
  `mergeJoinIsPartialCapable`) refuse `Algo != Hash/Merge`, so the descent
  returns nil — Q5's top hash join probes into a Nested Loop immediately
  (plan-verified: `Hash Join → Nested Loop → …`),
  while Q9's left spine reaches `Parallel Seq Scan on orders`. So Q5's
  aggregate is refused because the probe-side descent dies on the
  NestedLoop capability refusal. Step-1: confirm by refining the
  `gate=subtree` detail (unsafe vs gathered vs no-driving-scan) and decide
  whether NestedLoop descent is sound (probe-side fan-out semantics) or the
  refusal is correct and the Gather must come from elsewhere.

### DS-Q84 (lost its Gather Merge) — double lock-out, sort route unreachable

- S1: all 6 base leaves `cp=1 leaf=seq` (customer, customer_address,
  customer_demographics, household_demographics, income_band,
  store_returns). S2: all join admits `failkind=none` (50 over 2 runs =
  25/run). S3-joinrel: all 50 gathers `verdict=no-partials`.
- S3-upper-sort: ZERO `gate=sort` emissions across both planned runs — the
  tournament (`partialSortRootPays`) never ran. Mechanism (code-verified):
  the post-pass walk `findPartialSubtree` starts at the plan root and
  `terminatesPartial` (`parallel.go`) stops at `*Limit` — Q84's top node is
  `Limit → Sort → NestedLoop`, so the walk never reaches the Sort. Even if
  it did, Sort terminates "in the GENERAL case" (only Sort-directly-over-
  partial-capable-subtree is taken first), and Q84's Sort sits over a join
  tree, not a partial scan. The sort consumer is unreachable for this shape
  by construction, not by cost.
- S3-upper-agg: N/A (no aggregate in Q84).
- Net: Q84's parallelism is locked out TWICE — joinrel Gather has no
  partials to gather, and the sort route cannot engage under a Limit top.
  The pre-R51 Gather below the Sort therefore came from the joinrel route,
  so the R51 regression is upstream of the gather gate: partial paths
  stopped being generated under CP=true joinrels.

## 3. The Step-1 pointer (what Step-0 discharged and what it did not)

Discharged: the dying gate is NOT S0 (GUCs/session — leaves go cp=1), NOT
S1 (leaf propagation — all seq/bitmap leaves parallel-safe), NOT S2
(clause-walk — `failkind=none` on all 30+30+29+29+25+25 join admits; no
FuncCall/SubPlan/OuterColumnRef veto fires for any of Q5/Q84/Q9). The
post-pass consumers are ALSO exonerated as a class: they stand down whenever
the tree already carries a Gather (`subtreeHasGather`), and for Q5/Q84 the
tree carries none — the verdicts above were reached independently.

Remaining, both pointing at S3/S4:

1. **Zero partial paths under CP=true joinrels (Q5, Q9, Q84 alike).**
   `no-partials` is 100% of gather verdicts (30+30+29+29 TPC-H, 50 DS) even
   though every rel in every block is CP=true. The partial-arm producers
   (`joinpathsparallel.go`) veto everything below green flags — Step-1
   instruments path-level vetoes (S4), not flags.
2. **Q5's `gate=subtree` refusal** — refine the detail to name the disjunct
   (predict: no-driving-scan via the NestedLoop capability refusal, §2) and adjudicate
   soundness.
3. **Q84's Limit-top sort lock-out** — `terminatesPartial` at `*Limit` makes
   the sort consumer unreachable; the only historical route (joinrel Gather
   below Sort) depends on fixing (1).

## 4. Incidental findings (not Step-0 verdicts, recorded for Step-1)

- `DPPATH` lines in the same stderr stream already adjudicate upper-rel
  candidates per producer (`upper.groupagg.split` accepted for Q9/Q1 with
  kind=18; `upper.groupagg.gathered` dominated) — Step-1 can cross-check
  trace verdicts against DPPATH adjudication without new instrumentation.
- `enumtrace.go` discards `cpadmit`/`cpgather`/`upper` without Malformed++
  (suite-pinned); the R54 line kinds are harvestable with
  `grep DPTRACE <server-log>` straight off the log, as done here.
- Trace-test pins landed in the same cut: `joinsearchtrace_test.go`
  (admit/veto/nil-clause/leaf/gather/nil-safe),
  `partialaggupper_test.go` (`agg-upper` split + mode-refusal line shape +
  verdict vocabulary), `enumtrace_test.go` (admission-line hygiene).
