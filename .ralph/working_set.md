Task: M0142-0008b — scoping recon: size widening the DP-search gate
(`joinTreeHasOuterLink`) to filterless INNER/CROSS join trees. DONE this loop
(recon-only, no production diff). Verdict: do NOT widen the gate; no
implementation task filed.

Files: `.ralph/fix_plan.md` (M0142-0008b marked `[x]` with findings).
`docs/design/README.md` (indexed). New
`docs/design/0100-0149/m0142-0008b-scoping-recon-filterless-inner-cross-census.md`.
No Go/production code touched — pure AST-analysis recon via a throwaway
`/tmp` scratch Go program (deleted after use), delegated to a subagent and
verified. No server/cluster needed or touched.

Key symbols/paths: `internal/optimizer/planner.go:1373-1611` (the two-arm
`s.Where != nil` / `joinTreeHasOuterLink` gate before `tryJoinSearch`),
`planner.go:16898` (`joinTreeHasOuterLink`, left-spine-only walk).

Findings this loop: parsed all 22 TPC-H (Q15 skipped by design) + 99 TPC-DS
queries (3 unrelated corpus-data parse failures in the "hierarchy rank"
family — stray semicolon before a derived table's closing paren, not a
goopg parser gap), walked all 429 reachable `SelectStmt`s (top-level, CTEs,
derived tables, sublinks, UNION arms). **Only 5/429 (1.2%, all TPC-DS, zero
TPC-H) hit the "neither arm runs" gap**: `query28`/`query61`/`query77`/
`query88`/`query90`. 4 of 5 comma-cross provably-1-row scalar aggregate
subqueries (join order cannot matter there). Only `query77`'s `cs, cr`
cross (genuinely multi-row, and the query's only channel branch using
implicit cross instead of the `LEFT JOIN` its store/web UNION siblings use)
is a plausible real target — but its fix is a narrower per-query rewrite,
not a gate change. Separately confirmed the gate's own left-spine-only walk
has a real blind spot (outer join present but not on the left spine is
invisible to it) but **zero corpus matches** for that shape today. Net:
gate-widening is not worth doing against this corpus.

Next step: per the banner's item-4 ordering (M0141 remaining slices /
M0142's open items), the next loop should pick among the still-open M0142
items — **M0142-0008a** (SEMI/ANTI decorrelation scoping recon, sibling of
this task, still unstarted — Q4-class queries never reach `addPath` for
their SEMI/ANTI join; see fix_plan for its full scope) or **M0142-0016c**
(does PG qerr-match the M0142-0016b Q33/Q54/Q56 findings) or one of
M0141's larger remaining slices (S2b/S3-S7). M0142-0003i/-0003k remain
BLOCKED on a human decision (shared `:65433` TPC-H bench cluster reload) —
do not re-attempt without explicit authorization. Re-read the `## Current
Priority` banner in `.ralph/fix_plan.md` first in case it was rewritten.

Gates run: `make ralph-state-guard` — same pre-existing stale
status/progress marker as recent loops (loop_count field lag), self-repaired,
passed clean. No `go build`/`go test` run (no Go/production code touched
this loop — pure recon via a throwaway `/tmp` scratch program + docs/
fix_plan updates).

In-flight: none. The `/tmp/joingate_scan` scratch dir (subagent's analysis
program, outside the repo) was removed after use. Nothing left running.
