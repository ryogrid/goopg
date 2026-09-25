# R10 results — NOT LANDED: the flip needs 15 tests adjudicated first

*Round 10 of `../TODO.md`. Design: `DESIGN.md` (committed `86a702647`).
Attempted and **deliberately reverted** 2026-09-08. No code shipped.*

## 1. Verdict

The one-line default change is correct and the design's argument for it
stands (§2 of `DESIGN.md`: PG has no Gather post-pass, so leaving goopg
on one while trying to reproduce PG's output is reaching for the same
answer by a different method). **It is not landed**, because flipping it
fails **15 tests** that pin the pre-flip world, and each needs
individual adjudication that this round could not give them honestly.

The tree is left green and unchanged. The design is committed; the
implementation is one line, recorded below.

## 2. What flipping breaks

`gatherPathModeFromEnv`'s default `off` → `all`, then
`go test ./internal/optimizer/ ./internal/executor/`:

15 failures, listed in `failing-tests-under-the-flip.txt`. They are not
a uniform class, which is exactly why they cannot be bulk-updated:

- **Explicit stage pins.** `TestPartialPathIsNeverTheFinalPath` fails on
  *"join rel 0x3 has partial paths before C-19d"* — it asserts a
  historical staging invariant ("Join rels have no partial paths yet")
  that this round supersedes on purpose. Updating it is right.
- **Seam shape pins.** Six `TestPGShapedSeam*` /
  `TestSeamPlans*` tests pin join-search output trees. A changed tree
  here may be the intended effect *or* a real regression, and telling
  those apart means reading each expected tree against PG.
- **`TestC19fPathModelGatherExecutesAsAParallelHashJoin`** is the most
  interesting: it is the test written *for* this mechanism. Its failure
  under the flip is a signal worth understanding before anything else,
  not a fixture to re-baseline.
- **`TestFlagProvenanceEnvIsGenerated`** is a generated-artefact check —
  the flag's provenance stamp records the default, so it needs
  regenerating, not editing.

## 3. Why it was reverted rather than pushed through

Rubber-stamping 15 expected-output tests to make a change land is the
precise failure mode this workstream has spent nine rounds cataloguing:
a claim accepted because it was convenient rather than checked. Two of
those rounds (R3, R6) hit a single superseded test each, and in both
cases reading the test properly changed what shipped — R6's caught an
over-eager first cut that sorted before *every* window.

At fifteen, with at least three distinct classes and one test that
exists to guard the very mechanism being enabled, the probability that
bulk-updating hides a real defect is high, and the cost of getting it
wrong is a silently wrong plan on both benchmark corpora.

So: not landed, nothing half-done, and the next round starts from a
green tree with the list in hand.

## 4. What is already known, so the next round need not re-derive it

- The change is one line (`gatherPathModeFromEnv`'s `default:` arm),
  plus regenerating the flag-provenance artefact.
- R8's probe (`GOOPG_GATHER_PATHS=all`, taken **before** R9's jointype
  filter, so quarantined per `DESIGN.md` §3) showed
  `Parallel Hash Join` 0 → 19 on TPC-H and 0 → 132 on TPC-DS (PG: 9 and
  139), TPC-H `parallelism` 18 → 15, `join-method` 12 → 11,
  `qual-placement` 7 → 5, `aggregation-strategy` 10 → **14**, match
  unchanged at 2.
- The R9 filter means those counts will differ; they must be
  re-measured, not carried.
- `DESIGN.md` §7's acceptance rule stands unchanged, including that a
  parity-neutral result is an **accept**.

## 5. Filed

- **R11 — adjudicate the 15**, in three groups (stage pins, seam shape
  pins, generated artefacts), reading each expected tree against PG
  rather than against the new output. Then land the flip and run
  `DESIGN.md` §5's gates.
