# M0145-0002 — dual-pipeline measurement harness

Status: landed (code) / gates in this doc's §"Measurement".
Task: `.ralph/fix_plan.md` M0145-0002. Parent: none (banner-ordered;
M0145-0001's IR contract is the design this harness measures progress
toward). Kind: impl.

## What this is

The transition from the node-tree pipeline to the jointree-first pipeline
(M0145-0003 … 0007, cutover 0008) must be measurable **while the legacy
pipeline stays the value-gated one** — AGENT.md §"Plan-parity harness" G8.
This task builds exactly the three harness pieces the milestone doc asks
for, and nothing more: the jointree pipeline itself is **not** started here.

1. The env knob at the `planSelectWithSettings` dispatch.
2. A `pg-plan-parity-diff.py`-compatible capture recipe for the knob arm.
3. A per-stage divergence report (which of the six divergences a record
   belongs to), so progress is visible per-slice.

## The knob

`internal/optimizer/jointreepipeline.go`:

```go
var jointreePipeline = jointreePipelineFromEnv(os.Getenv("GOOPG_JOINTREE_PIPELINE"))

func jointreePipelineFromEnv(v string) bool { return v == "1" }
```

Fail-closed, `oneRelSearchFromEnv`'s polarity: only the literal `1` arms
the new pipeline; unset, `0` and anything unrecognised stay on the legacy
path. A typo must never silently route planning off the arm the value
gates cover.

### Dispatch site

`planSelectWithSettings` (`internal/optimizer/planner.go`) is now the thin
dispatcher; its former body moved verbatim to `planSelectLegacyPipeline`:

```go
func planSelectWithSettings(...) (Node, error) {
    if jointreePipeline {
        return planSelectJointreePipeline(s, cat, plannerSet, scope)
    }
    return planSelectLegacyPipeline(s, cat, plannerSet, scope)
}
```

The dispatch sits at the one function every planning scope funnels
through — top-level statements (`Plan`, :63), subqueries (:225), set-op
branches (:1088/:1119/:1238), `INSERT … SELECT` (:12476),
`planSelectWithParent` (:15701) and every CTE body (`with.go`:215/279/351/
413) — so the knob scopes per-`SelectStmt` the way PG's `subquery_planner`
recurses per `Query` (postgres/src/backend/optimizer/plan/planner.c:383,
:822). One knob, every scope, no second selection mechanism (G8).

### The delegating stub

`planSelectJointreePipeline` today is:

```go
return planSelectLegacyPipeline(s, cat, plannerSet, scope)
```

Deliberate, not a placeholder-shaped shortcut: M0145-0002 is the
measurement harness only. A knob-on capture is therefore a **byte-for-byte
A/A baseline** — the differ sees identical plans on both arms and any
movement a later task measures is attributable to that task's code, never
to the harness. `TestJointreePipelineDispatchDelegates` pins the identity
in-process; the A/A captures in §"Measurement" pin it end-to-end. Stage
landings (0003 sublink pull-up → 0007 unified lowering) replace the stub
body, keeping this entry point.

### Flag provenance

`GOOPG_JOINTREE_PIPELINE` is a plan-shaping flag, so it is registered in
`internal/optimizer/flaglabels.go` (`flagResolvedState` +
`flagProvenanceOrder`, appended last so old artefact lines still diff
cleanly) and `scripts/planner-flags.env` is regenerated. Consequence that
matters for the transition: **every** capture-stamped artefact now names
the pipeline arm in its `# planner-flags:` line — a knob-on capture that
does not name the flag cannot say which pipeline it measured, which is
exactly the mis-stamp class M0127-P5.9-q built the provenance system to
kill.

C5: a new default-off arm names the task and measurement that decide it —
**M0145-0008's cutover**, measured by the knob-arm captures this recipe
produces (promote to default-and-delete; HOLD is not an outcome).

## Capture recipe

`scripts/jointree-parity-capture.sh <corpus> <label> <outdir>` produces
`=== Qn` plan files the differ consumes directly, plus the diff and the
per-class report:

```
<outdir>/<label>.plans.txt      goopg arm (knob state = $JOINTREE)
<outdir>/<label>-pg.plans.txt   PG 18.3 reference arm
<outdir>/<label>-diff.txt       pg-plan-parity-diff.py output
<outdir>/<label>-class.txt      pg-plan-divergence-class.py report
```

