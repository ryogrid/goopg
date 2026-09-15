# M0137-0018 — re-score `make ea-ratchet` at HEAD and settle its harness standing

Status: accepted (landed 2026-09-15)

## Task

`make ea-ratchet` (C-20a, `d0b4f96e4`) exists and runs, but no M0137–M0143
task cites it and its pinned findings had not been re-scored since
2026-09-07. Deliverable: re-score at HEAD, record the finding-identity delta
against the pinned baseline, and either add the instrument to the group's
declared gate table or file a ledger row saying why it does not belong there.

## Re-score at HEAD

```
EA_PORT=5534 EA_CG_UNIT=goopg-ea-ratchet make ea-ratchet
```

Private clone (`tmp/c20a/data-sf025`, cloned from the shared SF0.25 cluster,
read-only rsync) and private port 5534, under the cgroup cap
(`scripts/goopg-test-run.sh`), per the script's own isolation contract —
never touches `:65437`'s shared cluster or a peer's server.

- Commit: `4c5c13905`.
- Capture: 99/99 queries, non-zero psql rc: none.
- `queries: 99   nodes scored: 605   unmatched-in-PG: 244`
- `FINDINGS: 140` (bar: `qerr > max(10.0, PG_qerr * 2.0)`)
- `RATCHET vs …20260907/ea-baseline.txt`: `baseline findings: 178   current: 140`
  — 99 FIXED, 61 NEW → `EA-RATCHET: FAIL (61 new estimate finding(s))`.

## The delta is not a trustworthy regression signal — root cause

A raw 61-new/99-fixed delta looks like real estimator movement over eight
days of M0138/M0139/M0141/M0142 work landing in between. It is not (or not
reliably) that, and the reason is a corpus-scale mismatch the re-score
surfaced:

```
git log -p --follow -- scripts/estimate-parity-gate.sh | grep -n EA_SRC_DATA
-EA_SRC_DATA="${EA_SRC_DATA:-${SF05_GOOPG_DATA}}"
+EA_SRC_DATA="${EA_SRC_DATA:-${SF025_GOOPG_DATA}}"
```

`e2a50de40` ("bench(tpcds): SF0.5→SF0.25 dev-gate migration", 2026-09-11,
four days **after** the EA baseline was pinned) moved the whole TPC-DS
dev-gate stack — `EA_SRC_DATA`, and in the **same commit**, every
`bench/tpcds/plans-pg/Q*.txt` PG-reference fixture — from SF0.5 to SF0.25.
The goopg-side data and the PG-side comparison fixtures moved together and
stayed mutually consistent. The EA baseline
(`analysis/planner-refactor-take3/c20a-estimator-census-20260907/ea-baseline.txt`,
178 findings, pinned 2026-09-07 against the **old** SF0.5 corpus) did not
move with them.

Consequence: every `make ea-ratchet` invocation since 2026-09-11 — including
this task's own first re-score — has been comparing SF0.25 finding
identities against an SF0.5-era pinned set. The 61 "NEW" findings are not
necessarily new defects, and the 99 "FIXED" findings are not necessarily
real fixes; some unknown fraction of both is pure artefact of the scale
change (different absolute row counts change symmetric q-error even where
the estimator's relative behaviour is unchanged). The genuine-regression
fraction from the 2026-09-07→2026-09-15 window is not separable from the
scale-change artefact after the fact — the SF0.5 corpus itself was replaced,
not archived, by the migration — so it is recorded as unrecoverable in the
ledger row rather than guessed at.

Two more staleness spots found while tracing this: `Makefile`'s help text
and the `ea-ratchet` target's own comment block both still said "TPC-DS
SF0.5" and "~1 h", and the comment named "the SF0.5 gate's cluster on
65437" — but port 65437 is (and per `CLAUDE.md`'s table, always was after
the migration) the SF0.25 regression gate, not SF0.5. All three corrected.

## Disposition: re-pin, don't just report

