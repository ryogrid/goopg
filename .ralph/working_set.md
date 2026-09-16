Task: M0142-0008a-3i-plumbing-b2 — step (ii) scoping pass landed (design doc
§32): corrected the admitSemiAnti mechanism, live-traced TPC-DS Q10, and
settled a two-phase (Phase A unchanged / Phase B new) design for the
`predp.go` reachability wiring. No production code changed this loop.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §32
  (4 subsections) — corrects §30 Finding 2's stale "4 call sites" framing,
  Q10 live trace, Phase A/Phase B design, and why Phase B's coding is
  deferred to next loop (dead-code-is-not-reference-impl caution).
- docs/design/README.md: m0142-0008a-1 index row extended with §32 summary.
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with the §32 summary
  and the refined "next loop codes Phase B" resume point.
- .ralph/progress.json: state-guard repair timestamp bump (benign, unrelated).

Key symbols/facts established this loop (verified live, not assumed):
- `admitSemiAnti` is a LOCAL LITERAL argument to `extractSearchLeaves(chain,
  false)` inside `tryPGShapedJoinSearch` (joinsearchseam.go:309) — NOT a
  parameter needing to thread through tryJoinSearch's 4 call sites (that was
  §30's pre-step-(i) framing, now stale). Flipping this one literal to
  `true` is safe unconditionally for the other 3 tryJoinSearch callers
  (planner.go:1544/1569/1606) since their `chain` is always captured BEFORE
  `unnestSubqueriesInPlan` runs and can never contain a Semi/Anti node.
- TPC-DS query10.sql (bench/tpcds/runtime_goopg/tpcds-data/queries/query10.sql)
  hits predp.go's documented "sunk" shape (3 non-EXISTS conjuncts on
  customer/customer_address/customer_demographics), NOT "nothing sunk" —
  confirms Phase A already runs unchanged for this milestone's own cited
  unblock target.
- `runJoinSearchBelowPinned`'s existing splice (predp.go:135-143) already
  DROPS the wrapping sunk Filter for free when all its conjuncts are legally
  pushed (`newPred == nil → newTarget = newChild`) — leaving a Filter-free,
  fully walkable subtree under the innermost pinned Semi/Anti join after
  Phase A, for the common case.
- `extractSearchLeaves`'s admitted-Semi/Anti `walk` (joinsearchseam.go, near
  line 1140) already recurses through arbitrarily nested Semi/Anti joins on
  `j.Left` — no new recursion logic needed for a stacked spine.

Hypothesis/Findings: none refuted this loop; all three bullets above are new
confirmed facts. Nothing ruled out — this narrows and de-risks step (ii),
it does not change the milestone's overall direction.

Next step: **code Phase B** in predp.go as an INERT scaffold (production's
admitSemiAnti literal at joinsearchseam.go:309 STAYS `false` this next loop
too) — a second `tryJoinSearch` call rooted at `spineJoins[0]` (nil
predicate) attempted after Phase A's existing splice, replacing the
absorbed spine on success and skipping `spineJoins`' bottom-up
`reresolveJoinByName` loop for joins the search's own output now covers,
falling back to today's unchanged tree on decline. Because the literal
stays `false`, this call is guaranteed to decline via the same
"leaf-count" mechanism §31.4 already proved inert — so verify the SUCCESS
path with a DIRECT unit test against a hand-built "search succeeded"
result (extract the splice-in logic into its own testable function first,
mirroring `pgShapedOffsetChecksOK`'s extraction), not via the TPC-DS sweep.
Flipping the literal to `true` + a live EXISTS/NOT-EXISTS end-to-end fixture
(§27.4's existing gate) is a SEPARATE, still-later step (iii) — do not
collapse it into the Phase B scaffold loop.

Gates run this loop: `go build ./...` clean (no optimizer code touched, this
was a docs-only loop). `make ralph-state-guard`: found and auto-repaired the
same benign prior-loop clean-exit marker several prior loops have also seen;
consistent after repair. Pre-commit hook's pgbench smoke: PASS (TPC-B ~42
tps, select-only ~147 tps, 0 failed). No optimizer unit tests / TPC-DS sweep
run this loop since no optimizer code changed (design-doc-only diff) —
next loop (which DOES touch joinsearchseam.go/predp.go) must run the full
`go test ./internal/optimizer/...` + TPC-DS SF0.25 sweep as usual.

In-flight: none. Nothing outstanding once this loop's diff is committed
(already committed as cb04e990d).
