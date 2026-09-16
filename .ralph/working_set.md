Task: M0142-0008a-3i-plumbing-a — land `semiAntiChainLink` + its
`semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK` legality consumers, plus
`problemPairsOuterWithDerived`'s Semi/Anti firewall arm, as INERT
infrastructure (design doc §15's settled item 2 + its new safety finding).
DONE and committed this loop. Also ran a live call-chain trace that settles
an open dependency question the previous three loops left unresolved — see
"Hypothesis/Findings" below, this is the important carry-forward.

Files this loop: internal/optimizer/joinsearchseam.go (new `semiAntiChainLink`
type + `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`, placed next to
`outerChainLink`/`outerLinksHaveSJInfos`/`outerOnQualsOK`),
internal/optimizer/relfromjoinlist.go (`problemPairsOuterWithDerived`'s
switch gained `parser.JoinSemi, parser.JoinAnti`, one line),
internal/optimizer/semiantichain_test.go (NEW: 9 tests — both consumers'
accept/decline cases plus the firewall arm's Semi/Anti-over-derived and
Anti-over-derived positive cases),
internal/optimizer/m0142_0008a_3i_plumbing_probe_test.go (updated consumer-#3
assertion: its nil-table fixture now correctly declines via the new arm,
flipping from the old "want false" to "want true"),
docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md (§21 added),
docs/design/README.md (index row extended), .ralph/fix_plan.md (old
undifferentiated `M0142-0008a-3i-plumbing` replaced by `-3i-plumbing-a` [x]
and `-3i-plumbing-b` [ ]; `M0142-0008c-3c`/`-3d`'s "blocked on" references
retargeted from "items 3-5" to `-3i-plumbing-b`), .ralph/deferral_ledger.md
(`M0142-0008a-3i-plumbing-a` row appended).

Key symbols: `semiAntiChainLink`/`semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
(joinsearchseam.go, right after `outerChainLink` at ~line 1284),
`problemPairsOuterWithDerived` (relfromjoinlist.go:564, switch at :589-593),
`extractSearchLeaves` (joinsearchseam.go:1071, STILL treats Semi/Anti as an
opaque leaf — unchanged this loop), `origChain` capture (planner.go:1532-1536),
`runJoinSearchBelowPinned`'s descend loop (predp.go:83-115).

Hypothesis/Findings (IMPORTANT — read before picking up -3i-plumbing-b or any
more Semi/Anti-admission work): a live traced call chain (not inference) found
that extending `extractSearchLeaves`'s type test to admit Semi/Anti (the old
filing's "item 1") is DEAD CODE for every real query on its own, because TWO
independent gates block a Semi/Anti node from ever reaching it: (a)
`planner.go:1532-1536` captures `origChain := f.Child` BEFORE
`unnestSubqueriesInPlan` runs, so `origChain` never contains a Semi/Anti node
by construction (unnesting hasn't happened yet); (b) `predp.go:83-115`'s
descend loop is hard-coded to walk through Semi/Anti `*Join` nodes ONLY,
bailing (`predp.go:100`, `return newRoot`) on any OTHER `*Join` type it might
need to pass through — it never reaches past a plain Inner/Left/Right join to
find `origChain`'s Filter wrapper in a hypothetical Semi/Anti-bearing chain
anyway. So items 1 (walk extension), 4 (retire predp.go's splice), and 5
(reduceOuterJoins precedent) from the old `-3i-plumbing` filing are ONE
coupled step that must land together, not three separable pieces — the
numbering in §14.3/§15 implied separability that doesn't actually exist.
What DOES separate cleanly (and is what landed this loop): the new
`semiAntiChainLink` type + its two legality consumers + the Q78-firewall arm
are pure, callable-but-uncalled functions — fully unit-testable and PROVEN
empirically zero-impact (TPC-DS SF0.25 sweep, see Gates below) without
touching the walk or predp.go at all. This is the same "dispatch-layer-first"
shape M0142-0008c-1/-2/-3a already used successfully.

Next step: per banner order (M0137-M0143 group), pick up
**M0142-0008a-3i-plumbing-b** (fix_plan.md, newly filed this loop, design doc
§21.4) — the coupled change: (1) move/duplicate the `origChain` capture to
after `unnestSubqueriesInPlan` runs (or otherwise route a Semi/Anti-bearing
tree to `tryJoinSearch`); (2) extend predp.go's descend loop to pass through
non-Semi/Anti `*Join` nodes instead of hard-bailing; (3) extend
`extractSearchLeaves`'s type test to admit Semi/Anti and build
`semiAntiChainLink`s via the walk (consumers already exist from this loop);
(4) rebuild `existsUnnestSJInfo`'s throwaway 2-bit numbering with real
`leafRangeRelSet` bits at admission time; (5) retire predp.go's
splice-and-reresolve for the newly-admitted cases. **Explicitly NOT sized for
one loop** (design doc §21.4) — do its own scoping pass first: what breaks in
`origChain`'s other callers/assumptions if the capture point moves post-unnest,
and whether `chainOnQual`'s `belowNullable` bookkeeping needs a
Semi/Anti-aware arm for INNER links sitting above an admitted Semi/Anti link
(§14.3's still-open question, never answered by any of the last 4 loops).
Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b lands — both are
explicitly blocked on it (no TPC-DS measurement can distinguish "correct but
unreachable" from "wrong" while `addPathsToJoinrel` never receives a real
SEMI/ANTI sjinfo).

Alternatives if -3i-plumbing-b's scoping pass is judged not worth continuing
immediately: M0141-S2b-2 (base join/scan Pathlist-across-search-boundary
surgery — still needs its own scoping pass). M0142-0003i/0003k remain BLOCKED
on a human-authorized shared `:65433` cluster reload — do not attempt.
Unrelated, NOT to be picked up under this banner unless explicitly
re-prioritized: `internal/parser`'s `GroupedJoinUnaliased` AST-drift gap
(pre-existing, traced to `dc91bd6b7`, ~60 failing test functions in
`internal/parser`, reproduces identically on clean HEAD, reconfirmed AGAIN
this loop via the pre-commit units gate — now confirmed unchanged across 4
consecutive loops).

Gates run: `go build ./...` (clean). `go vet ./internal/optimizer/...`
(clean). `go test ./internal/optimizer/...` (full pass, incl. all 9 new
`semiantichain_test.go` cases and the updated probe test, run together and
individually with `-v`). `scripts/tpcds-sf025-regression.sh sweep` (private
`GOOPG_BIN=tmp/goopg-sf025-bin`, foreground) — `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: same=99 changed=0` — this is a
real empirical gate result, not a formality: it specifically checks whether
the new `problemPairsOuterWithDerived` Semi/Anti arm changed anything for
`reduceOuterJoins`'s LEFT→ANTI demotion, the one live producer of a real
Semi/Anti `SpecialJoinInfo` already reaching that function in production
today — it did not. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — same single pre-existing unrelated
`internal/parser` package failure as the prior three loops (confirmed
identical failure set, ~60 test functions, `GroupedJoinUnaliased` AST-drift);
everything else including `internal/optimizer` green. `make
ralph-state-guard` — found and auto-repaired the same stale
clean-exit-marker pattern as the prior three loops, then passed clean.

In-flight: none. The private sf025 server used by the sweep script is
started/stopped internally by `scripts/tpcds-sf025-regression.sh` itself (no
manually-started server to clean up this loop).