The task's own wording allows "re-score and report the delta" as a
complete answer, but a baseline compared across an already-known corpus
change is not a working ratchet — it is a `FAIL` a person has to know this
whole story to distrust, forever, until someone repins it anyway. Re-pinning
is not tuning (B2's derive-before-measure / PG-equivalent-quantity rules
don't apply here — nothing about the *bar* or the *formula* changed, only
the corpus the already-correct formula runs over) and it is the documented,
supported operation (`make ea-ratchet-repin`, or direct `parity.py
--write-baseline` reusing an existing capture, which is what this task
did to avoid a second ~7-minute capture run):

```
python3 scripts/estimate-parity/parity.py tmp/c20a/ea-capture.txt \
    bench/tpcds/plans-pg --json …/ea-findings-20260915.json \
    --write-baseline …/ea-baseline.txt
# -> baseline written: …/ea-baseline.txt (140 entries)
```

New baseline directory (same naming convention as the 20260907 one):
`analysis/planner-refactor-take3/c20a-estimator-census-20260915/` —
`ea-baseline.txt` (140 entries), `ea-capture-20260915.txt` (+`.header`,
commit `4c5c13905`), `ea-findings-20260915.json`. `scripts/
estimate-parity-gate.sh`'s `EA_BASELINE` default now points at it. A future
`make ea-ratchet` run ratchets cleanly against a same-scale baseline.

The old 20260907 baseline and its capture are left in place (not deleted) —
they remain the historical record C-20a's original landing produced, and
the ledger row explains why they are no longer the live comparison target.

## Harness standing: added to AGENT.md's measurement section

`make ea-ratchet` measures something neither of the group's existing tables
can see: `scripts/pg-plan-parity-diff.py` normalises `rows=` out entirely
before comparing (K50), and the values gates check result correctness, not
cardinality error. Since Q2 ("reproduce PG's estimates") and M0138/M0142's
estimator work are exactly the tasks whose claims are about estimate
accuracy, the instrument earns a named place in the harness doc rather than
staying an uncited afterthought. Added as a third table in AGENT.md
§"Plan-parity harness" → "Measurement — pipeline, ports, and what the
metric cannot see", directly after the corpus/capture/score table, stating:
what it measures, that it is not required by every task (cite it when the
claim is about estimate accuracy), the `EA_CAPTURE=<file>` no-server
re-score mode, and — so this exact trap is not rediscovered — the
re-pin-on-corpus-change rule with this task's finding as the worked example.

Not added to the "values gates" table: `make ea-ratchet` fails on
plan-node-level q-error, not on row-count correctness of a query result —
different failure semantics, would misrepresent both tables to conflate
them.

## What was not done (scope boundary)

- **No individual finding in the 61 NEW / 99 FIXED lists was triaged.**
  This task establishes a trustworthy baseline going forward; it does not
  investigate any specific estimator. A future M0138/M0142-territory task
  that wants to know "did estimator X get better or worse since
  2026-09-07" cannot answer that from EA alone — see the ledger row's
  `deferred` column.
- **No production planner/executor/catalog code touched** — this is a
  harness-standing task (one script default, three doc-comment corrections,
  one re-pinned baseline, one ledger row, this design doc) per the M0137
  charter.

## Plan-parity harness report items (AGENT.md §"What every M0137–M0143 task
report must contain")

1. Category movement (`CATEGORIES:`/`CATEGORIES-EXCL-MATCH:`): N/A — this
   task runs `pg-plan-parity-diff.py`'s sibling instrument (`ea-ratchet`'s
   `parity.py`), not `pg-plan-parity-diff.py` itself; no plan-shape corpus
   capture this task.
2. `shape-delta` counts: N/A — same reason.
3. Stats epoch: `GOOPG_ANALYZE_SEED=20260905` (the script's own default,
   unchanged by this task), single ANALYZE pass before the 99-query capture,
   commit `4c5c13905`. No values sweep intervened.
4. Seam-decline census: N/A — no `GOOPG_PGSHAPED_DP_TRACE=1` capture this
   task.
5. Planning route: N/A — not applicable to an estimate-accuracy capture (no
   join-search-seam question in scope).
