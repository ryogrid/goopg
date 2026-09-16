Task: M0142-0008a-3i-plumbing-b2 — step (i) reachability, §30's (a)/(b)
decision settled as a third option (c) (design doc §31). Design-only loop,
no production code touched.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §31 added.
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with §31's decision
  + corrected next-step (code §31.3, not the predp.go reachability change).
- docs/design/README.md: m0142-0008a-1 index row extended with a §31
  summary sentence.

Key symbols:
- `tryPGShapedJoinSearch` (joinsearchseam.go:216) — preamble checks at
  `:243` (nrels bound), `:261` (nprefix := jl.nrels()), `:309`
  (`extractSearchLeaves(chain, false)` — 5th return `semiAnti` is
  discarded via `_` today), `:314` leaf-count decline, `:333` per-leaf
  offset-agreement loop, `:347` spine-offset-disagreement check.
- `extractSearchLeaves` (joinsearchseam.go:1093) — its `semiAnti
  []semiAntiChainLink` return already carries `.rhs RelSet`, a single-bit
  set marking each synthetic leaf's `scans` index (`loRight :=
  len(scans)` at `:1152`, one `scans = append(scans, j.Right)` at `:1156`,
  `hiRight := len(scans)` at `:1159` — always exactly one bit).
- `buildLeafSpans` (joinsearchseam.go:1316) — landed by §27; places real
  leaves' spans verbatim (skipping synthetic widths) and synthetic
  leaves' spans out-of-band, appended AFTER the real total width,
  *indexed by walk position, not partitioned real-then-synthetic*. This
  is the source of §31's newly-found third bug (see below).

Finding this loop (from live tracing, not re-derivation):
§30 framed the choice as (a) widen ctx.bindings/ctx.joinlist vs (b) a
parallel/duplicate entry point bypassing the preamble. Neither is needed:
`semiAnti` already gives the caller everything required to make the
existing three preamble checks synthetic-leaf-aware with purely local
arithmetic — no ctx.bindings mutation (avoids §24.2's ~24-consumer-site
FOR UPDATE nil-deref risk) and no duplicate seam to keep in sync. Settled
design (c), detailed in design doc §31.3:
1. `:314` leaf-count: `len(scans) != nprefix` -> `!= nprefix+numSynthetic`
   (`numSynthetic := len(semiAnti)`). `nprefix` itself stays unchanged —
   synthetic leaves are never real FROM items.
2. `:333` offset-agreement: walk `scans` with `i`, walk `ctx.bindings`
   with a separate counter `j` that skips synthetic indices (build
   `synthetic RelSet` = OR of `semiAnti[*].rhs`, same pattern
   `buildLeafSpans` already uses locally); only advance/compare `j` on
   real leaves.
3. `:347` spine-offset-disagreement — **a third bug found this loop,
   independent of (a)/(b)/(c)**: `prefixTotalWidth :=
   cumOffsets[len(cumOffsets)-1].hi` silently reads a SYNTHETIC leaf's
   out-of-band span (== totalRealWidth + that leaf's own width) whenever
   the synthetic leaf is LAST in walk order (a bare trailing `EXISTS` —
   plausibly the common case), instead of the real total width. Fix:
   `buildLeafSpans` needs to also return the real-only running total (the
   `realOffset` it already computes internally at `:1327`), or recompute
   it locally as `sum(widths[i] for non-synthetic i)`.

Next step: code §31.3's three-check fix inside `tryPGShapedJoinSearch`,
gated behind a DIRECT UNIT-TEST CALL only (mirror `-3i-plumbing-b1`'s
"prove inert before wiring": production keeps passing `admitSemiAnti=
false` at the one call site, so this stays fully inert). New unit test
needed exercising all three: a shape with a real leaf AFTER a synthetic
one (offset-agreement + leaf-count) AND a shape with the synthetic leaf
LAST in walk order (the new prefixTotalWidth bug). Do NOT combine this
with the separate `predp.go` descend-loop reachability change (§30's
original step (i) target, still not started) — that stays its own later,
higher-blast-radius loop per design doc §31.4.

M0142-0008c-3c/-3d remain independently available once -3i-plumbing-b2
lands but are not blocking this item's own next step.

Alternatives if -3i-plumbing-b2 is judged not worth continuing
immediately: M0141-S2b-2 (base join/scan Pathlist-across-search-boundary
surgery — still needs its own scoping pass). M0142-0003i/0003k remain
BLOCKED on a human-authorized shared `:65433` cluster reload — do not
attempt. Unrelated, NOT to be picked up under this banner unless
explicitly re-prioritized: `internal/parser`'s `GroupedJoinUnaliased`
AST-drift gap (pre-existing, ~60 failing test functions — still the ONLY
package failing in the units precommit gate, unchanged by this loop).

Gates run this loop: `go build ./...` clean (no .go files touched this
loop; ran as a sanity check anyway). `make ralph-state-guard`: found and
auto-repaired the same benign prior-loop clean-exit marker as several
prior loops; consistent after repair. No optimizer/executor code changed,
so `go test ./internal/optimizer/...` and the TPC-DS SF0.25 sweep were not
re-run (nothing to regress — design-doc/fix_plan/README edits only).
Nightly triage: checked ci/logs/action-items.md — still the same run
(20260916-035206, 13 items) a prior loop already filed; no new run,
nothing to file this loop.

In-flight: none. Nothing outstanding from this loop once committed.
