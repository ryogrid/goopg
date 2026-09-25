# M0139-0007a (arm 1/2) — measure `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108)

Status: accepted (measured 2026-09-15; **decision: HOLD — stays default-off**)

## Task

`.ralph/fix_plan.md` M0139-0007a: measure the two already-built, never-measured
R108/R113 absorption arms independently against the post-M0137–M0142 corpus
and decide adopt/hold per the plan-parity metric, one design doc per arm.
This document covers the first arm, `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108) —
the hash-join spill/batch-decision currency. The companion arm
(`GOOPG_PG_SORT_RELATION_BYTES_COST`, R113) is measured independently in
`m0139-0007a-sort-relation-bytes-cost-measurement.md`, deliberately not
combined in one run: M0139-0007's own recon named row 3 (Sort) as feeding row
1's (hash-join) competing plan shapes, so a combined flip could move a plan
for a reason neither arm alone explains.

## What the arm already is (no new derivation — this is a measurement task)

`hashjoin_pgtuplesizing.go`/`hashjoin_pggeometry.go` (built at R108, see
M0139-0007's recon inventory row 1) port PG's packed `HashJoinTuple` sizing
and `page_size()` (`postgres/src/backend/optimizer/path/costsize.c`, cited by
file:line in the source comments) as `pgHashGeometry`/`pgHashSpillPages`, and
`hashJoinCost` (`cost_funcs.go:796`) elects this currency for the
spill/batch decision only when `pgHashTupleSpillCostEnabled()` is true — the
exact "hand PG's formula PG's input" shape B2 requires. This arm is the
**batch-decision half** of the hash-join witness M0139-0007's task filing
named; the **build-entry-footprint half** (`GOOPG_NARROW_COST_INPUTS`) is
already landed default-ON since R128. Nothing about the mechanism changed in
this task — it is measurement only.

## Method

Single binary (`tmp/goopg-acceptance-bin`, built at HEAD, no code change: the
env var is read once at package-var init, so the same binary serves both the
off default and the on arm). Never touched the shared `:65432`/`:65433`/
`:65437`/`:65438` clusters' data directories.

- **TPC-H**: `scripts/tpch-estimate-audit-arm.sh`,
  `GOOPG_PG_HASH_TUPLE_SPILL_COST=1 PGSHAPED=1 PLAN_ONLY=1`, private port
  5592, `REFERENCE=""` + `-ref-port 65432 -ref-db tpch -ref-user postgres
  -ref-password postgres` (live PG capture). Artefacts:
  `analysis/m0139/m0139-0007a-hashspill-on-tpch.{txt,plans.txt,pg.plans.txt}`.
- **TPC-DS**: `scripts/lib/tpch-private-clone.sh`'s online
  `pg_basebackup -X fetch` snapshot of the SF0.25 goopg cluster
  (`bench/tpcds/runtime_goopg/data-sf025`, source port 65437) onto a private
  clone/port (5594), started with `GOOPG_PG_HASH_TUPLE_SPILL_COST=1` via
  `scripts/goopg-test-run.sh`, captured with `scripts/capture-tpcds.sh`,
  server stopped and the clone dir removed immediately after. The shared
  `:65437` cluster was never stopped/started/written. Artefact:
  `analysis/m0139/m0139-0007a-hashspill-on-tpcds-goopg.txt`.
- **Same-PG-reference / matching-stats-epoch controls**, identical technique
  to M0141-S2a-fix2's doc (isolates the goopg-only delta from PG-side
  ANALYZE sampling noise): TPC-H diffed against the already-committed
  `analysis/m0141/m0141-s2a-fix1-tpch.pg.plans.txt` (fix1's own PG capture —
  fix1 is what is actually landed at HEAD, so this is the correct baseline
  reference, not fix2's, since fix2's code was reverted); TPC-DS diffed
  against `analysis/m0141/m0141-s1-tpcds-pg.txt`, after confirming identical
  `stats-epoch: 5d4dc56356f3d676` across the arm's own capture and the
  baseline (`m0141-s2a-fix1-tpcds-goopg.txt`) it is compared to.
- `shape-delta.sh`
  (`docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh`)
  between the baseline (fix1, i.e. current HEAD default) and this arm's
  capture, on both corpora.

## Result: zero category movement on both corpora

### TPC-H — byte-identical to the HEAD-default baseline for all 22 queries

Baseline (`m0141-s2a-fix1-tpch.plans.txt`, i.e. current HEAD default) vs its
own PG reference:
```
PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
```
Arm (`GOOPG_PG_HASH_TUPLE_SPILL_COST=1`) vs the SAME PG reference:
```
PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
```
Identical in every field. `shape-delta.sh` confirms byte-for-byte, not just
matching category counts: `queries=22 text-changed=22 shape-changed=0` — the
`text-changed=22` is expected (`cost=`/`rows=`/`width=` annotations differ on
every query, since the currency substitution does change the *numbers* fed
into the spill-decision arithmetic even when it never flips the decision);
`shape-changed=0` means not one query's structural shape moved.

### TPC-DS — category counts identical; two queries show text/cost movement, both already `MISSING-NODE`

Baseline (`m0141-s2a-fix1-tpcds-goopg.txt`) vs PG (`m0141-s1-tpcds-pg.txt`),
same `stats-epoch: 5d4dc56356f3d676` on both goopg captures:
```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES: join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=69 sort-strategy=75 parallelism=84 qual-placement=21 rendering=23
```
Arm vs the SAME PG reference:
```
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES: join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=69 sort-strategy=75 parallelism=84 qual-placement=21 rendering=23
```
Identical totals and every category count. `shape-delta.sh` against the
baseline: `queries=99 text-changed=4 shape-changed=1` (`Q36 Q64 Q70 Q86`
text-changed — Q36/Q70/Q86 are the pre-existing `SKIP_QUERYGEN` `error=3` set,
unrelated to this arm; `Q64` is the one shape-changed query). Verified via
`--verbose` that Q64's verdict is `MISSING-NODE` both before and after this
arm (PG's plan uses **Incremental Sort**, a node kind goopg does not
implement — M0141-S7's dependency), so whatever the arm's cost movement did
to Q64 internally, it changes nothing the parity metric's category tags can
see: a `MISSING-NODE` verdict carries no category tag at all.

## Decision: HOLD — stays default-off

Per the task's own instruction ("decide adopt/hold per the plan-parity
metric"): zero category movement and zero match movement on both corpora,
with no offsetting argument of the `GOOPG_GATHER_PATHS` kind (that flip had a
genuine bug fix and a stated reason for the category rise being newly-visible
divergence, not a general license to promote on a null result). There is
nothing here to weigh a promotion against.

A second reason not to promote on a null result: AGENT.md's B2 section
explicitly flags this arm's *direction* as the risky one — "An absorption
that errs the other way [from R124's accepted direction] — planner charging
below what the executor needs — must carry that same execution gate before
it lands" (the TPC-H SF=1 acceptance arm, `scripts/tpch-acceptance-arm.sh`,
all 22 queries executed). PG's tuple representation (~22 B) is smaller than
goopg's real per-tuple `HashJoinTuple` allocation (`48*ncols+24+avgVar`), so
handing the *planner's spill decision* PG's smaller currency risks the
planner deciding "no spill" in a case where the executor's real allocation
still needs one — a correctness-adjacent memory risk, not merely a slower
plan. Since this measurement found no plan-parity upside anywhere in either
corpus to justify carrying that execution-time gate, it was not run this
task; the gate remains the required precondition for ever promoting this arm,
not something this HOLD decision needs to clear.

The arm is not deleted: it is a correctly-derived, already-tested PG-formula
port (not tuning) and remains available as a control for any future
investigation that needs the PG-equivalent hash-join spill currency. Per
AGENT.md's default-off-arm-cap norm, this measurement is its expiry event and
resolves it to **HOLD**, matching the disposition already reached for
`GOOPG_PG_SORT_RELATION_BYTES_COST` in the companion document.

## What every M0137–M0143 task report must contain (per AGENT.md)

- **Category movement**: zero, both corpora — full tables above.
- **shape-delta**: TPC-H `shape-changed=0`. TPC-DS `shape-changed=1` (Q64,
  a pre-existing `MISSING-NODE`/Incremental-Sort case both before and after).
- **Stats epoch**: TPC-DS — identical (`5d4dc56356f3d676`) between the arm's
  capture and the baseline it is compared to. TPC-H — the same-PG-reference
  control technique (diffing against the baseline's own already-committed PG
  capture) is used specifically so the live-vs-live epoch mismatch this
  milestone group's TPC-H protocol always has does not contaminate the
  comparison.
- **Seam-decline census**: N/A — this task changes no join-enumeration or
  subquery-decorrelation code path; it is a pure cost-arithmetic currency
  toggle inside an already-existing spill-decision branch.
- **Planning route**: unchanged — `hashJoinCost` is reached from the same
  existing hash-join costing call sites; this task added no new route and no
  code (measurement only, per the recon's own framing of M0139-0007a as
  "measure and adopt/hold", not "build").

## Verification

- `go build ./...` — unaffected; no source file changed by this task.
- Live measurement above: TPC-H `match` unchanged at 8/22, TPC-DS `match`
  floor held at 2/99 on both arms.
- Shared clusters (`:65432`, `:65433`, `:65437`, `:65438`) were read from
  (TPC-H) or cloned online without stopping (TPC-DS) but never
  stopped/started/rebuilt. All private artefacts (private clone data dirs,
  cgroup scopes, server logs) were removed after use; the private ports
  (5592, 5594) are free again.

## No production diff

Confirmed: `git status --short` shows no change under `internal/` from this
task. Both env-var arms measured here already existed at HEAD (R108); this
task only ran and recorded the measurement the recon named as missing.

## Resume points

- `GOOPG_PG_HASH_TUPLE_SPILL_COST` stays default-off, closed per this
  measurement — do not re-measure without new evidence (a corpus expansion,
  or a hash-join memory bug report that makes the executor-side risk
  concrete) rather than re-running the identical A/B.
- If a future task does want to promote it, the TPC-H SF=1 execution
  acceptance arm (`scripts/tpch-acceptance-arm.sh`) is the required
  precondition named above, not yet run because this measurement gave no
  reason to run it.
- M0139-0007b (Memoize's entry-byte currency) remains open and does not
  depend on this arm's disposition.
