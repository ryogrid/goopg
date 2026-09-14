# M0137-0012 — file ledger rows for the four unowned carry-overs (B6, B8, B10, O15)

Status: accepted
Date: 2026-09-15
Milestone: M0137 — Parity measurement harness and instrument repair

## Problem

`METHODOLOGY3/02-open-problems.md`'s "Blockers" table names four findings that
have each already been diagnosed to a mechanism, a file/line and a measured
consequence, but none of them has ever been written into
`.ralph/deferral_ledger.md`:

- **B6** — no Memoize on the NL probe path R59 repriced (DS Q72 4s → 320s
  TIMEOUT), carried unfixed through R60/R61/R62, "the status channel treats it
  as non-blocking by policy; the problem is real and unowned."
- **B8** — `indexProbeCostMultiplier = 2.0` puts plan parity and wall-clock
  performance in direct conflict; "the real fix… is a cross-layer programme
  that has never been scoped."
- **B10** — `corr = 0` fallback prices every index scan with no correlation
  slot at `max_IO_cost`; "root-causes §7 priority 4; persistence half CLOSED
  (K27), fallback half open."
- **O15** — `*Gather` crossing is still deliberately excluded from
  `pushConjunctTraced` (named inline at `02-open-problems.md` C2), the same
  class of defect as R56's Q78 qual-placement loss, which a values-green sweep
  cannot detect.

Per `AGENT.md`'s plan-parity harness ("Way of working"), a **recon task** is
measurement plus a design note plus, where something is deferred, a ledger
row — with **no production change**. This task is exactly that: it makes each
finding *visible and resumable* in the ledger (mechanism + file/line + resume
point) rather than leaving it locatable only by knowing to grep
`METHODOLOGY3/`.

## What landed

Four new rows appended to `.ralph/deferral_ledger.md`, one per finding
(`m0137-0012-b6-no-memoize-nl-probe`, `m0137-0012-b8-indexprobe-multiplier-parity-vs-wallclock`,
`m0137-0012-b10-corr-zero-fallback-max-io-cost`,
`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced`). Each row's
`deferred` and `resume point` columns were re-derived against the tree at
HEAD, not copied verbatim from the round doc, catching two places where the
current code is more specific than the finding's original text:

- **B6**: confirmed goopg *does* have a general Memoize producer
  (`internal/optimizer/joinpathsmemoize.go`, `internal/executor/operators_memoize.go`,
  `GOOPG_MEMOIZE` default-on) and that the NL-index-join path already calls
  `maybeAttachMemoize` (`internal/optimizer/nl_index_join.go:127`) and
  `getMemoizePath` (`internal/optimizer/joinpathsnli.go:313,498`) in its
  generic sweep — so the gap is scoped to why the *specific* probe path R59
  repriced does not reach a Memoize-wrapped inner, not to Memoize's absence
  altogether. The resume point names the exact call sites to start from.
- **B8**: located `indexProbeCostMultiplier` at
  `internal/optimizer/cost_funcs.go:1052` (a package-level variable, not a
  GUC) and confirmed the sibling K73 item (`Join.FromOuterReduction`) is a
  real, still-present field referenced by name in `AGENT.md`/`TODO.md`.
- **B10**: located `indexCorrelationFor` at
  `internal/optimizer/costindex.go:479-496` (guard at `:480-482`, returns 0
  when the leading column has no correlation statistic) and confirmed via the
  function's own doc comment that the fallback is intentional PG-parity
  behaviour (PG's own default for the uncorrelated case, `selfuncs.c:7237`) —
  the open half is the fallback *firing in practice* on bench data that has
  never been re-ANALYZEd since the correlation-persistence fix (K27), plus
  R30's synthesised index geometry (`estimateIndexGeometry`).
- **O15**: read `pushConjunctTraced` in full
  (`internal/optimizer/inner_join_qual_pushdown.go:340-556`) and confirmed its
  `switch x := n.(type)` has cases for `*Filter`, `*Project` and `*Join`
  only — a `*Gather` node falls through to the terminal-target check
  (`innerJoinPushEligibleInput`) and the push declines. The resume point
  names the exact function and switch to extend, and notes the fix is
  properly sequenced *after* M0137-0010's qual-placement census (which
  detects the class of loss) rather than attempted blind here.

No `TODO.md` round directory was edited (frozen history, per the harness).
No `rNNN-*` directory was created (new artefacts belong under `analysis/m01NN/`
per the harness; this task produced no raw artefact, only the ledger rows and
this note).

## What did not change

No production code. No plan/value sweep was run — none of the four findings'
mechanisms were touched, so there is nothing new to measure; each ledger
row's own `resume point` is what a future implementation task will need to
run (DS Q72 timing for B6, TPC-H Q7/Q4 timing for B8, a corpus-wide sweep
after re-ANALYZE for B10, M0137-0010's census output for O15).

## Verification

- `go build ./...` unaffected (doc + ledger-only change).
- `.ralph/deferral_ledger.md` table format matches the existing 2160-row
  convention (`| status | date | task-id | landed | deferred | resume point |
  why |`), verified by inspection against the file's own header and the most
  recent prior rows (`take2-R81-q4-sort-blocked-on-inputs` et al.).
- `RALPH_PRECOMMIT_SCOPE=units` gate: same pre-existing `internal/parser`
  AST-drift failures already recorded under M0137-0009's working-set notes
  and today's nightly-triage filing (AI-20260914-235643-001/003/013/014) —
  confirmed unrelated to this doc-and-ledger-only change and out of scope
  here.
