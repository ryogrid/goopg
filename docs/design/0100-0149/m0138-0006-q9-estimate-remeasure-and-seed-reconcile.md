# M0138-0006 — Q9 estimate re-measure vs R130's table + `GOOPG_ANALYZE_SEED` reconciliation

Status: accepted (landed 2026-09-15)

## Task

Two sub-tasks, per `.ralph/fix_plan.md`'s M0138-0006 line:

1. Re-measure TPC-H Q9's row-count estimate at HEAD (after M0138-0002/-0003/
   -0004 landed) against `METHODOLOGY3/01-what-we-learned.md`'s R130 table —
   the pre-registered prediction is that porting PG's ANALYZE sampler moves
   goopg's estimate **toward PG's 60,125**, because the Question 2 decision
   ("YES, reproduce PG's estimation errors") assumes the errors originate in
   statistics computation. If it does not move that way, that is itself the
   finding, and M0142's premise needs re-examination before its slices are
   scoped.
2. Reconcile `GOOPG_ANALYZE_SEED`'s determinism knob with PG's own sampler
   seed sequence — retire it, or document why both must coexist.

This is a measurement-only recon task (per `AGENT.md` §"Plan-parity harness"),
matching the shape of M0138-0001/-0005. No production code changed.

## Method

Bootstrapped per `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`.
Both TPC-H lanes (`:65433` goopg, `:65432` PG) were already up at loop start
(shared `:6543x` block — verified, not restarted); the goopg lane already
carries the pinned `GOOPG_ANALYZE_SEED=20260905` from `bench/tpch/env_goopg.sh`
(confirmed via `/proc/<pid>/environ`), so the capture below reads goopg's
already-warmed per-connection statistics through the same deterministic
sampler M0138-0002/-0003/-0004 landed.

```
go build -o /tmp/estimate-audit-m0138-0006 ./cmd/estimate-audit
PGPASSWORD=tpch /tmp/estimate-audit-m0138-0006 -plan-only \
  -label m0138-0006-q9-remeasure -out analysis/m0138 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres \
  -queries 9
```

Output: `analysis/m0138/m0138-0006-q9-remeasure.{txt,plans.txt,pg.plans.txt}`
(scratch, untracked, same precedent as M0138-0001/-0005's `analysis/m0138/`
artefacts).

## Results — 1. Q9 estimate

| estimate source | estimate | vs actual (175) |
|---|---|---|
| goopg, pre-M0138 default (R130, `01-what-we-learned.md`) | 97 | 1.8x low |
| **goopg, HEAD (post M0138-0002/-0003/-0004)** | **146** | **1.2x low** |
| PG 18.3 | 60,125 | 344x high |

goopg's top-level `HashAggregate`/`Sort` estimate moved **97 -> 146** (+50%),
landing much closer to the **actual** 175 than to PG's 60,125. In log-distance
terms goopg moved from `log(175/97)=0.58` to `log(175/146)=0.18` — closer to
truth — while PG's estimate sits at `log(60125/175)=5.84`, over an order of
magnitude further out in the other direction. **The estimate did not move
toward PG.** Per the task's own pre-registered conditional, this is the
finding, and M0142's premise (that porting PG's stats-gathering machinery
would, on its own, reproduce PG's Q9 join-cardinality error) needs
re-examination — not abandonment; see "Where the divergence actually comes
from" below for the redirected hypothesis M0142-0001/-0002 should start
from.

**Leaf-level check — did M0138 converge the underlying stats?** Comparing the
first join in each plan (`partsupp ⋈ part`, filtered on `p_name LIKE
'%green%'`): goopg estimates 12,121 filtered `part` rows / 48,484 joined rows;
PG estimates 10,101 / 40,404 for the same join. Both read `partsupp`'s full
800,000 rows identically (a base-table count needs no statistics). These are
now the **same order of magnitude** on both engines (ratio 1.2x) — consistent
with M0138-0005's corpus-wide finding that `n_distinct` sign/order-of-magnitude
agreement is now near-total. **The leaf statistics converged; the top-level
estimate did not.** The 344x gap is not explained by a residual per-column
statistics gap of the kind M0138 targets.

### Where the divergence actually comes from (hypothesis, not adjudicated here)

The two plans diverge in **join method/shape**, and that is where the
estimates diverge:

- **goopg's plan** drives the chain with index-scan Nested Loops on the FK
  path (`lineitem_part_supp_fkidx`, `supplier_pk`, `orders_pk`), each
  `Index Scan` estimated at `rows=1` per probe (a correct estimate — the FK
  join is genuinely near-unique per probe). The compounding chain stays near
  the 146-row final estimate.
