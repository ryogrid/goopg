Task: M0142-0008 — recon: how much of goopg's plan shape is chosen by
forced rewrites rather than by the search? **DONE and committed this loop**
(`a38711e6b`). Measurement-only, no production code changed.

Files: `.ralph/fix_plan.md` (M0142-0008 `[x]`, M0142-0008a/0008b filed as
new open tasks under the M0142 milestone). `.ralph/deferral_ledger.md` (new
`m0142-0008` row). `docs/design/0100-0149/m0142-0008-forced-rewrites-vs-search-census.md`
(new). `docs/design/README.md` (indexed).

Key symbols: none touched (recon-only, read-only investigation). Read
`internal/optimizer/scan_input_rewrite.go`, `nl_index_join.go`,
`joinpathsnli.go`, `unnest.go`, `cost_funcs.go`, `plannersettings.go`, and
`planner.go:1580-1649` (the call sites deciding whether a statement's
FROM-tree reaches `tryJoinSearch`).

Findings this loop: classified the six candidate "forced rewrite"
mechanisms the milestone's ledger flagged. **Both rewrite functions
(`rewriteScanInputsWithSingleTablePredicates`, `rewriteJoinsToNLI`) carry an
explicit `isSearchedTree` guard confining them to the portion of the tree
the DP search never reaches at all** — the ledger's 6.5x/12.5x "deletion
regresses" numbers (Q20, Q4) are the rewrite being the sole decision-maker
for subtrees outside the search's current structural coverage, not the
rewrite winning a head-to-head cost comparison against the search. Traced
two structural causes: **Q4** — `unnestExistsExpr` (`unnest.go:4078`)
builds the SEMI join directly on the raw parse tree before any search
joinrel exists (F11/K63/M0139-0005's already-verified "no sjinfo → no
joinrel → no path → no price" lineage). **Q20** — the search's own
single-table index-scan mechanism is structurally capable, but
`planner.go:1590-94`'s own comment confines DP search to OUTER-linked
trees, leaving filterless INNER/CROSS trees on the legacy path pending its
own future widening round. Of the four knobs: `GOOPG_NLI_COSTGATE` and
`GOOPG_INDEX_PROBE_MULT` are architecturally sound (they tune inputs the
search's own `addPath` already consumes, not forcing mechanisms);
`enable_nestloop_index` is a clean, already-correctly-scoped kill switch;
only `GOOPG_INDEXKEY_HARVEST` joins the two rewrites as a genuine
pre-search, shape-determining mechanism. **Verdict: a dedicated milestone
is warranted, but scoped to closing the two named structural coverage gaps
— not to deleting the rewrites**, which would just reproduce the already-
measured regressions until the gap is closed. Filed **M0142-0008a** (scope
wiring SEMI/ANTI decorrelation into the search's joinrel machinery —
`unnest.go:4078`, `collapse.go:398` per `take2-P3-01`'s prior finding that
`deconstructFromItem` has no catalog/resolver at that point,
`joinsearchlevel.go`) and **M0142-0008b** (measure blast radius of widening
`joinTreeHasOuterLink`'s DP-search gate to filterless INNER/CROSS trees).
Both filed as scoping recons first (K50/M0142-0012a precedent), since
either could move many plans at once.

Next step: pick the next task per `.ralph/fix_plan.md`'s `## Current
Priority` banner (still M0137–M0143, item 4: M0141/M0142 remaining
slices). Open M0142 items as of this loop: **M0142-0003c** (2-witness
recon, Q9+Q45, level-6/level-5 enumeration-order-vs-cost-tie parity),
**M0142-0005** (Memoize/probe-multiplier interlock, B6+B8, sized like an
executor slice — this is the largest non-recon item still open in M0142
and arguably deserves priority since three separate recons (M0142-0005's
own filing, M0142-0010, M0142-0013's Q23 tangent) now point at it as the
root blocker for several ea-ratchet findings), **M0142-0008a/0008b** (new
this loop, both scoping recons), **M0142-0016c** (does PG qerr-match the
M0142-0016b Q33/Q54/Q56 residual-selectivity findings if forced into an
analogous plan). None is mandated over the others by the banner.
M0142-0005 is worth strong consideration next since it's the only item in
this list that isn't itself another recon — landing it would be the first
non-recon M0142 progress since M0142-0016b, and it's independently named as
the blocker behind M0142-0010's and part of M0142-0013's findings.

Gates run: no production code touched, so the practice-card row-count gate
suite was not required (git diff -- internal/ empty, confirmed before
committing). `make ralph-state-guard`: found the same pre-existing stale
progress-marker inconsistency as the last several loops (status=running vs
a stale progress=completed marker from a prior loop's clean exit),
self-repaired to in_progress, then passed clean. Pre-commit pgbench smoke
gate: PASS (all three phases, 0 failed transactions).

In-flight: none. Used one background research subagent
(`a7e197d02f9bb64ea`) this loop to classify the six mechanisms via
code-read + `git log -S` — it completed cleanly and its findings are fully
incorporated into the committed design doc; nothing left running. Nightly
CI batch status not re-checked this loop (last loop's note: the
20260916-035206 run was in progress at that loop's start; action-items.md
at this loop's start still only reflected the older 20260914-235643 run —
all 14 of its `## AI-` items are confirmed already filed under M-NIGHTLY in
`.ralph/fix_plan.md`, so nightly triage was a no-op this loop). The next
loop should check whether action-items.md has been regenerated by the
newer run and file any genuinely new `## AI-` items before selecting work.