`JOINTREE` (default 1) is exported into the measured server's
environment; `JOINTREE=0` is a control arm through identical machinery.

- **`tpch`** — delegates bring-up to `tpch-estimate-audit-arm.sh`
  (`JOINTREE=<n> PLAN_ONLY=1 … -serial=false`, the canonical
  parallel-mode arm from M0144-0001), then captures PG itself with a bare
  `estimate-audit -plan-only -serial=false -warm-stats=false -port 65432`.
  Never `-ref-port`: that flag makes the tool ANALYZE the reference, which
  R1 forbids (recipe corrected in m0137-0003 §2 / m0144-0001). The arm
  script gained `export GOOPG_JOINTREE_PIPELINE="${JOINTREE:-0}"` —
  explicit-arm convention, same reason as PGSHAPED: an unset flag stops
  being a well-defined arm the day the default flips, and this default IS
  scheduled to flip at 0008.
- **`tpcds-sf025` / `tpcds-sf1`** — offline `cp -a` clone of
  `data-sf025`/`data` (source must be down; `postmaster.pid` present →
  refuse; a `*.HOLD` sibling → refuse), `55xx` private port under
  `goopg-test-run.sh`'s cgroup cap, `capture-tpcds.sh` on both arms with
  `GOOPG_EXPECT_BIN_SHA256` pinning the built binary. PG arm is the
  read-only `:65438` reference (`tpcds025`/`tpcds` per corpus). Pins come
  from `bench/tpcds/env_tpcds.sh` (`GOOPG_ANALYZE_SEED`, `GOMEMLIMIT`,
  `GOGC`) — the same source of truth the SF0.25 gate sweep uses, so a
  clone is measured under the same sampler seed as the gate.

EXPLAIN-only end to end (G8): nothing executes a query body, so a plan
the executor cannot yet run is recorded as evidence — never suppressed,
never gated on.

## Per-stage divergence report

`scripts/pg-plan-divergence-class.py <goopg> <pg>` — same operands as
`pg-plan-first-divergence.py`, whose census it reuses verbatim (one
mutually-exclusive first-divergence record per divergent query). Each
record is mapped to the class the record belongs to — the six
medium-level divergences of the plan-flow doc, which are also the M0145
stage boundaries:

| class | rule (on the diverging pair) | surface |
|---|---|---|
| D2-unionall | Append/MergeAppend/SetOp/HashSetOp | M0145-0004 |
| D1-sublink | SubPlan/InitPlan/Subquery Scan/Semi/Anti | M0145-0003 |
| D6-cte | CTE Scan/WorkTable Scan/CTE hdr/Recursive Union | deferred — boundary kept |
| D3-partialpath | Gather/Parallel/Partial/Finalize/Materialize, cat=parallelism | executor substrate, outside M0145 |
| D4-upperrel | Sort/Limit/Unique/WindowAgg/LockRows/ProjectSet/Result/agg kinds, cat=aggregation-strategy/sort-strategy | M0145-0006 |
| D5-narrowing | — | closed (M0144-0003c), 0 by construction |
| jointree-search | residual: no marker | M0145-0005 single-pass DP |
| verdict | error/timeout/unparsed | not a plan record |

Two semantics decisions worth recording:

- **Only the diverging pair is matched, not the parent.** The parent is
  the agreed context; the children are the decision point. A join-order
  record *under* a matched `CTE` header (TPC-DS Q14) or a scan-type record
  under a matched Semi join is `jointree-search`, not the inline or
  pull-up gap — the first cut of the classifier included the parent and
  mislabelled exactly this case.
- **Precedence follows pipeline stage, earliest first** (D2 → D1 → D6 →
  D3 → D4): when a record carries two markers (e.g. Q51's
  `Subquery Scan` vs `CTE web_v1`), it is attributed to the earliest stage
  that could change it.

Current-corpus reading (existing artefacts, not this task's captures —
they validate the instrument):

```
TPC-H (m0144-0001, parallel):  match=1 D1=1 D2=0 D3=11 D4=6 D5=0 D6=0
                               jointree-search=3 verdict=0
TPC-DS SF1 (m0144-0008):       match=1 D1=6 D2=3 D3=32 D4=33 D5=0 D6=11
                               jointree-search=10 verdict=3
```

