# R67 TRIAGE — (a) Materialize re-measured on post-R66 numbers: still queued (2026-09-11)

Due from R65 REPORT §4 and the R66 close-out: re-measure R63's
insufficient-alone verdict with current numbers, then sequence what's
next. No code in this round (tree clean throughout); cadence per goal
process (TODO → doc → review → commit+push) all the same.

## 1. Re-measured evidence (all live, pinned env)

- **Live PG TPC-H uses Materialize exactly ONCE** (`r66s2cpg.pg.plans.txt`,
  re-captured this week): Q5's 1-row `region` NL inner
  (`Materialize → Seq Scan on region, Filter r_name='ASIA'`).
  goopg prints it 0 times (no `PathMaterial` by design,
  `joinpathsmergeouter.go:67`; executor replays NL probes,
  `createplannl.go:367` — both citations re-verified, line numbers
  corrected from R63's `:68`/`:357`).
- **Q5's surroundings diverge in 5 categories**
  (`pp66-s2cpost.txt`: MISSING-NODE
  `[join-order,join-method,parameterisation,aggregation-strategy,
  sort-strategy]`): nation×region is Hash Join in goopg vs Nested Loop
  + Materialize in PG; top agg HashAggregate vs GroupAggregate-over-Sort.
  Materialize is not a verdict category — a perfect producer moves zero
  categories and zero matches on Q5. The NL shape it would decorate is
  itself the join-method gap (cost-driven).
- **Q8's case evaporated live.** The stale fixture has Materialize
  (`plans-pg/Q8.txt`); live PG Q8 does not (re-planned amid stats drift
  — same oscillation family as R66's Q8 finding). Nothing to converge
  to; fixtures ≠ target (K9 standing).
- **DS sample scales the pattern, doesn't change it.** PG Q65 (fixture)
  materializes a 12-row Merge-Join inner; goopg hash-joins a different
  shape with no materializable NL inner (`ds-sweep-s2c` plans). A
  producer decorates NL inners goopg already picks — and where goopg
  picks them, the join-method gap sits underneath. (DS PG fixtures carry
  the K10 work_mem caveat; the verdict rests on TPC-H live numbers,
  the DS sample only checks the pattern generalises.)
- **No MATCHING query needs it.** All 6 TPC-H matches
  (Q1/Q6/Q10/Q11/Q14/Q15a-VIEWBODY — `pp66-s2cpost.txt` match=6) have
  zero PG Materialize lines. There is no query-closing opportunity
  anywhere.

## 2. Verdict

R63 stands, unchanged on post-R66 numbers: **(a) stays queued behind
the join-order/join-method costing program** that owns Q5's
surroundings (and Q65's). Materialize becomes last-mile label work
once NL-inner shapes converge — cheap then, motionless now. Landing a
producer today would add a new plan-node type plus EXPLAIN rendering
plus wiring that never fires on the live corpus (the executor's
materialize-and-replay mechanism itself is tested —
`join_nl_stream_test.go` — what is untested is the node/render/wiring
around it; R66's unwinnable-path lesson in that precise form) for
zero category movement.

Two bookkeeping honesty items. First, the tracked gate runs against
fixtures, so a producer WOULD move Q5/Q8 MISSING-NODE→SHAPE-DIFF
there (`PG-only node kinds: Materialize`); dismissed by K9 (fixtures
are disqualified as parity targets), not by silence. Second, goopg
emits `Memoize` where live PG emits zero anywhere — a parameterisation
driver already inside one of Q5's five counted categories, not a
separate finding.

NL-inner census (this round, `r66s2cpost.plans.txt` — the verdict
depends on it, so it was run, not assumed): 36 goopg `Nested Loop`
sites; inners are hundreds-to-millions of rows or already-joined
subtrees, except dimension-probe tails (Q11/Q20/Q21 1-row nation
scans — but PG prints no Materialize on those queries either) and
Q7's 2-row nation cross product (PG hash-joins there — join-order
grounds). PG's sole Materialize site (Q5's 1-row region inner) meets
a goopg Hash Join. **No coinciding site exists**: the re-triage
condition (a goopg small-NL-inner where PG prints Materialize with
otherwise-matching surroundings) is concrete, falsifiable, and
currently unmet.

## 3. Sequencing (the triage's other job)

Post-R66 category leaders — TPC-H (`pp66-s2cpost.txt`):
join-order=14, join-method=12, aggregation-strategy=10,
parameterisation=10, sort-strategy=9, scan-type=9; rendering=1 (Q10
FD-trim, planner work owned elsewhere); TPC-DS (ROADMAP, pre-R66):
join-order ~95, parallelism ~89. **Join-order is the dominant,
untouched blocker on both corpora**, and R51→R53 already named its
costing half (`join_search_one_level` pricing + parallel admission;
R53 Step-0/Slice-1 measured the L6 hash-arm margin to the cent).

- **Next major round: join-order costing** (the R53 lineage). It is
  required for nearly every non-match (ROADMAP conjunction rule:
  no single fix flips any query alone, and Q4/Q22 carry no join-order
  divergence at all — the claim is category leadership plus the
  existing lineage, not exclusivity).
- **#6 (large-group stats), R61 #4 (CTE tagging), (b) Q4
  (cost-model framing): stay deferred** — nothing in post-R66 numbers
  promotes any of them (each deferred 2–3× on the same grounds; the
  grounds have not moved). Stasis-justified, thinly: the next round
  touching aggregation-strategy must re-derive #6's deferral from its
  own numbers rather than re-carrying it.
- **(a) re-triage condition (do not re-litigate on cadence):** revisit
  only when a goopg plan picks a small NL inner where PG prints
  Materialize with otherwise-matching surroundings — i.e. evidence of
  a closable gap, not elapsed rounds.

Evidence live-only, cited inline (`/tmp/pp2/r66/r66s2cpg.pg.plans.txt`,
`r66s2cpost.plans.txt`, `pp66-s2cpost.txt`, `ds-sweep-s2c/`).
