# M0142-0012-verify — run the plan-parity floor-measurement suite M0142-0012 deferred

Status: accepted (landed 2026-09-15)

## Task

M0142-0012 (`a5a1bd492`) landed a cardinality-estimation fix for the
decomposed-NLI `Join{Lateral: true}` shape, verified via correctness gates
only (`tpch-spotcheck`, the SF0.25 value/checksum sweep, `make ea-ratchet`).
Per `AGENT.md` §"Plan-parity harness", none of those gates score this
milestone group's own headline metric (TPC-H/TPC-DS `match` count against
live PG 18.3). This task runs that measurement — a **recon task**: no
production code changed.

## Method

Bootstrapped per `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`,
following M0137-0017's serial+parallel precedent for TPC-H and M0142-0001's
precedent for TPC-DS. All four bench clusters were already up except goopg
TPC-DS SF0.25 (`:65437`, down at loop start — restarted via
`bench/tpcds/server.sh start sf025`, capped, no `--reset`, no data change).

```bash
go build -o /tmp/estimate-audit-0012verify ./cmd/estimate-audit

PGPASSWORD=tpch /tmp/estimate-audit-0012verify -plan-only \
  -label m0142-0012verify-tpch-serial -out analysis/m0142 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres \
  -serial=true
PGPASSWORD=tpch /tmp/estimate-audit-0012verify -plan-only \
  -label m0142-0012verify-tpch-parallel -out analysis/m0142 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres \
  -serial=false

scripts/capture-tpcds.sh 65437 postgres postgres \
  analysis/m0142/m0142-0012verify-tpcds-goopg.txt "M0142-0012-verify goopg SF0.25" \
  bench/tpcds/runtime_goopg/data-sf025
scripts/capture-tpcds.sh 65438 tpcds025 ryo \
  analysis/m0142/m0142-0012verify-tpcds-pg.txt "M0142-0012-verify PG18.3 SF0.25 reference"

python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpch-serial.plans.txt   analysis/m0142/m0142-0012verify-tpch-serial.pg.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpch-parallel.plans.txt analysis/m0142/m0142-0012verify-tpch-parallel.pg.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpcds-goopg.txt         analysis/m0142/m0142-0012verify-tpcds-pg.txt
```

**Shape-delta (item 2 of the report contract)** needs a *self*-diff (goopg
before M0142-0012 vs goopg after), which a straight text diff cannot give
honestly — every EXPLAIN line carries a numeric cost/row estimate, so a
cardinality-only change rewrites nearly every line's *text* even when the
join order and node types (the actual "shape") never move
(confirmed: a byte-level per-query diff of the TPC-DS captures came back
97/99 "changed", which is the wrong instrument — see note below). Feeding
`pg-plan-parity-diff.py` an old-goopg capture as its "pg" argument and the
new-goopg capture as its "goopg" argument reuses its own cost-blind
shape/category extraction to diff goopg against itself:

```bash
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpch-serial.plans.txt  analysis/m0142/m0142-0001-tpch-corpus.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpcds-goopg.txt         analysis/m0142/m0142-0001-tpcds-goopg.txt
```

`analysis/m0142/m0142-0001-tpch-corpus.plans.txt` /
`m0142-0001-tpcds-goopg.txt` are M0142-0001's captures, taken **before**
M0142-0012 landed, at the identical `stats-epoch` (see below) — the only
variable between the two captures in each pair is the M0142-0012 commit
itself.

## Results — the headline did not move; the parallel-mode measurement reproduces exactly

| corpus / arm | match (this task) | match (pre-M0142-0012 baseline) |
|---|---|---|
| TPC-H `-serial` | **6/22** | 6/22 (M0142-0001, same day) |
| TPC-H `-serial=false` (parallel) | **2/22** | 2/22 (M0137-0017) |
| TPC-DS SF0.25 | **2/99** (Q9, Q41 — floor intact) | 2/99 (M0142-0001) |

TPC-H parallel-arm categories are byte-identical to M0137-0017's own numbers
(`join-order=17 join-method=12 scan-type=9 parameterisation=4
aggregation-strategy=10 sort-strategy=13 parallelism=16 qual-placement=3
rendering=1`) — M0142-0012 moved zero parallel-mode TPC-H category tags
either.

TPC-DS categories, per the mandatory two-line report (raw / excl-match):

| | join-order | join-method | scan-type | parameterisation | agg-strategy | sort-strategy | parallelism | qual-placement | rendering |
|---|---|---|---|---|---|---|---|---|---|
| pre-0012 (M0142-0001) | 90 | 69 | 61 | 45 | 70 | 76 | 85 | 21 | 23 |
| post-0012 (this task) | 91 | 68 | 61 | 45 | 69 | 75 | 83 | 21 | 23 |

