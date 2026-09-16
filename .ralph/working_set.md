Task: M0142-0008a-3i-plumbing-b2 — step (iii) LANDED (design doc §34,
commit pending). Item is now fully DONE (all three steps (i)/(ii)/(iii)).
No in-flight work; next loop picks a fresh item per fix_plan.md's banner.

Files this loop:
- internal/optimizer/joinsearchseam.go: flipped
  `extractSearchLeaves(chain, false)` to `extractSearchLeaves(chain, true)`
  at the ONE production call site inside `tryPGShapedJoinSearch` (was
  `:309`). Updated 3 nearby stale comments that used to justify why
  `semiAnti` stays empty in production — they now explain the real split
  (Phase A's `origChain` call can never contain a Semi/Anti node; only
  Phase B's `spineJoins[0]` call, reached post-unnest, can).
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §34
  (4 subsections) documenting the flip, verification, the Q78 finding, and
  why Q10/Q35 did not converge (expected — needs M0142-0008c's separate
  create_unique_path mechanism).
- docs/design/README.md: m0142-0008a-1 index row extended with §34 summary.
- .ralph/fix_plan.md: -3i-plumbing-b2 marked [x] DONE with the step (iii)
  landing note; M0142-0008c-3c/-3d marked unblocked (their "blocked on
  -3i-plumbing-b2" notes updated to "unblocked 2026-09-16, not yet picked
  up").
- .ralph/deferral_ledger.md: new row (task-id m0142-0008a-3i-plumbing-b2)
  for Q78's implausibly-cheap Nested-Loop-with-Filter cost estimate —
  correctness-neutral, deferred as a cost-model scoping question for
  whoever next touches the Semi/Anti path-building arms.

Key symbols: `extractSearchLeaves`/`tryPGShapedJoinSearch`
(joinsearchseam.go); `admitSemiAnti` (now `true` literal, was `false`).

Hypothesis/Findings: CONFIRMED — Phase B is genuinely live in production
now (first non-empty plan-shape self-diff this milestone: same=98
changed=1, Q78). Zero correctness regressions (TPC-DS SF0.25 PASS=96
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0). Q10/Q35 unchanged as expected
per §16/§19's independent finding that they need M0142-0008c's
create_unique_path mechanism (-3c/-3d/-4 not yet landed), not this flip.
Q78's antijoins come from PG's LEFT JOIN...WHERE col IS NULL strength
reduction, not literal EXISTS/NOT EXISTS SQL — confirms
`extractSearchLeaves`'s JoinType-keyed admission is correctly provenance-
agnostic. One new, deferred, NOT-yet-diagnosed finding: Q78's `ws` CTE plan
lost a Memoize-wrapped indexed date_dim probe in favor of a Nested Loop
with a bare post-filter whose EXPLAIN cost (944.11) looks too cheap for
that shape — flagged, not chased (practice card: measure end-to-end, don't
chase a single cost number without its own scoping pass).

Next step: pick the next item per fix_plan.md's `## Current Priority`
banner (plan-parity group M0137-M0143 still first). Two natural
candidates now unblocked by this loop: M0142-0008c-3c (hash-join
unique-ify substitution) or -3d (merge/partial-NL unique-ify substitution)
— both need their own scoping pass first (neither has been live-traced
yet, only recon-sized). Separately, Q78's cost-estimate anomaly (this
loop's deferral-ledger row) is available as a smaller side quest if the
banner allows discretion between equally-ranked items.

Gates run this loop: `go build ./...` clean; `go vet ./internal/optimizer/...`
clean; full `go test ./internal/optimizer/...` pass (unchanged test count —
no test needed updating since none pinned the literal's value); TPC-DS
SF0.25 sweep PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3,
PLAN-SHAPE same=98 changed=1 (Q78, verified correctness-neutral via
checksums); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
shows only the pre-existing unrelated `internal/parser`
`GroupedJoinUnaliased` failure (internal/optimizer itself `ok`); pre-commit
hook's pgbench smoke will run automatically on commit (not run standalone
this loop — relying on the git hook per AGENT.md); `make ralph-state-guard`
found and auto-repaired the same benign prior-loop clean-exit marker prior
loops have also seen, consistent after repair.

In-flight: none. This loop's diff is about to be committed in the same
turn — nothing abandoned mid-gate.
