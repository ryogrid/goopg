# M0146-0002a: category regressions from the Parallel Hash arm — attribution recon

Status: recon complete 2026-09-27. Follow-up implementation: M0146-0002i
(SEMI probe) and M0146-0002j (ANTI probe).
Task: `.ralph/fix_plan.md` M0146-0002a (Kind: recon, Parent: M0146-0002).
Evidence: `analysis/m0146/m0146-0002a/` — canonical TPC-H captures
(`tmp/m0146-0015d/tpch/`, commit `d44f472a9` era), a live DP trace on the
TPC-H private lane (`GOOPG_PGSHAPED_DP_TRACE=1`, port :5533, a clone of
`:65433`), and a relpages comparison against PG 18.3 `:65432`.

## The question

At M0146-0002 slice 2 (`cf02e88b9`), three TPC-H queries gained categories
when the `Parallel Hash` arm started winning elections:

- Q12: `+join-method +join-order +scan-type`
- Q21: `+join-method`
- Q4: traded `aggregation-strategy,join-order` for `join-method,scan-type`

The recon question: is the Parallel Hash arm mis-electing, or did it expose
older gaps?

## Verdict up front

**The arm is electing correctly on all three queries.** The residuals split
into one real planner gap — the parameterized-probe partial nested loop is
INNER-only end to end — and one physical boundary that no planner change
can close — goopg's heap is physically smaller than PG's, which legitimately
moves elections and worker counts.

## Q4 — the partial SEMI probe is filed, dominates, and cannot drive a Gather

PG 18.3 (`q4-pg.txt`):

```
Finalize GroupAggregate -> Gather Merge (4 workers)
  -> Partial GroupAggregate -> Sort
    -> Nested Loop Semi Join (cost 0.43..68894.24)      <- partial path
      -> Parallel Seq Scan on orders (14370 rows/wkr)
      -> Index Scan on lineitem (probe, 0.43..10.69, semi-discounted)
```

goopg at `d44f472a9` (`q4-goopg.txt`): `GroupAggregate -> Sort -> NL Semi ->
[Gather(3w) -> PSeq orders | Index Scan lineitem]` — the NL Semi sits ABOVE
the gather and re-probes the index for all 57340 gathered rows (total
171540 vs PG's 68894).

The DP trace (`q4-dptrace.txt`, canonical stats — the clone reproduces the
`:65433` plan byte-for-byte) shows the real mechanism, and it is worse than
"never filed":

```
DPPATH partial producer=join.nestloop.partial relids={0,1} jointype=semi
       rows=4983 startup=0.375 total=76062.64  verdict=accepted   <- the probe
DPPATH partial producer=join.hash.partial.parallelhash relids={0,1}
       jointype=semi total=174292.91 verdict=accepted
DPTRACE cpgather rel={lineitem+orders} partials=1
```

1. `addPartialNestLoopPaths` admits jointype SEMI (`joinpathsnli.go:465`)
   and files the parameterized index probe as a partial path. Post
   M0145-0008l's semi early-exit discount it costs **76062 — essentially
   PG's 68894** — and *dominates the PHSJ (174292) in the partial
   pathlist*.
2. `generateUsefulGatherPaths` calls `makeGatherPath(rel,
   rel.PartialPathlist[0], ...)` — head only (`gatherpaths.go:200`). The
   head is the NL probe.
3. `partialPathDrivingKind`'s parameterized-inner arm returns `PathPrebuilt`
   for anything but `JoinInner` (`gatherpaths.go`, the `p.Jointype !=
   parser.JoinInner` refusal) — so no Gather is emitted.
4. The PHSJ sibling is already gone from the list (dominated), so there is
   no admissible fallback: the joinrel gets no gather, and the plan falls
   back to a **serial** NL over `Gather(orders)` — the shape the slice-2
   capture replaced and PG never considers.

This is exactly the "filed-at-head starves admissible siblings" failure the
producer's R94 comment says the filing gate exists to prevent — but the gate
admits SEMI *as a jointype* while the downstream gates admit SEMI only for
an **unparameterized** inner. The probe subclass slips through the
invariant.

PG correspondence: `try_partial_nestloop_path` (joinpath.c:1842-1846)
admits parameterized inners for {INNER, LEFT, SEMI, ANTI} — the probe is
just `innerrel->cheapest_parameterized_paths` feeding the same pathbuilder.

Fix filed: **M0146-0002i**.

## Q21 — the ANTI probe is refused at the producer gate

PG 18.3 (`q21-pg.txt`):

```
NL Semi -> [ Gather(4w)
              -> NL Anti                                   <- partial
                 -> Parallel Hash Join (orders ⋈ PH(l1⋈supplier))
                 -> Index Scan on l3 (probe per worker)  ]
         -> Index Scan on l2 ]
```

goopg (`q21-goopg.txt`): same node multiset, but `NL Anti` sits ABOVE the
Gather, probing `l3`'s index for all 39277 collected rows. (The slice-2
`Parallel Hash Anti Join` that "regressed" Q21 is gone at HEAD — later
costing already moved it back to a probe; what remains is the probe being
on the wrong side of the gather.)

