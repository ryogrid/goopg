Task: M0141-S2b — its own "size it as its own scoping task before attempting
it" scoping pass (S2b's fix_plan text called for exactly this). DONE this loop
(recon/decomposition-only, no production diff, same precedent as M0140-0006).

Files: `.ralph/fix_plan.md` (M0141-S2b closed `[x]` as a decomposition into 5
new unchecked sub-tasks S2b-0..S2b-4; M0141-S7's bullet gets a NARROWED note
correcting its "all 14 witnesses need S2b" claim). `docs/design/README.md`
(new m0141-s2b row indexed). New
`docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`.
`.ralph/deferral_ledger.md` (new row, task-id `m0141-s2b`). No Go/production
code touched — pure Serena/Read code census across
`internal/optimizer/{upperordered.go,upperorderedinput.go,
upperorderedgrouping.go,windowsetoppaths.go,distinctpaths.go,planner.go}` +
`git log`/`git blame`-style dating of `upperorderedgrouping.go`. No
server/cluster needed or touched.

Key symbols/paths: `internal/optimizer/upperorderedgrouping.go`
(`electOrderedGrouping`/`groupingEmissionPathkeys`, landed 2026-09-11
`e8a1215fd`, R47 slice 2/K101 — THE central finding this loop made: this file
already implements S2b's "loop the producing rel's Pathlist into ORDER BY"
mechanism, for GROUP_AGG only); `internal/optimizer/distinctpaths.go`
(`createDistinctPaths`/`addDistinctPaths`, C-16a/b — has the same internal
Hashed/Sorted-shaped plurality GROUP_AGG has, unwired to ORDER BY);
`internal/optimizer/windowsetoppaths.go` (`createWindowPaths` — its own
comment proves it NEVER has more than one candidate: "goopg's input is a
single finished Node, so that loop has one iteration"; `createSetOpPaths` —
has plurality via `addSetOpPaths` but `newUpperRelForNode` allocates a FRESH
rel per call, relids always 0, not re-fetchable across a chain, per its own
comment); `internal/optimizer/upperorderedinput.go` (`inputNodePathkeys`/
`searchedTreePathkeys`/`validatedSearchPathkeys` — the C-07 seam that already
recovers ONE winning candidate's pathkeys across the join/scan search's
coordinate boundary, never a second one; file header explicitly names the
coordinate-boundary translation risk — "bitten twice", "TOTALITY invariant
panics on any hole").

Findings this loop: (1) **Corrected a premise both M0141-S2 (2026-09-15) and
M0141-S7 (2026-09-16) carried without checking**: `electOrderedGrouping`
already exists and is live, landed BEFORE either task's own dated finding,
yet neither cites it — both read `createOrderedPaths`/`addOrderedPaths`
directly and missed the pre-check at `planner.go:1960`. S1's fresh
2026-09-15 capture (which S2 used) ran WITH the loop live and still found
mechanism (B) alive in Q4/Q5/Q8/Q12/Q21/Q22 — so the residual gap is real,
just mis-described as "the loop was never built" instead of "the loop
declines for these queries". (2) Census of all 4 `createOrderedPaths`
callers' UPSTREAM rels for their own Pathlist plurality (table + citations in
the design doc): GROUP_AGG plural+wired; DISTINCT plural+unwired (cheapest
net-new slice, mirrors `electOrderedGrouping` almost exactly); WINDOW never
plural at its own rel (no local fix possible, purely downstream-gated); SETOP
plural but its rel is unaddressable later (separate identity problem, no
current motivating witness); base join/scan ORDER BY (no aggregation) — the
real K24/F15/K12(B) item, the DP search's own multi-candidate tournament is
discarded at the `upperorderedinput.go` seam. (3) Working hypothesis (NOT
traced live this loop — flagged explicitly as unconfirmed): GROUP_AGG's
residual 6-query gap is `electOrderedGrouping`'s own `len(cands) < 2` decline
firing because the join/scan tree beneath grouping never hands it a
sort-shaped input to build a second (Sorted) `PathAgg` candidate from — the
same one-Node-not-Pathlist seam repeated one level down. Filed as S2b-0 to
settle this before spending effort elsewhere. (4) Structural ceiling
independent of all of the above: `addOrderedPaths` only ever has 2 arms
(no-sort / full-sort) — Incremental Sort's own third "prefix-match" arm
(S7's job) is required on top of every S2b slice regardless of which lands.
(5) Decomposed S2b into S2b-0 (trace recon), S2b-1 (DISTINCT loop-fix,
cheapest), S2b-2 (the real K24 item: join/scan Pathlist across the
coordinate boundary — needs its OWN further scoping pass first, per K24's
"do not attempt in one sitting"), S2b-3 (WINDOW, gated on S2b-2), S2b-4
(SETOP, no current motivating witness).

Next step: per the banner's item-4 ordering, the next loop should pick
**M0141-S2b-0** (cheapest, pure trace recon, settles whether S2b-1 is worth
doing standalone or everything funnels through S2b-2) — `GOOPG_PGSHAPED_DP_TRACE=1`
capture of TPC-H Q4/Q5/Q8/Q12/Q21/Q22 via `./bin/estimate-audit -plan-only`
per `m0137-0003-baseline-capture-procedure.md`, grep `producer=` lines
feeding the GROUP_AGG rel, confirm/refute whether a Sorted `PathAgg`
candidate is ever offered. **M0141-S2b-1** (DISTINCT loop-fix) is the
next-cheapest if S2b-0's answer says DISTINCT is worth attempting standalone,
or as a parallel/alternate pick regardless (it is cheap enough to attempt and
measure even with a null-result risk). Do NOT attempt **M0141-S2b-2** blind —
it needs its own scoping pass first, per K24. Alternative if the banner or a
fresher read says otherwise: M0142's still-open items — M0142-0008a (SEMI/ANTI
decorrelation scoping recon, unstarted) or M0142-0016c. M0142-0003i/-0003k(c)
remain BLOCKED on a human decision (shared `:65433` TPC-H bench cluster
reload) — do not re-attempt without explicit authorization. Re-read the
`## Current Priority` banner first in case it was rewritten.

Gates run: `make ralph-state-guard` — same pre-existing stale status/progress
marker as recent loops (loop_count field lag from a concurrent driver),
self-repaired, passed clean. No `go build`/`go test`/pgbench-smoke run (no
Go/production code touched this loop — pure recon/decomposition, matching
the M0140-0006 precedent which also ran no code gates).

In-flight: none. Nothing left running; no server/cluster touched this loop.