`PLAN-PARITY` aggregate line is **identical** pre/post:
`queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0`
(the 3 `ERROR` queries — Q36/Q70/Q86 — are the known dsqgen-artefact queries
that fail to parse on PG too, per `tpcds-plan-diff.py`'s own header comment;
not new). `CATEGORIES` and `CATEGORIES-EXCL-MATCH` are the same two lines
(this diff has zero `MATCH`-tagged category hits in either run).

**Verdict: M0142-0012 did not move the group's headline metric on either
corpus, in either TPC-H arm.** Q9 and Q41 — the canonical TPC-DS floor
(`M0137-0004`) — are confirmed intact and are not among the queries whose
shape moved (below).

## Results — shape-delta: 17 TPC-DS plans moved sideways, 0 TPC-H plans moved

The self-diff (goopg-before vs goopg-after, both pinned to `stats-epoch
5d4dc56356f3d676` for TPC-DS and `253df6b1d97f9f5b` for TPC-H — confirmed
identical in all three captures of each corpus, so no ANALYZE drift
contaminates the comparison):

- **TPC-H**: `PLAN-PARITY: queries=22 match=22 shapediff=0` — M0142-0012's
  cardinality fix touched 6/21 TPC-H queries' cost values (per
  M0142-0012a's blast-radius recon) but flipped **zero** join/node shapes.
- **TPC-DS**: `PLAN-PARITY: queries=99 match=79 shapediff=17 missingnode=3
  error=0` (this run's "pg" argument is itself a goopg capture, so its own
  3 dsqgen-artefact rows come out `error=0`/absorbed differently — read only
  the `match`/`shapediff` split). **17 queries changed plan shape**: Q4, Q6,
  Q11, Q25, Q29, Q31, Q34, Q45, Q54, Q56, Q60, Q64, Q72, Q73, Q78, Q79, Q88.
  This is far fewer than M0142-0012a's 69/99 call-site-hit count — most hits
  were on DP-search candidates that lost anyway, or changed a candidate's
  cost without changing which candidate the search picked (M0142-0012a
  flagged exactly this as its own scope boundary: raw call-site hits are not
  "hits that survive into the final plan").

None of the 17 became a new PG match, and neither existing match (Q9, Q41)
is among the 17 — consistent with the flat aggregate/category counts above.
Reading the two small per-category deltas together (`join-order` 90→91,
`join-method`/`aggregation-strategy`/`sort-strategy`/`parallelism` each -1 or
-2) says the 17 shape changes are a **near-exact wash**: some queries picked
up a category tag they did not have before, a similar number dropped one,
and the net movement is within noise of zero. This is squarely the "moved
plans sideways" case `AGENT.md`'s report contract calls out by name (item 2):
non-zero shape-delta, no headline or category movement.

## Stats epoch / provenance (item 3 of the report contract)

- TPC-H (`-serial` and `-serial=false`, this task and M0142-0001):
  `stats-epoch 253df6b1d97f9f5b` in all three captures — no ANALYZE drift.
- TPC-DS (SF0.25 goopg, this task and M0142-0001): `stats-epoch
  5d4dc56356f3d676` in both — no ANALYZE drift. (PG reference captures do
  not carry this goopg-internal fingerprint; PG's own statistics were not
  touched between the two TPC-DS captures — no `ANALYZE`/reload ran on
  `:65438` in between.)
- No values sweep intervened between the pre- and post-M0142-0012 captures,
  so no OFF-baseline re-take was needed.

## Items 4–5 of the report contract

- **Seam-decline census**: N/A — this task ran no `GOOPG_PGSHAPED_DP_TRACE`
  capture (it is a plan-parity re-score, not a join-search seam
  investigation).
- **Planning route**: N/A — not traced this task; the corpus captures do not
  distinguish `tryPGShapedJoinSearch` from the legacy constructor per query.

## Follow-up

Filed as `.ralph/fix_plan.md` **M0142-0014**: triage the 17 TPC-DS
plan-shape changes above (do they trade one non-matching shape for another
non-matching shape of equal quality, or does any of them represent a
regression the flat aggregate count is masking — K50's "aggregate hides
per-query movement" warning applies here exactly as it did to M0142-0001's
corpus-level Q9/Q96 read). One `.ralph/deferral_ledger.md` row records this
(resume point: the 17-query list above, `pg-plan-parity-diff.py`'s
per-query `SHAPE-DIFF [...]` category tags already computed in this task's
`analysis/m0142/` artefacts — no re-capture needed).

## What was not done (scope boundary)

- No attempt to close any of the 17 shape changes or raise the match count.
  That is M0142-0014.
- No change to which arm the M0137/M0142 headline quotes (TPC-H `-serial`
  stays primary, per M0137-0017's own scope note).
- `analysis/m0142/m0142-0012verify-*` and `m0142-0001-*` artefacts are
  committed alongside this doc per the group's "raw artefacts land with the
  design doc" rule.

## Verification

- All `pg-plan-parity-diff.py` invocations: `unparsed=0` on every diff.
- Stats-epoch fingerprints confirmed identical within each corpus's
  pre/post pair (see above).
- No production Go code touched — pure recon; `go build ./...` unaffected.
