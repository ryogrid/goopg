# M0145-0008 legacy-deletion slice 1: the fire-set gate compares HEAD with the staged tree

Status: task M0145-0008 CLOSED 2026-09-24 (legacy-deletion slice 4, `m0145-0008-del4-pgshaped-knob-and-retirement-audit.md`). Earlier: **LANDED 2026-09-24** (harness only; no engine code). Task:
`.ralph/fix_plan.md` M0145-0008 (the cutover; this is the first of its
legacy-deletion slices). Earlier docs: `m0145-0008-cutover-flip.md`,
`m0145-0021-fireset-derivation.md`, `m0145-0021a-fireset-gate-enforcement.md`,
`m0145-0021b-tpch-fireset-lane.md`. Evidence: `analysis/m0145/m0145-0008-del1/`.

## Why this slice goes first

Before this slice, `scripts/tpcds-fireset-gate.sh` built one engine from the
worktree and ran it twice: baseline `GOOPG_JOINTREE_PIPELINE=0` (legacy),
candidate `=1` (jointree). That was the right A/B while the jointree pipeline
was the thing under test. After the cutover it measured the wrong thing:

- The fire set was "plans where legacy and jointree differ" (25 ids at the
  flip), not "plans the staged change moves". A change that moved a plan on
  the shipped pipeline to one legacy also happened to produce dropped out of
  the fire set, and a change that moved nothing still executed 25 ids at two
  scales.
- Deleting the knob (a later slice) would make both arms identical. The
  gate would then derive zero fires on every run and PASS having compared
  nothing. The flip doc already named this: "the HEAD-vs-staged redesign
  belongs to the deletion slice".

So the redesign must land before the knob is deleted, or G9 (AGENT.md) goes
vacuous without anyone noticing.

## What changed

`scripts/tpcds-fireset-gate.sh`:

- **Two engines.** The candidate is built from the checkout
  (`tmp/fireset-bin/<label>-candidate`). The gate stamp's `dirty_code` rule
  already forces the checkout to equal the index for `internal cmd go.mod
  go.sum`, so this is the staged code. The baseline is built from
  `BASELINE_REV` (default `HEAD`): `git archive <sha> -- go.mod go.sum cmd
  internal` into `tmp/fireset-src/<label>-baseline`, then `go build`. That
  path set is `cmd/goopg`'s whole in-module closure, `go:embed` files
  included (checked with `go list -deps`), and the generated parser is
  checked in. Every capture and execution runs with `GOOPG_BIN=<arm image>
  NO_BUILD=1`, so the arms differ only by source tree.
- **Both arms run the shipped pipeline.** `BASELINE_JOINTREE` and
  `CANDIDATE_JOINTREE` now default to 1. They stay settable while the knob
  exists.
- **Resume safety.** The resolved baseline sha goes to
  `<outdir>/<label>-baseline-rev.txt`, and `FIRESET_RESUME=1` rebuilds from
  that sha, not from a HEAD that may have moved between batches.
- **A/A refusal.** `BASELINE_REV=worktree` runs the candidate's own image
  on both arms (for env-file or knob experiments). If the knob values match
  and neither arm has an env file, that is an A/A, and the gate exits 2.
- **TPC-H lane.** Under `NO_BUILD=1`, `tpch-acceptance-arm.sh` needs its
  runner image, so the gate builds `RUNNER_BIN` once from the worktree when
  `tpch` is in `CORPORA`. The runner is a client, so one image serves both
  arms.
- **Clone cleanup.** The four private TPC-DS clones a corpus creates (two
  capture clones, two execution clones, 3.3 GB each at SF1) are deleted
  unless they have a live postmaster. The capture clones go as soon as the
  fire set is derived, so peak disk at SF1 is two clones, not four.
  `FIRESET_KEEP_CLONES=1` keeps them. Before this, the gate never deleted
  anything: this loop found 140 stale execution clones (279 GB), and
  removed the 132 that had no live server and were older than an hour.

`jointree-parity-capture.sh` and the arm scripts are unchanged. They already
honoured a caller's `GOOPG_BIN` and `NO_BUILD`.

## Verification

- **A/A (HEAD vs worktree, this slice's own tree: script-only changes, so
  the engine code is identical)**, both default corpora: `fires=none` at
  SF0.25 and at SF1. Both arms' PG parity lines are identical (SF0.25
  `match=4`, SF1 `match=6`). The two images have different sha256 (Go
  embeds the build path), so this is two independent builds agreeing, not
  one binary compared with itself. Stamp: PASS.
- **Non-vacuity**: `BASELINE_REV=bb90e51a4^` (before M0146-0002e, which
  moved TPC-DS plans), `CORPORA=tpcds-sf025`: `changed (8): Q6 Q8 Q12 Q14
  Q20 Q54 Q58 Q98`. All 8 ids executed on both arms,
  `introduced=none unchanged=none missing=none`, rc 0.
- After both runs, none of their clone directories remained.

## What a fire set now means

The fire set is the plans the change moves on the shipped pipeline. A
change that moves no plan gets `fires=none` and a PASS, with no execution.
That PASS is correct, not vacuous: plan capture is the derivation, and the
A/A above shows the capture is deterministic at both scales. Before, every
run executed the 25 legacy-vs-jointree ids.

## Remaining legacy-deletion slices (unchanged order)

1. Delete `planSelectLegacyPipeline`, the `jointree` parameter of
   `planSelectImpl`, and the other `jointreePipeline` branch sites
   (`joinsearchseam.go`, `collapse.go`, `specialjoin.go`, planner.go
   appendrel), with the legacy-only machinery they gate (pre-DP unnest,
   pinned spine) and the group-L tests pinned through `SetJointreePipeline("0")`.
2. Retire the knob with `flagProvenanceRetired` (the M0145-0018 precedent),
   then drop the fire-set knob variables and `tpcds-sf025-regression.sh`'s
   legacy control pass (`GOOPG_JOINTREE_PIPELINE=0`).
3. The seam guards on M0145-0001's retired list.
