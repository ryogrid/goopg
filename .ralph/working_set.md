Task: nightly triage (M-NIGHTLY filing, unconditional) + M0142-0003c —
re-verify the enumeration-order/tie-break premise for Q9's L6 divergence
before attempting any DP-search reordering. **Both DONE and committed this
loop.** Measurement-only for 0003c (two read-only `EXPLAIN`s against the
always-on shared PG `:65432` TPC-H reference cluster; no goopg server run, no
production code changed).

Files: `.ralph/fix_plan.md` (nightly run `20260916-035206`'s 13 AI-ids filed —
12 appended to existing open M-NIGHTLY bullets, 1 new bullet for
`TestPort_IsolationSuite`; M0142-0003c entry rewritten `[x]` with the
refutation finding, new `M0142-0003d` follow-up filed `[ ]`).
`.ralph/deferral_ledger.md` (new `m0142-0003c` row → resume point M0142-0003d).
`docs/design/0100-0149/m0142-0003c-q9-real-pg-cost-gap-not-a-tie.md` (new).
`docs/design/README.md` (indexed). `analysis/m0142/m0142-0003c-pg-{default,
serial-default,forced-goopg-order}.txt` (new, committed evidence).

Key symbols: `internal/optimizer/joinsearchlevel.go` (`joinSearchOneLevel`,
confirmed a faithful line-cited port of PG's `join_search_one_level` —
NOT touched, just read); `internal/optimizer/joinsearch.go`
(`buildInitialRels`, confirmed FROM-order initial rels). No goopg code edited
this loop. PG side: real PG 18.3 on the shared `:65432` TPC-H reference,
`join_collapse_limit=1`+`from_collapse_limit=1`+explicit left-deep `JOIN`
(same forcing method M0142-0015 used for TPC-DS Q45).

Findings this loop: M0142-0003c was filed on the premise (from -0003b's
Verdict) that Q9's L6 tie is an enumeration-order question — reordering
goopg's pair-visitation to match PG's `join_search_one_level` might flip
which of two exactly-tied candidates wins. **That premise is refuted.**
(1) goopg's enumeration STRUCTURE already matches PG's exactly (verified by
reading the code, not just asserting it — phases 1/2/3, dedup offsets,
FROM-order initial rels, all cited against `joinrels.c` line numbers, plus an
existing pair-count completeness test). (2) Forcing real PG into goopg's own
chosen Q9 order costs **336207.55 vs PG's own default 204932.03 — a 64% gap,
not a near-tie.** Real PG needs no tie-break; it wins outright in its own
cost model. The exact float64 tie M0142-0003b measured is a property of
**goopg's** cost model alone. (3) This means Q9 and TPC-DS Q45 (M0142-0015)
are NOT two witnesses of the same phenomenon, contrary to this task's own
filing note — Q45 is a genuine both-engines near-tie (unaffected, stays
tie-break-class); Q9 is a real costing-term divergence goopg's model hides
behind a coincidental exact tie, most likely centered on how goopg prices an
index-driven Nested Loop against an already-shrunk composite relative to a
hash join that forces a full base-table scan of `lineitem` (real PG's default
plan discounts the index route by ~130000 cost units over the hash
alternative on this exact join; goopg's model prices the analogous
`nestloop.index` candidate only ~3-9 units above its own `join.hash` winner).

Next step: **M0142-0003d** (filed this loop) — scope which specific cost term
underprices goopg's index-driven-NLI-over-lineitem route (or overprices the
hash-with-full-scan alternative). Concrete first move per
`planner_verify_both_candidates_generated`: confirm goopg's DP search
actually generates a candidate comparable to PG's
index-driven-NLI-from-the-shrunk-composite at the right level (don't assume
it exists just because -0003a saw a `nestloop.index` verdict=dominated line —
confirm it's driving the SAME composite PG's plan drives), then get its full
`internal/optimizer/cost_funcs.go` breakdown against PG's
`cost_nestloop`/`cost_index` (`costsize.c`). Check whether B8
(`indexProbeCostMultiplier`, left explicitly unsettled by last loop's
M0142-0005 recon) is the mismatched term — but measure first, don't assume.
Other still-open M0142/M0141 items as of this loop: **M0142-0005** (large,
needs its own scoping recon — per-worker Memoize cache), **M0142-0008a/0008b**
(SEMI/ANTI decorrelation scoping), **M0142-0016c** (Q33/Q54/Q56 shape check),
**M0141-S2b/S3-S7** (upper-planner ordering, Incremental Sort). None mandated
over the others by the banner (still item 4).

Gates run: `git status --porcelain -- internal/` empty before AND after this
loop's work (no production code touched — confirmed, not assumed). `go build
./...` clean. `make ralph-state-guard`: same pre-existing stale
progress-marker inconsistency as the last several loops (status=running vs a
stale progress=completed marker from a prior loop's clean exit), self-repaired
to in_progress, then passed clean. Practice-card row-count gate suite not
required (no production code touched, same reasoning as last loop's
M0142-0005 recon). Pre-commit pgbench smoke gate: runs at commit time per the
mandatory-on-every-commit policy.

In-flight: none. A background Explore-agent research task (PG
`join_search_one_level` vs goopg DP enumeration order comparison) was launched
early this loop as a safety hedge against Ralph's headless -p mode risk of
losing async Agent-tool results at turn end — direct investigation in-session
answered the core question first (Finding 1/2 above), but the agent's report
still arrived and turned out to add real value: it surfaced a pre-M0138 round
(`docs/design/not_ralph/plan_parity_fix_take2/r53-q9-costing-step0/SLICE1.md`,
2026-09-10) that had already attributed a same-shape L6 cost gap to
`hashsize.EntryBytes`'s spill-footprint model — folded into the design doc's
Addendum and M0142-0003d's fix_plan entry as a concrete (but re-verification-
needed) resume point, not discarded. Nightly CI batch
(`ci/logs/action-items.md`, run `20260916-035206`, mtime 2026-09-16 04:45):
all 13 items now filed under M-NIGHTLY. The next loop should check whether a
newer run has landed before re-checking triage.