- **PG's plan** instead runs a chain of **Hash Joins** over materialized
  intermediate results (`partsupp ⋈ part` -> `⋈ supplier` -> `⋈ nation` ->
  `⋈ lineitem` -> `⋈ orders`), each priced by `eqjoinsel`
  (`postgres/src/backend/utils/adt/selfuncs.c:2280`) /
  `calc_joinrel_size_estimate`
  (`postgres/src/backend/optimizer/path/costsize.c:5501`) independently per
  join clause. Multiplying independent per-join selectivities down a
  correlated FK chain (`partsupp` -> `lineitem` -> `orders`, `supplier` ->
  `nation`) without extended/cross-column statistics is a well-known PG
  cardinality-blowup mode, and here it compounds from `partsupp ⋈ part`
  (~40,404) through `⋈ supplier` (~302,967) up to the final 60,125 — three
  orders of magnitude of compounding that never happens on goopg's index-
  probe path because each probe stays anchored at `rows=1`.

So the 344x gap looks **plan-shape-driven, not stats-precision-driven**: to
reproduce it, goopg's planner would need to *choose PG's Hash-Join chain*
(a join-order/method decision), at which point the same independent-selectivity
compounding this milestone group's own cost model already implements would very
likely reproduce a similar-magnitude estimate. This was not verified by
forcing goopg onto that shape (out of scope for a measurement-only recon, and
forcing shapes is explicitly against the milestone's goal) — it is a
hypothesis for M0142-0001/-0002 to test, not a conclusion. Filed as a ledger
row with this exact resume point.

## Results — 2. `GOOPG_ANALYZE_SEED` reconciliation

Read `internal/executor/operators_analyze.go:702-760` (`analyzeSeedEnv`,
`analyzeSeedFor`) against PG's actual seeding
(`postgres/src/backend/commands/analyze.c:1227`,
`postgres/src/backend/utils/misc/sampling.c:52,139,234,271,286`):

- **PG has no reproducible-ANALYZE-seed knob.** `acquire_sample_rows` draws
  `randseed = pg_prng_uint32(&pg_global_prng_state)` (`analyze.c:1227`) — a
  process-global PRNG state seeded once per backend from a
  time/PID-derived value, not from any GUC or environment variable. Two `ANALYZE`
  runs of the same PG backend, or two different backends, get different
  samples; there is nothing on the PG side to port a "reconciliation" against.
- **goopg's unset behaviour already matches this exactly**: `analyzeSeedFor`
  returns `time.Now().UnixNano()` when `GOOPG_ANALYZE_SEED` is unset or `0`
  (the documented sentinel) — a fresh wall-clock draw per relation, the same
  "no external determinism" property PG has. Production and every unset run
  are bit-for-bit unaffected by the variable's existence.
- **`GOOPG_ANALYZE_SEED` is purely a measurement-harness knob**, with no PG
  counterpart to "coexist" or conflict with: it exists because goopg's stats
  are per-connection and the plan-capture harness re-`ANALYZE`s per session
  (`cmd/estimate-audit -warm-stats`), so an unpinned wall-clock seed made two
  captures of the *same binary* disagree by up to 455 estimate lines / 27
  plan-shape lines (measured 2026-09-05, cited in the existing code comment)
  — noise larger than the signal every planner A/B in this programme is
  trying to read. PG capture runs don't have this problem because the M0137
  harness ANALYZEs the PG reference once per capture session too, but PG's
  reference cluster is captured from a **separately maintained, long-lived**
  session in the M0137-0003 procedure, not re-ANALYZEd fresh inside the
  measurement tool the way goopg's per-connection model requires.
- The per-relation OID-mixing (`analyzeSeedEnv ^ int64(tbl.OID)`) has no PG
  analogue either — it exists solely to stop a single pinned seed from
  replaying the identical random stream on every table (which would
  correlate which sample positions survive across all tables, systematically
  under-representing the pinned run vs an unpinned one). This is a
  harness-quality concern with no upstream question to reconcile against.

**Verdict: keep `GOOPG_ANALYZE_SEED`, no code change.** There is nothing to
retire it in favour of — PG's own sequence is exactly the wall-clock-style
draw goopg already falls back to when the variable is unset, so "reconcile
with PG's sequence" resolves to "confirm the unset path already reproduces
it" (true, confirmed above) plus "the pinned path is harness-only and PG has
no equivalent to disagree with" (also true). The existing code comment at
`operators_analyze.go:702-751` already stated this correctly; this task's
contribution is confirming it against the actual PG source rather than
trusting the comment's own claim.

## Deferral

One ledger row: the plan-shape-driven-not-stats-driven hypothesis for Q9's
344x PG estimate, with M0142-0001/-0002 named as the resume point (those
tasks already exist in `.ralph/fix_plan.md`; this is not a new task, it is
narrowing their starting hypothesis so M0142-0001's recon does not have to
re-derive it).

## Gates

`go build ./...` unaffected (no source changed). No values-gate re-run needed.
Pre-commit hook's pgbench smoke: not re-run this loop (no production code
touched; same precedent as M0138-0005).
