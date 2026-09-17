Task: M0141-S7-cd-q64-reclassify (recon, `.ralph/fix_plan.md` item 4's
"cost diagnosis only" scope, selected via banner's P0-E6-wait fallback: M0143
has only the owner-gated M0143-0007b left, item 3 is implementation (not
selectable), so item 4's two open recon tasks -> cd-q64-reclassify, the
one prior loop's baton pointed at next). DONE and committed this loop
(`18390e924`).

Files: `docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`
(new "Update 2026-09-18c" section), `docs/design/README.md` (appended
Update-2026-09-18c summary to the m0141-s7 index row), `.ralph/fix_plan.md`
(M0141-S7-cd-q64-reclassify ticked with its finding; new implementation task
**M0141-S2b-8** filed under M0141-S7's nested list, right after
M0141-S7-cd-candidatepool). No production code touched — pure recon-by-reading,
no server/trace needed for this task (unlike its sibling cd-q64/candidatepool
tasks which needed a live DPPATH trace).

Key symbols: `electOrderedGrouping` (`upperorderedgrouping.go:178`, call site
`planner.go:1960`, gated on `len(s.OrderBy)>0`), `preplanWithClause`
(`with.go:210`, plans a CTE body via the SAME `planSelectWithSettings`),
`addGroupingPaths` (`groupingpaths.go:379`, SORTED arm at :441-495),
`sortPathForBounded` (`joinpathsmerge.go:480-515`, always full `PathSort`,
never checks `seed.Pathkeys` for a partial-prefix match).

Findings: the task's own (a)/(b)/(c) decision tree turned out not to need a
trace — `electOrderedGrouping` only ever runs when the statement/CTE HAS an
`ORDER BY` of its own, and Q64's `cross_sales` CTE has none (verified by
reading `query64.sql`), so the "does the CTE caller reach it" question is
moot: it can't reach it regardless of caller wiring, there's no ORDER BY to
adjudicate. PG's `Presorted Key: item.i_item_sk` / `GroupAggregate` inside
the CTE is a DIFFERENT mechanism entirely — a plain-GROUP-BY (no ORDER BY
anywhere) sorted-vs-hashed strategy election. goopg's equivalent,
`addGroupingPaths`'s SORTED arm, always builds a full `Sort`
(`sortPathForBounded`) and has NO Incremental-Sort-over-partial-prefix-seed
offer at all, for any query — a genuinely un-audited gap, case (c). Filed as
**M0141-S2b-8** (implementation, not recon — not selectable while banner item
4 restricts to diagnosis-only). Tally correction applied: "Nested Loop x4" ->
"Nested Loop x3 (Q4, Q11, Q35)" in both the design doc's Update-18c prose and
fix_plan's task text (the historical producer-shape table itself, like
Update-18b before it, is left as a historical snapshot with a prose
correction rather than edited in place — matches 18b's own precedent).

Next step: re-check the banner. M0141-S7's recon-only tasks (item 4) are now
BOTH closed (cd-q64, cd-q64-reclassify) except **M0141-S7-cd-candidatepool**
(still open, needs a live DPPATH trace — investigate whether
`addIncrementalSortPaths` should also build over the same cheap seed
`createOrderedPaths`'s arm 1/2 uses when it has a genuine partial-prefix
match). That's the next selectable recon-only task under the P0-E6-wait
fallback order, unless P0-E6 has been marked `[x]` by the owner by the next
loop (re-check the banner's `[!]` marker first — if resolved, P0-E7 becomes
selectable and takes priority over everything in items 2+).

Gates run: `go build ./...` clean; `go vet ./internal/optimizer/` clean (no
code changed, ran as a courtesy). `make ralph-state-guard` auto-repaired a
stale `progress.json`/`status` mismatch (previous loop's clean-exit marker
misread as project-completion), clean after. Pre-commit hook's pgbench smoke
PASS (mandatory on every commit per AGENT.md, ran despite docs-only diff).
No TPC-H/TPC-DS dependency — no server started, `:65437`/`:65438` untouched
(never even checked status this loop since no cluster was needed).

In-flight: none.
