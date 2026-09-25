# M0139-0004 — re-measure the "duplicate hash build map" premise

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: recon (measurement only; no production diff, per the plan-parity
harness's recon-task carve-out in `AGENT.md` §"Plan-parity harness")

## Goal

`docs/design/not_ralph/minimize_datum/05-work-estimate.md` §1.5 (quoting
`02-goopg-current-representation.md` §3) prices "delete the duplicate build
map" as a **one-commit, ~2× peak-build-memory win**, on the premise that
`internal/executor/operators_join_agg.go`'s hash-join build maintains
**both** `lazyHash map[string][]Row` and `lazyIntHash map[int64][]Row` for
the whole build on the int-key path. Verify that premise against the tree at
HEAD before scheduling any code change against it.

## Method

Static read of the build path plus the pre-existing unit-test suite that
already pins the same invariants (no new test needed — the wiring this task
needed to confirm was already gated at HEAD by a prior loop; this task's
contribution is connecting that existing coverage to §1.5's claim and
correcting the doc):

1. `buildLazyHashTable` (`operators_join_agg.go:598-716`) decides the lane
   **once**, before either build loop runs, from the plan's *static* key
   types: `o.lazyHashIsInt = !o.multiKey() && o.plan.HashKeysAreInt64()`
   (`:651`). A multi-column key never sets it (`:646-650`); the CTID
   preservation path forces it false unconditionally (`:699`).
2. `presizeLazyHash` (`:822-855`) allocates **exactly one** of the two maps,
   selected by that same flag (`:851` vs `:855`), and returns immediately if
   either map already exists (`:823`) — it can extend an existing table, not
   create a second one. Pinned by `join_presize_test.go`'s
   `TestPresizeLazyHashChoosesTheLaneTheBuildCommittedTo` (both subtests
   assert the *other* map stays `nil`) and
   `TestPresizeLazyHashKeepsAnExistingTable`.
3. `lazyHashInsertDatum` (`:1238-1254`) — the only insert path both build
   loops call per row — inserts into `lazyIntHash` **and returns**, or falls
   through to `lazyHash`; the two branches are mutually exclusive per call.
   `lazyHashInsertKeyed` (`:1285-1309`, the spill-reload counterpart) has the
   identical shape.
4. The only place both maps are ever non-nil at once is `demoteIntHash`
   (`:1325-1338`) — the safety net for a build where the plan's static
   "these keys are int64" promise turns out false for some row. It migrates
   every already-inserted `lazyIntHash` entry into `lazyHash`, **then** sets
   `o.lazyIntHash = nil` (`:1337`) and `o.lazyHashIsInt = false` (`:1326`)
   permanently for the rest of that build. Pinned by
   `join_presize_test.go`'s `TestPresizedIntTableStillDemotes` and
   `dense_build_cut2_test.go`'s demotion tests: after either, `lazyIntHash`
   is `nil` and every row lives in `lazyHash` alone.
5. The parallel/cooperative build path (`parallelBuildLazyHashTable`,
   `parallel_hash_build.go:588-800`) is not a second implementation of the
   lane logic — its consumer loop calls the same `presizeLazyHash` and
   `buildLoopLeft`/`buildLoopRight` as the serial path (`:709-724`), so it
   inherits the same mutual exclusion rather than re-deriving it.
6. A multi-column key never reaches `lazyIntHash` at all — pinned
   separately by `join_composite_key_test.go`'s assertion that a
   multi-key build leaves `o.lazyIntHash == nil`.

## What HEAD actually does (measured, not assumed)

For the routine int-key build (`HashKeysAreInt64()` true, single key column):
exactly **one** map is ever allocated, sized, and populated —
`presizeLazyHash` picks it before the first row and every insert commits to
it. There is no window in the normal path where both tables hold live rows.

The **only** double-population is the demotion fallback (step 4): a
transient, partial copy — bounded by however many rows were inserted before
the first non-int64-representable key arrived, not the full build — that
exists only inside `demoteIntHash`'s own copy loop and is gone (`lazyIntHash
= nil`) before the function returns. This is a defensive path for a static
type promise turning out wrong mid-build, not the routine int-key lane 05
§1.5 describes, and it does not produce a standing ~2× on any build that
completes without a key-type surprise (the overwhelmingly common case: the
planner already checked the key columns are int-typed before setting
`HashKeysAreInt64()`).

## Verdict

**05 §1.5's premise is refuted at HEAD.** `lazyHash` and `lazyIntHash` are
not "both maintained" for the build's duration on the int-key path; they are
two mutually-exclusive representations of the same table, chosen once from
static plan information, with no steady-state double retention. "Delete the
duplicate build map" prices a bug that does not exist in the current tree —
there is nothing to delete; the two fields already behave as a single
logical table dispatched by `o.lazyHashIsInt`.

Per the task's own instruction ("if the premise is refuted, the deliverable
is a ledger row recording the refutation and a correction to
`minimize_datum/05` §1.5, not a code change"): §1.5's table row is corrected
in place (see the doc's own change) to mark the claim refuted, cite this
design doc, and remove it from the "three cheaper interventions" comparison
— it was never a free ~2× win available at HEAD, so it does not change
`minimize_datum`'s cost/benefit picture. This task does not decide or
advance `minimize_datum` (still NOT APPROVED TO START); it removes one
stale input from a document that feeds a future owner decision.

`take2 07 §6`, cited by 02 §3 as recording the same claim "separately," is a
`plan_parity_fix_take2/` round document; per the plan-parity harness's
browsing restriction this task did not open it (no task line names it and it
is not reached via the mechanism index) — the correction here is scoped to
`minimize_datum/05` §1.5, the document this task was actually assigned to
verify. If a future reader finds `07 §6` repeats the same stale claim, that
is a separate, cheap follow-up in the same shape as this one.

## Gates

Recon task: no production diff. `go build ./...` clean (confirmed
unaffected — no `.go` file touched). The unit tests cited above
(`TestPresizeLazyHashChoosesTheLaneTheBuildCommittedTo`,
`TestPresizeLazyHashKeepsAnExistingTable`, `TestPresizedIntTableStillDemotes`,
`join_composite_key_test.go`'s multi-key assertion, `dense_build_cut2_test.go`'s
demotion tests) were already green at HEAD before this task — read, not
re-derived, per the recon-task discipline of eliminating a hypothesis without
scoping a new campaign around it. No `.ralph/deferral_ledger.md` row: nothing
PG-incompatible surfaced — this is a correction to an internal cost-estimate
document, not a PG-compatibility gap, so per the project's ledger scope
(`.ralph/PROMPT.md` §"Deferral Ledger") it does not belong there; the
refutation is instead recorded here and in `05-work-estimate.md` §1.5
directly, matching the M0139-S3 precedent for non-PG findings.