`q21-dptrace-anti-refusals.txt` shows the refusal firing twelve times,
including the exact PG outer set:

```
DPTRACE pveto site=nestloop rel={l1+l3+orders+supplier}
        dir={l1+orders+supplier}+{l3} veto=V1-nl-inner detail=jt=ANTI
```

Unlike Q4 the path never exists: the producer's `jt != Inner && jt != Semi`
test (`joinpathsnli.go:465`) refuses ANTI wholesale — whole-inner and probe
alike — even though the ordinary whole-inner ANTI partial NL is already
executor-supported (`TestParallelLeftAntiNestedLoopIdentity`; the verdict
is per-outer-row and worker-local, `finishOuter` in join_nl_stream.go).

Fix filed: **M0146-0002j** — same three-gate widening as (i) plus the
producer gate itself.

## Q12 — a physically smaller heap flips the election; not a planner defect

PG elects `NL -> [PSeq lineitem | orders_pk probe]` at 190868; goopg elects
`Parallel Hash Join` at 184617 over its own NL at ~195k.

`relpages.txt` — same row counts, different heaps:

| table | goopg pages | PG pages | Δ |
|---|---|---|---|
| orders | 26545 | 27814 | −4.6% |
| lineitem | 115293 | 129346 | −10.9% |
| part | 3809 | 4199 | −9.3% |

goopg's page format packs tighter, so `costParallelSeqscan` (a faithful
port — disk charged whole, CPU/rows through `getParallelDivisor`) prices
PG's lineitem scan 163090 vs goopg's 149054 — the delta is ≈
`seq_page_cost × (129346−115293)`, i.e. *entirely real pages*. With the
hash side cheaper by ~14k, PHJ legitimately wins under goopg's costs; under
PG's larger heap the NL wins. Same logic, different physical input — the
election is correct on both engines.

The same boundary moves worker counts: `compute_parallel_worker` adds a
worker per ×3 over `min_parallel_table_scan_size` (1024 pages), so orders
needs ≥27648 pages for 4 workers. PG's 27814 qualifies; goopg's 26545
gives 3 — a 4.6% packing difference straddling the threshold explains most
of the residual `Workers Planned: 3 vs 4` lines.

Nothing here is planner-actionable: adopting PG's page counts would
misprice goopg's real I/O, and parity-by-heap-packing is a storage-format
property. Ledgered as a boundary note so it is not re-litigated.

## What is filed vs ledgered

- **M0146-0002i** (impl): admit the parameterized-probe partial NL under
  SEMI — classifier `partialPathDrivingKind` probe arm +
  `lateralProbeJoinIsPartialCapable` + executor `lateralProbeJoinPartial`.
  Producer already admits SEMI. Expected: Q4's join spine becomes PG's
  (NL Semi inside the gather, ~76k), clearing `join-order` and likely
  `sort-strategy`/`parallelism` there.
- **M0146-0002j** (impl): the same for ANTI, plus the producer gate.
  Expected: Q21's NL Anti inside the gather — `join-order`,
  `qual-placement`, `parallelism` movement.
- **Ledger**: LEFT probe arm (worker-local by the same argument but no
  measured consumer; the "widen together or not at all" convention keeps
  it refused), and the heap-packing boundary (Q12 election + worker-count
  class).

## Gates run

None required — recon only, no code change. The live trace and EXPLAINs
above were captured on the private clone (`:5533`, cgroup-capped) with
`GOOPG_PGSHAPED_DP_TRACE=1`.

