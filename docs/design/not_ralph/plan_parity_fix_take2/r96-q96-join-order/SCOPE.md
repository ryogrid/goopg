# R96 SCOPE: Q96 join-order Step-0 (which dim joins store_sales first)

Lineage: R95 REPORT (Q96 census down to
`[join-order,scan-type,qual-placement]`; aggregation-strategy,
join-method, sort-strategy, parallelism now match). The remaining
distance is decision-shaped, not feature-shaped — measure before
slicing.

## The divergence (anchors, SF0.25 private clone vs live `:65438`)

goopg (`/tmp/pp2/r95/q96-natural.txt`):
`((store_sales ⨝ store) ⨝ hdemographics) ⨝ time_dim(probe)` —
HJ1 `ss_store_sk=s_store_sk` (719876×1 → 57322), HJ2
`ss_hdemo_sk=hd_demo_sk` (57322×720 → 5477), probe → 74.

PG (`ds-pg-live.txt` §Q96):
`((store_sales ⨝ hdemographics) ⨝ store) ⨝ time_dim(probe)` —
HJ1 `ss_hdemo_sk` (232218×720 → 22198, divided rows), HJ2
`ss_store_sk` (22198×1 → 1769), probe → 35.

Rows agree modulo the parallel divisor (PG 1769×3.1 ≈ 5484 vs goopg
5477 final; intermediates 57322 vs ~68800 full-equivalent). The two
orders produce the same rows at different intermediate prices — this
is a pure cost-model preference, and goopg's own numbers for the
losing order were never read. Prime suspect (not verdict): width-driven
hash cost — goopg widths run ~35× PG's here (428/464/472 vs 12/8/4,
the R70 gap), so hashing 720 wide hdem rows first prices very
differently than in PG. Alternatives: build-side choice rule,
fk/join selectivity asymmetry, partial-path costing interaction
(the winner is under a Gather). P0 distinguishes them.

## P0 — candidate-cost attribution (temp-instrumented, foreground)

Binary: clean-HEAD + TEMP env-gated stderr ONLY (revert before any
slice commit). Cluster: private TPC-DS SF0.25 clone + 55xx port.
Query `query96.sql` EXPLAIN ×2, byte-identity required.

1. Log, for the {ss,store}, {ss,hdem}, {ss,store,hdem} joinrels, every
   filed candidate's method/order/rows/width/cost + which producer
   filed it and the `add_path` verdict (dominated vs survived).
2. Verdict table: goopg's priced cost for order B (PG's order) —
   filed-and-dominated (with the dominating term named) vs never-filed
   (with the filing gate named). Name the single term that decides the
   preference (hash build rows×width? build-side rule? selectivity?).
3. Outcomes, both committable: ATTRIBUTED (term named with numbers —
   proceed to P1) or BLOCKED (both orders price within fuzz / the
   decision is made elsewhere — name where, re-scope, no heroic
   tracing here).

No tree mutation in P0 (log only); temp reverted after; EXPLAIN
byte-identity re-verified.

## P1 — slice menu (no implementation)

From the attributed term, enumerate minimal slices (term fix iff
mis-transcribed vs PG with PG-term-keyed comment; width-program
routing if the R70 gap decides it; filing/admission if order B is
absent) with blast-radius notes (join order moves globally — Q9/Q41
controls plus a join-order-sensitive TPC-H control set) and a
recommendation. Implementation is separate work (R97+).

## Gates & bounds

- Temp reverted, builds clean, EXPLAIN byte-identity re-verified.
- values/sweep/pp gates run at slice time, not Step-0.
- Explicitly out: cost-term changes, width model changes (R70 owns),
  election rules, fuzz, parallelism, Q13-inner, NLI/Memoize, forcing
  PG's order. Q9/Q41/Q91 read-only controls; scan-type and
  qual-placement ride as observations, not targets.
