Task: M0142-0008a-3i-plumbing-b1 — INERT scaffold: walk extension + SJInfo
rebuild (design doc §22.4, narrowed by §23.4). DONE and committed-ready this
loop (not yet committed as of writing this baton — see Next step).

Files this loop:
- internal/optimizer/joinsearchseam.go: `extractSearchLeaves` gained a new
  `admitSemiAnti bool` parameter and a Semi/Anti-admission arm (recurses
  `j.Left`, appends `j.Right` as ONE opaque leaf, builds a
  `semiAntiChainLink`, rebuilds the matched join's `j.SJInfo`'s
  Syn/MinLefthand/Righthand from real leaf-index bits — replacing
  `existsUnnestSJInfo`'s throwaway synL=1/synR=2 placeholder). The one
  production call site (`tryPGShapedJoinSearch`) now reads
  `extractSearchLeaves(chain, false)`, discarding the new `semiAnti` return
  with `_` — behavior byte-identical to before.
- internal/optimizer/semiantichain_test.go: 2 new tests exercising the REAL
  function directly (not the m0142_0008a_3i_plumbing_probe_test.go throwaway
  copy): `TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo`
  (admitSemiAnti=true path, full link+SJInfo-rebuild assertions) and
  `TestExtractSearchLeaves_AdmitSemiAntiFalse_UnchangedFromProduction`
  (admitSemiAnti=false path proves inertness directly).
- internal/optimizer/m0142_0008a_3i_verify_probe_test.go: updated its one
  `extractSearchLeaves(j.Right)` call site to the new 2-arg/6-return
  signature (`extractSearchLeaves(j.Right, false)`), no logic change.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §23 added
  (what landed, verification) + §23.4 (narrowing correction, see Findings).
- docs/design/README.md: m0142-0008a-1 index row extended with §23/§23.4
  summary.
- .ralph/fix_plan.md: `-3i-plumbing-b1` flipped to `[x]` with corrected
  scope (walk extension + SJInfo rebuild only); `-3i-plumbing-b2`'s
  description updated to ALSO own `predp.go`'s pass-through (moved out of
  -b1 — see Findings item 2 below).
- .ralph/deferral_ledger.md: `M0142-0008a-3i-plumbing-b1` row appended.