## M0146-0002i outcome (2026-09-27, impl)

The SEMI arm landed as predicted. Rather than widening each gate's inline
jointype check independently, one shared set — `{INNER, SEMI}` — is now
read by all three probe gates: `partialProbeNestLoopJointype`
(`parser.JoinType` domain, gatherpaths.go) for `partialPathDrivingKind`'s
parameterized-inner arm, `partialProbeNestLoopJoinType`
(`optimizer.JoinType` domain, parallel.go) for
`lateralProbeJoinIsPartialCapable`, and the same set inline in executor
`lateralProbeJoinPartial`. The producer admission `{INNER, SEMI}` was
already correct and is untouched. ANTI waits for M0146-0002j's producer
widening; LEFT and bitmap probes stay refused; the fused
`NestedLoopIndexJoin` family keeps its verified {I,L,S,A} set.

Measured on the canonical parallel TPC-H arm: Q4's join spine is now PG's
— `Nested Loop Semi Join` inside `Gather` over `Parallel Seq Scan orders`
with the `Index Scan` probe on `idx_lineitem_orderkey_fkidx` per worker
(cost 76255 vs PG 70092; pre-change serial-above-Gather at 172846). Q4's
categories went `[join-order, aggregation-strategy, sort-strategy,
parallelism]` → `[sort-strategy, parallelism]`; aggregate TPC-H
join-order 14→13 and aggregation-strategy 6→5. The two residual
categories belong to the `Gather Merge` + `Partial GroupAggregate` upper
(M0146-0003/0025), not this arm. TPC-DS moved zero plans at either scale
(fire-set `fires=none`).

Worker-locality holds by the same per-outer-row argument the whole-inner
SEMI arm already records: one qualifying probe row decides the outer row
and the probe breaks (`finishOuter`). Verified end to end:
`TestParallelLateralSemiProbeIdentity` (serial-vs-parallel row identity,
`-race`, workers 1/2/4), the four classifier/walk boundary tests
re-pinned to {I,S} with LEFT/ANTI as the refused set, and the executor
walker test admitting SEMI on all three attach walks. Gates: units,
tpch-spotcheck (Q12=2/Q13=33), tpcds-sf025 (96/96, 99/99 shapes),
tpch-acceptance-arm (24/24 value-identical), tpcds-fireset (no fires) —
all PASS.

## M0146-0002j outcome (2026-09-27, impl)

The ANTI arm landed as predicted: the producer gate went {INNER,SEMI} →
{INNER,SEMI,ANTI} and the three probe gates moved to the same set in the
one change (the shared predicates now carry `parser.JoinAnti` /
`JoinTypeAnti`). The producer now files ANTI for both inner families —
the whole-inner arm had already proven ANTI
(`TestParallelLeftAntiNestedLoopIdentity`, M0145-0010) and the probe
arm's gates widened with it. LEFT and bitmap probes stay refused.

Measured on the canonical parallel TPC-H arm: Q21's `Nested Loop Anti
Join` is inside `Gather` probing `idx_lineitem_orderkey_fkidx` on l3 per
worker — the placement PG shows — at 182221 (vs pre-change 263100, vs PG
269864; ~2.3 s with canonical 412 rows). The residual is join-order on
the outer tree: goopg joins (l1↔supplier) inside the gather then probes
orders above it, where PG builds `Parallel Hash Join` over all three
inside the gather. Categories went `[join-order, parallelism,
qual-placement]` → `[join-order, join-method, parallelism,
qual-placement]` — the targeted placement landed while the outer
reelection added a method-label diff; the attribution class stays
D1-sublink (unchanged), not the partial-path arm. TPC-DS moved zero
plans at either scale (`fires=none`).

Verified end to end: `TestParallelLateralAntiProbeIdentity`
(serial-vs-parallel row identity, `-race`, workers 1/2/4), boundary
tests re-pinned to {I,S,A} with LEFT refused, producer test now asserts
{I,S,A} filed / LEFT unfiled. Gates: units, tpch-spotcheck
(Q12=2/Q13=33), tpcds-sf025 (96/96, 99/99 shapes), tpch-acceptance-arm
(24/24 value-identical), tpcds-fireset (no fires) — all PASS.
