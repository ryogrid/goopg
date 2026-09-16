Task: M0142-0008a-3i-plumbing-b2 — landed §28.4's dropped-equijoin fix
standalone (design doc §29), ahead of the harder predp.go reachability
wiring. Production code change (first real code in this loop, not
design-only): committed this loop.

Files this loop:
- internal/optimizer/joinsearchseam.go: extractSearchLeaves's Semi/Anti arm
  (around the existing rebaseChainQual call, ~line 1160) now folds
  j.LeftKey/j.RightKey into an explicit OpEq conjunct ANDed onto
  j.Predicate BEFORE the rebase, gated on `j.LeftKey != nil && j.RightKey
  != nil`. Mirrors createplanjoin.go's joinInputs.joinPredicate idiom.
- internal/optimizer/semiantichain_test.go: new test
  `TestExtractSearchLeaves_AdmitSemiAnti_FoldsKeyEquijoinIntoPred` — bare
  correlated EXISTS (Predicate naturally nil) proves the fold; asserts
  exactly one conjunct, structurally `(LeftKey = RightKey)` by pointer
  identity, accepted by semiAntiOnQualsOK.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §29 added.
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with this loop's
  landing note + narrowed Next-step.
- docs/design/README.md: m0142-0008a-1 index row extended with §28/§29
  summaries. Also REPAIRED a pre-existing defect found while editing this
  exact row: the row's markdown table cell was missing its closing `|`
  and ended mid-sentence on a dangling unclosed `**§19.5` fragment (some
  earlier loop's edit was cut off mid-write) — table rendering was broken
  for this row across however many prior loops touched it. Confirmed via
  raw byte inspection (`repr()` of the line's tail) before and after, and
  confirmed neighboring rows' correct `" |\n"` termination for contrast.

Key symbols:
- `extractSearchLeaves` (joinsearchseam.go:1093), its Semi/Anti arm
  (~1131-1194, the `admitSemiAnti` branch).
- `combineAnd` (pushdown.go:530) — used to build the fold, not a new
  helper.
- `fillOneJoinHashKeys` (join_hash_keys.go:190) — read to confirm
  HashKeys[0] is seeded from LeftKey/RightKey directly (not derived from
  Predicate), which is what makes the gap real for any future consumer
  reading `pred` alone rather than `HashKeys`/`Residual()`.
- `unnestExistsExpr` (unnest.go:4772-4785) — re-confirmed `join.Predicate
  = joinPredicate` (residuals only) and `join.LeftKey/RightKey = outerKey/
  innerKey` are two SEPARATE assignments; the equality never becomes a
  Predicate conjunct at construction time.

Findings this loop:
1. The ordering constraint in §28.4/§28.5 ("land the equijoin fix in the
   SAME loop as the reachability wiring, or admitSemiAnti=true becomes a
   live wrong-rows bug") is about EXPOSURE, not coding order — the fix is
   fully inert as long as the one production call site
   (joinsearchseam.go's tryPGShapedJoinSearch) keeps passing
   admitSemiAnti=false, which it still does. Splitting the fix out into
   its own bounded, independently-testable loop is therefore safe and
   matches the project's own "prove inert before wiring" precedent
   (-3i-plumbing-b1).
2. The key expressions (LeftKey/RightKey) live in the SAME local
   coordinate space j.Predicate already uses (confirmed live:
   RightKey.Index = outerWidth + params[0].SubCol.Index, a LOCAL
   merged-schema offset) — so folding them in BEFORE the existing
   `rebaseChainQual` call (rather than rebasing each piece separately) is
   correct and is what the landed code does.
3. `fillOneJoinHashKeys` seeds `HashKeys[0]` directly from
   `(LeftKey,RightKey)`, never from Predicate — confirming the single-key
   case never needed Predicate to carry the equality for CORRECT
   execution today; the gap is confined to hypothetical future consumers
   treating `pred`/`Predicate` as "the whole condition" on its own.

Next step: wire the predp.go descend-loop extension (predp.go:96-101's
current hard-bail on a non-Semi/Anti `*Join` inside
runJoinSearchBelowPinned's descend loop) so SOME call reaches the
post-unnest tree with admitSemiAnti=true — this is the ONLY remaining
piece of -3i-plumbing-b2's scope now (the dropped-equijoin exposure risk
is closed). This is real production DP-routing code — higher blast radius
than this loop's change; scope it carefully before coding (re-read
predp.go's full descend loop and runJoinSearchBelowPinned's splice/
reresolve tail, not just the 96-101 hard-bail site) rather than coding it
blind, per the practice card's M0072-0002 hang precedent. Likely also
still depends on enough of M0142-0008c-3c/-3d/-4 (bushy-DP admission of a
Semi/Anti pair) existing — not re-examined this loop, per §26.3's forward
note.

Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 fully lands —
both still blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel.

Alternatives if -3i-plumbing-b2 is judged not worth continuing
immediately: M0141-S2b-2 (base join/scan Pathlist-across-search-boundary
surgery — still needs its own scoping pass). M0142-0003i/0003k remain
BLOCKED on a human-authorized shared `:65433` cluster reload — do not
attempt. Unrelated, NOT to be picked up under this banner unless
explicitly re-prioritized: `internal/parser`'s `GroupedJoinUnaliased`
AST-drift gap (pre-existing, ~60 failing test functions — still the ONLY
package failing in the units precommit gate, unchanged by this loop).

Gates run this loop: `go build ./...` clean. `go vet
./internal/optimizer/...` clean. `go test ./internal/optimizer/...` full
pass (including the new test). TPC-DS SF0.25 sweep: PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0, PLAN-SHAPE same=99 changed=0 (zero production
behavior change, as expected). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: only the pre-existing unrelated
internal/parser GroupedJoinUnaliased failure; internal/optimizer itself
passes. TPC-H spotcheck: SKIPPED (standing M0142-0003k blocker, tpch data
still not reloaded — unrelated to this change). `make
ralph-state-guard`: found and auto-repaired the same benign prior-loop
clean-exit marker as several prior loops; consistent after repair.

In-flight: none. Nothing outstanding from this loop once committed.