Key symbols: `extractSearchLeaves` (joinsearchseam.go, now
`func(node Node, admitSemiAnti bool) (scans, widths, onQuals, outer,
semiAnti []semiAntiChainLink, ok)`), the new admission arm inserted right
after `j, isJoin := n.(*Join)` and before the generic non-admitted-type
branch, `semiAntiChainLink`/`semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
(landed by -3i-plumbing-a, now have a real producer for the first time,
still gated), `existsUnnestSJInfo` (unnest.go:4398, its synL=1/synR=2
placeholder is now overwritten in place at admission time when
admitSemiAnti=true), `predp.go`'s descend loop (predp.go:96-101, the hard
bail this loop did NOT touch — see Findings).

Findings (read before picking up -3i-plumbing-b2):
1. Items 3 (walk extension) and 4 (SJInfo rebuild) landed cleanly with the
   same "callable but never called" inert shape `-3i-plumbing-a`/
   `-0008c-1/-2/-3a` already used: a literal `false` at the one production
   call site, not a package-level flag, so any reader can grep-confirm
   inertness without tracing further.
2. Item 2 (predp.go's pass-through, `predp.go:96-101`) does NOT have that
   same property and was deliberately NOT landed this loop, despite being
   listed in -3i-plumbing-b1's original filing text. Its bail already only
   fires on a shape the eligibility pre-check *guarantees* cannot occur
   today (no non-Semi/Anti `*Join` is ever placed on the pinned spine under
   current EXISTS/IN-only engagement scope) — so "generalizing" it would be
   dead code provable only by the SAME "cannot happen under current
   construction" argument the existing comment already makes, not by an
   independently-checkable condition, and there is no PG-faithful fixture
   that would exercise it. It also turns out to only have real meaning once
   -b2's cutover actually routes a wider tree through `predp.go` (today
   nothing needs "pass through" — Semi/Anti nodes on the spine are already
   walked correctly). Re-filed into `-3i-plumbing-b2`'s scope in both
   fix_plan.md and design doc §23.4 rather than split out further.
3. Both new production-code tests build their fixture on the same
   Q69-witness-class SQL as the earlier probe/§21 tests; the fixture is the
   FINAL planned tree (post hash-method-selection), so `j.Predicate` comes
   back nil and the test reconstructs it from `j.LeftKey`/`j.RightKey` —
   this mirrors `TestSemiAntiLinksHaveSJInfos_MatchesRealSJInfo`'s existing
   pattern and is a TEST-FIXTURE concern only; production's real entry
   point (predp.go's pre-search origChain) always has `Predicate` populated
   pre-method-selection (already established by §21's probe).

Next step: per banner order (M0137-M0143 group), pick up
**M0142-0008a-3i-plumbing-b2** (fix_plan.md, design doc §22.3/§22.4/§23.4).
Scope is now: (a) predp.go's pass-through (moved here this loop — extend the
descend loop's hard bail at predp.go:96-101 to pass through non-Semi/Anti
`*Join` nodes, now meaningful because of (c) below); (b) item 6 — give the
Semi/Anti RHS subtree a `ctx.bindings`/`ctx.joinlist` representation by
plumbing a `*resolveContext` through `unnestSubqueriesInPlan`/
`unnestExistsExpr`, with its OWN scoping pass into their other internal call
sites first (IN-family unnesting etc. — not investigated by any loop so
far); (c) the actual cutover — feed the full spine+origChain tree to
`tryJoinSearch`, flip the production call site's `admitSemiAnti` to `true`;
(d) retire `predp.go`'s splice-and-reresolve ONLY for statement shapes
empirically proven (TPC-DS sweep + a dedicated EXISTS/NOT-EXISTS regression
set) handled end-to-end by the new path, keeping the old splice as fallback
for everything else. This is very likely still not a one-loop task — start
with (b)'s own scoping pass (the `*resolveContext` plumbing's blast radius
through `unnestSubqueriesInPlan`'s other internal call sites is explicitly
"not investigated") before attempting (a)/(c)/(d).
Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 lands — both still
blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel (fix_plan.md
lines ~3891, ~3899).

Alternatives if -3i-plumbing-b2 is judged not worth continuing immediately:
M0141-S2b-2 (base join/scan Pathlist-across-search-boundary surgery — still
needs its own scoping pass). M0142-0003i/0003k remain BLOCKED on a
human-authorized shared `:65433` cluster reload — do not attempt.
Unrelated, NOT to be picked up under this banner unless explicitly
re-prioritized: `internal/parser`'s `GroupedJoinUnaliased` AST-drift gap
(pre-existing, traced to `dc91bd6b7`, ~60 failing test functions,
reconfirmed unchanged across 5 consecutive loops now, including this one via
the precommit gate run below).

Gates run this loop: `go build ./...` clean. `go vet
./internal/optimizer/...` clean. `go test ./internal/optimizer/...` full
pass (including the 2 new tests). TPC-DS SF0.25 sweep (private
`GOOPG_BIN=tmp/goopg-sf025-bin`, foreground): `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: same=99 changed=0`.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: only the same
pre-existing, unrelated `internal/parser` `GroupedJoinUnaliased` AST-drift
failure the prior 4 loops already found; `internal/optimizer` passes.
`make ralph-state-guard`: found and auto-repaired a stale
status/progress-completed inconsistency (previous loop's clean-exit marker,
not a real project-completion signal) — consistent after repair.

In-flight: none. Committed as b68e29e92 (pgbench smoke pre-commit gate
PASS). Nothing outstanding from this loop.