The picture matches the milestone's premise: the searched-jointree
residual is small (3–10); the mass sits in D4/D3 (upper-rel + partial
substrate) with D6's CTE boundary the largest single deferred class on
TPC-DS.

## Measurement (AGENT.md D2)

All gates ran against the **default** pipeline (G8); the change is a
planner change so the full set ran:

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | PASS (optimizer 3.1s incl. the two new pins) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=33 canonical |
| `tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` (96 PASS / 3 oracle-SKIP); plan channel `same=99 changed=0`; ran under `FORCE=1` against the live nightly batch — wall times not valid, values are |

Floor captures (default arm, `JOINTREE=0` through the new recipe):

```
TPC-H parallel (m0145-0002-tpch-off):
PLAN-PARITY: queries=22 match=1 shapediff=21 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES: join-order=18 join-method=11 scan-type=13 parameterisation=7 aggregation-strategy=4 sort-strategy=9 parallelism=19 qual-placement=5 rendering=2
CATEGORIES-EXCL-MATCH: join-order=18 join-method=11 scan-type=13 parameterisation=7 aggregation-strategy=4 sort-strategy=9 parallelism=19 qual-placement=5 rendering=2

TPC-DS SF0.25 (m0145-0002-sf025-off):
PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
CATEGORIES: join-order=91 join-method=68 scan-type=61 parameterisation=55 aggregation-strategy=43 sort-strategy=61 parallelism=85 qual-placement=26 rendering=26
CATEGORIES-EXCL-MATCH: join-order=91 join-method=68 scan-type=61 parameterisation=55 aggregation-strategy=43 sort-strategy=61 parallelism=85 qual-placement=26 rendering=26
```

Floors held: TPC-DS match=2 = **Q9 + Q41** (the pinned floor queries);
TPC-H parallel match=1 = **Q6**, the M0144-0001 re-pinned baseline — no
current match lost.

Knob-arm A/A evidence (`JOINTREE=1` vs `JOINTREE=0`, same recipe, same
HEAD `dc9ca699…`):

- **TPC-DS SF0.25**: `.plans.txt` files differ in exactly 6 hunks, all
  provenance — label line, `GOOPG_JOINTREE_PIPELINE=0|1` flag stamp,
  pid/inode, psql scratch paths. Plan bodies byte-identical.
- **TPC-H**: 0 shape-difference lines after stripping `(cost=…)`
  literals; the residual is reservoir-sampler variance between two
  independently-ANALYZEd private clones (the TPC-H arm does not pin
  `GOOPG_ANALYZE_SEED` — existing captures share that noise profile, so
  it is left unpinned; the differ's verdicts are cost-insensitive and
  identical on both arms).
- Stats epochs: sf025 goopg `5d4dc56356f3d676`, PG `a574afdef103f449`
  (the estimate-audit `.plans.txt` format does not stamp an epoch).
- Planning route: unchanged — PG-shaped DP on both arms (the delegating
  stub routes through it).
- Wall times: N/A — captures are `EXPLAIN`-only; the FORCE=1 sweep's
  seconds are contaminated by the nightly and not cited.

Divergence-class census on the knob arm (the report's own baseline —
what the stages will drain):

```
TPC-DS SF0.25: D1=8 D2=4 D3=31 D4=25 D5=0 D6=11 jointree-search=15 verdict=3
TPC-H:         D1=1 D2=0 D3=12 D4=3 D5=0 D6=0 jointree-search=5 verdict=0
```

Movement: **none** — the delegating stub changes no plan by
construction (A/A evidence above); the task's product is the instrument,
not a number.

Artefacts: `analysis/m0145/m0145-0002-{sf025,tpch}-{off,on}{,-pg}.plans.txt`,
`*-diff.txt`, `*-class.txt`.

## Retirement

- `GOOPG_JOINTREE_PIPELINE` is retired by M0145-0008's cutover (G8);
  nothing else may add a second pipeline-selection mechanism.
- `planSelectLegacyPipeline` is deleted at 0008 when the legacy pipeline
  is removed.
- The delegating stub dies piecemeal: each stage (0003→0007) replaces its
  slice of the delegation.
