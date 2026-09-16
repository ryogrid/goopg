Task: M0142-0008a-3i-plumbing-recon3 (DONE this loop) — a design recon that
corrects the -3i-plumbing task's own decomposition before any DP-search code
lands. No production code changed.

Files this loop: docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md
(§14 added), docs/design/README.md (index row extended), .ralph/fix_plan.md
(M0142-0008a-3i-plumbing-recon3 filed+checked, M0142-0008a-3i-plumbing
rescoped). Committed together with this file.

Key symbols: `extractSearchLeaves` (joinsearchseam.go:1071, the walk closure
at :1104-1191 — type test at :1110, Left/Right admission branch at
:1116-1170 building `outerChainLink`), `reresolveJoinByName`
(joinlayout.go:623 — re-resolves an ALREADY-PLACED join in place, cannot
relocate one), `runJoinSearchBelowPinned` (predp.go:73 — the splice model
this recon found cannot reach PG's Q69 shape no matter how it's extended),
`rangeBinding`/`rangeBinding.table` (planner.go:549, *catalog.Table
dereferenced unconditionally dozens of times in planner.go — this is why
appending x.Right as a bare struct is unsafe), `baseRelInfo.table`
(cardinality.go:694, a SEPARATE field, already nil-safe everywhere:
joinsearch.go:403/480, joinrelsize.go:638, relfromjoinlist.go:233/546),
`problemPairsOuterWithDerived` (relfromjoinlist.go:564 — the Q78
catastrophic-mis-costing firewall; confirmed it skips Semi/Anti entirely,
`:589-593`), `existsUnnestSJInfo` (unnest.go:4398, throwaway 2-bit numbering
still waiting for admission-time real bits).

Hypothesis/Findings: §12.4's own "(a) append x.Right to bindings/relInfos,
(b) SJInfo bookkeeping, (c) fix the post-search splice" decomposition is
WRONG-LAYERED, not just under-verified. Three findings, all live-code-cited
(design doc §14): (1) x.Right cannot become a rangeBinding by bare append —
needs the same synthetic-&catalog.Table{} pattern every derived-table/CTE
leaf already uses (planner.go has ~15 examples); this part is buildable and
cheap once done right. (2) The REAL blocker is one layer up:
reresolveJoinByName only patches an already-placed join's predicate; it
cannot relocate the join or let the search build a new Semi/Anti node at a
chosen position, so (a)+(b)+(c) built on runJoinSearchBelowPinned's splice
model can NEVER reach an interleaved shape like PG's real Q69 plan (Semi
Join low in the tree, two Anti Joins stacked above it, not beside it) — the
pin itself is the obstacle. (3) goopg already has the right mechanism for
the sibling LEFT/RIGHT case: extractSearchLeaves's existing chain ADMISSION
(flatten both sides into the ordinary leaf list, record a link struct, let
the search's own join_is_legal-fed legality machinery place the join) is
the actual S5b reopening mechanism — reuses ~90% of already-tested
outer-join-admission infrastructure instead of (b)/(c)'s from-scratch
bookkeeping+splice-repair. This is bigger than §12.4 estimated (touches
extractSearchLeaves, a function 3 past C-04-series silent-regression fixes
already hardened) — correctly not sized for one loop, hence recon-only again
this loop rather than coding blind into the highest-risk subsystem in the
project's history.

Next step: M0142-0008a-3i-plumbing (rescoped, still open) — per design doc
§14.3's 5-item plan: (1) a throwaway probe (style of §13's
m0142_0008a_3i_verify_probe_test.go) against the Q69 fixture that extends
extractSearchLeaves's type test (joinsearchseam.go:1110) LOCALLY in a test
file to admit JoinTypeSemi/JoinTypeAnti, descending both sides the way
Left/Right already do, confirming the flattened leaf list matches
prediction and checking whether outerChainLink's existing consumers
(outerOnQualsOK, deriveOuterLinkConstants, problemPairsOuterWithDerived)
choke on a link with empty `nullable` bits; (2) decide new
`semiAntiChainLink` type vs. a Jointype field on the existing
`outerChainLink`; (3) rebuild existsUnnestSJInfo's real RelSet bits at
admission time via leafRangeRelSet; (4) retire
runJoinSearchBelowPinned's splice for the now-admitted cases (this is what
resolves the old (c) — no separate splice-repair needed once the search
places the join itself); (5) reduceOuterJoins's LEFT->ANTI demotion is
existing, live, production precedent that a real Semi/Anti SpecialJoinInfo
in ctx.joinInfoList already works with the ordinary search's legality
checks today — corroborating, not a drop-in. Alternatives if this is
blocked or judged still too large: M0142-0008c (create_unique_path scoping,
independent, same census, has a concrete PG-source resume point) or
M0142-0005 (per-worker Memoize scoping, independent, needs its own
scoping/floor-measurement pass per its own filing) are both open and
unblocked. M0142-0003i/0003k remain BLOCKED on a human-authorized shared
`:65433` cluster reload — do not attempt.

Gates run: none needed (docs/plan-file-only change, no production code
touched). `make ralph-state-guard` run before finishing (see status block).

In-flight: none.
