# M0137-0017 — capture TPC-H plans in BOTH serial and parallel modes

Status: accepted (landed 2026-09-15)

## Context

`AGENT.md` §"Plan-parity harness" and the M0137-0003 baseline-capture
procedure both record the same fact: `estimate-audit`'s `-serial` flag
defaults **true**, setting `max_parallel_workers_per_gather = 0` on **both**
engines before a single query runs. Every M0137–M0143 TPC-H capture to date —
including the headline **6/22** the harness quotes — is therefore a
serial-control-arm statement. It is valid as a like-for-like comparison, but
it cannot see a parallelism-only divergence, and the `parallelism` category in
`pg-plan-parity-diff.py`'s nine-category breakdown has read **0** in every
prior report for exactly that reason: the category is measured *out* of the
corpus, not solved (ledger row `take3-plan-capture-is-serial-only`).

The banner named this task **more important than its size suggests**: it is
the only M0137 item that unblocks a whole scoring dimension, not one query.
The prerequisite the ledger row names — a parallel-mode PG reference — did
not exist (`bench/tpch/plans-pg/` is serial-only and, per K9, not even a
parity target). Building it was explicitly part of this task's scope.

## Decision — run both arms, same tool, same stats epoch

`estimate-audit -plan-only` already accepts `-serial=false`; no code change
was needed. Both arms were captured back-to-back against the live TPC-H
clusters (goopg `:65433`, PG reference `:65432`), same session-warmed
statistics both times (both artefacts stamp the identical
`# stats-epoch: 253df6b1d97f9f5b`, so the A/B isolates the `-serial` flag and
nothing else moved underneath it):

```bash
go build -o /tmp/estimate-audit ./cmd/estimate-audit
PGPASSWORD=tpch /tmp/estimate-audit -plan-only \
  -label m0137-0017-serial -out analysis/m0137 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres \
  -serial=true
PGPASSWORD=tpch /tmp/estimate-audit -plan-only \
  -label m0137-0017-parallel -out analysis/m0137 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres \
  -serial=false
python3 scripts/pg-plan-parity-diff.py analysis/m0137/m0137-0017-serial.plans.txt   analysis/m0137/m0137-0017-serial.pg.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0137/m0137-0017-parallel.plans.txt analysis/m0137/m0137-0017-parallel.pg.plans.txt
```

`-ref-port` with `-serial=false` is what actually builds the parallel-mode PG
baseline: the tool captures the PG reference through the same function with
the same `-serial` treatment as the goopg side (M0137-0003 §2), so passing
`-serial=false` produces, for the first time, a **PG 18.3 parallel-plan
reference** (`m0137-0017-parallel.pg.plans.txt`) alongside the goopg parallel
capture. Both are committed under `analysis/m0137/`, per the group's "Way of
working" rule that raw artefacts land in the same commit as the design doc
that cites them, not `/tmp`.

## Result — the category is now scoreable, and it is not clean

| arm | match | shapediff | missingnode | `parallelism` category |
|---|---|---|---|---|
| `-serial` (existing control) | 6/22 | 14 | 2 | 0 (measured out) |
| `-serial=false` (new) | **2/22** | 20 | 0 | **16/22** |

Full category counts, parallel arm: `join-order=17 join-method=12
scan-type=9 parameterisation=4 aggregation-strategy=10 sort-strategy=13
parallelism=16 qual-placement=3 rendering=1`.

Two things this measurement establishes, neither of which the serial-only
harness could show before:

1. **`parallelism` is the single largest category** in the parallel-mode
   diff (16 of 22 queries), larger than every category except `join-order`.
   The serial-only headline's `parallelism=0` was hiding this, not solving
   it, exactly as the ledger row predicted.
2. **The serial-vs-parallel match count itself drops 6→2.** The serial arm's
   six matches are Q1, Q6, Q10, Q11, Q14, Q15a-VIEWBODY; only **Q6 and Q11**
   survive into the parallel arm. Q1, Q10, Q14 and Q15a-VIEWBODY flip to
   `SHAPE-DIFF` once parallel workers are allowed. This is evidence, not yet
   a diagnosis: it says the serial control arm is over-stating parity by a
   factor of 3x on this corpus, not which mechanism causes any individual
   query's parallel-mode divergence.

This task is a **measurement task** (per AGENT.md's "recon" carve-out: no
production code changes, `go build ./...` untouched). It deliberately does
**not** attempt to close any of the 16 `parallelism` divergences — that is a
new, larger body of work the measurement exists to make visible, not
something a capture-and-diff task can also fix in the same loop.

## Follow-up

Filed as `.ralph/fix_plan.md` **M0137-0019** (per the group's two-artefact
completion rule: this ledger row plus that task, not the ledger row alone):
triage the 16 parallel-mode `SHAPE-DIFF`/category hits in
`analysis/m0137/m0137-0017-parallel.*` and decide, per query, whether the
divergence is a plan-selection defect (same class M0139/M0141/M0142 already
work) or a Gather-placement/costing gap specific to the parallel path
(M0140's `GOOPG_GATHER_PATHS=all` territory). Do not re-run the capture for
that triage — these artefacts are already committed and stats-epoch-pinned.

## What was not done (scope boundary)

- No attempt to raise the parallel-mode match count. That is M0137-0019 and
  whatever it spawns.
- No change to which arm the M0137 headline quotes. The **serial** capture
  stays the harness's primary reported number (matches every existing
  report and every other M0137–M0143 task's baseline); the parallel capture
  is a second, now-available measurement, not a replacement metric — the
  task's job was to make the category scoreable, not to pick a new headline.
- `bench/tpch/plans-pg/` was not touched or extended. Per K9 it is not a
  parity target; `analysis/m0137/m0137-0017-parallel.pg.plans.txt` is the
  parallel-mode PG reference going forward, in the same location every other
  M0137-series baseline already lives.

## Verification

- Both captures completed with `rc=0`, `unparsed=0` on both diffs.
- Stats-epoch fingerprints identical across the serial/parallel pair
  (`253df6b1d97f9f5b`), confirmed by inspecting each artefact's header line
  (`scripts/check-stats-epoch.sh` is for same-corpus same-engine A/Bs; this
  is a manual equality check appropriate to a goopg-vs-goopg-input capture
  pair, not the goopg-vs-PG comparison §4a's tool targets).
- No production Go code touched; `-serial=false` is an existing, already-
  tested flag (`cmd/estimate-audit/main.go:293`).
