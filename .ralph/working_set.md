Task: M0141-S2b-2 (DONE, committed `00ed0ff84`) — scoping recon for "base
join/scan Pathlist-across-the-search-boundary surgery" (the K24 item),
following the fix_plan.md banner's item 4 (M0141 remaining slices before
M0142). Nightly triage for `ci/logs/action-items.md` run
`20260917-004357` was already fully filed in fix_plan.md before this loop
started (all 17 items accounted for, -001/-004 already marked stale/
re-run-passes) — no new M-NIGHTLY filing was needed this loop.

Files: docs/design/0100-0149/m0141-s2b-scoping-decomposition.md (new
"S2b-2 result" section), docs/design/README.md (m0141-s2b row appended),
.ralph/fix_plan.md (S2b-2 marked [x] as recon; new S2b-2a/2b/2c sub-items;
S2b-3's gate text corrected to name S2b-2c specifically). Temporary
`GOOPG_S2B2DEBUG=1`-gated instrumentation in
internal/optimizer/upperordered.go was added, used, then fully reverted via
`git checkout --` before commit (confirmed empty diff).

Key symbols: `createOrderedPaths`/`addOrderedPaths` (upperordered.go —
today's single-seed bottleneck), `searchedRelOf` (searchedtree.go:169 — R21
slice 2a's accessor, already gives the full `*RelOptInfo.Pathlist`, just
unused by this seam), `costSortRun` (rel-level `rows`/`width` only, no
per-candidate prefix-credit term — the actual reason the surgery is inert).

Findings: live-traced the seam against the full TPC-DS SF0.25 corpus (temp
prints, `scripts/tpcds-sf025-regression.sh sweep`+`plans`, private
`GOOPG_BIN=tmp/goopg-s2b2-bin`, removed after). 38 real `createOrderedPaths`
calls reach a non-nil `searchedRelOf(input)` with Pathlist sizes 3-16
(median ~7) — the seam IS reached constantly, confirming the task's own
framing. But parsed all 38 blocks: every candidate in one call shares the
SAME `Rows` (36/38 exact, 2 off by 1 row via a LIMIT-kind rounding
artefact), and `sr.CheapestTotal` is ALWAYS the exact min-cost entry
already. Since `costSortRun` has no per-candidate variable term (no
Incremental Sort prefix credit exists — M0141-S7 unimplemented), the Sort
cost added on top of every candidate is an identical constant, so ranking
by cost is invariant under it: the search's own already-chosen
cheapest-total candidate (== today's single seed) remains cheapest even in
a hypothetical full-Pathlist tournament. **Conclusion: this surgery, in
isolation, is empirically proven to move zero plans** (verified: sweep +
plans both PASS=96/changed=0, before AND after the revert) — same "moves
nothing" verdict R21 Slice 1 predicted for its own plumbing-only cut.
M0141-S7 (Incremental Sort's per-candidate prefix credit) is the real
prerequisite that would make a Pathlist tournament here meaningful; the two
are mutually blocking, not independent sequential slices (S7's own
fix_plan entry already said "implementation is gated on M0141-S2b" — this
loop supplies the missing reverse-direction link).

Next step: per the fix_plan banner (M0141 slices before M0142, `## Current
Priority` item 4), the next real M0141 candidate is now **M0141-S7** itself
(re-adjudicated GO, already has its own scoping design doc
`docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`)
— but it was assessed by both the design doc and a subagent survey this
loop as a large multi-part build (14/99 TPC-DS witnesses, ZERO existing
implementation — no executor node, no planner producer), not sized for one
loop as-is; a future loop should re-read that design doc and either find an
already-decomposed first slice inside it or write one, mirroring this
loop's own S2b-2a/2b/2c split. Do NOT attempt M0141-S2b-2a/2b (the now-filed
plumbing-only slices) before scoping S7's own first implementable slice,
since 2a/2b's own gate is a predicted null result and they exist only to
support 2c, which is blocked on S7. Alternative fallback per the banner:
M0142's remaining open items if S7 also proves too large this loop
(M0142-0016c has no stated blocker; M0142-0005/M0142-0008a-3 need their own
recon passes first; M0142-0008c-1a/-3d/-4 are explicitly NOT ready — see
this session's earlier subagent survey). Read AGENT.md's plan-parity
harness section again before selecting (required every loop touching
M0137-M0143).

Gates run: `go build ./...` clean, `go test ./internal/optimizer/...` PASS.
TPC-DS SF0.25 sweep + plans run twice each (before/after instrumentation
enhancement) — `PASS=96 MISMATCH=0`, `PLAN-SHAPE changed=0` every time.
`make ralph-state-guard` self-repaired the same stale running/completed
mismatch seen in prior loops, then passed. No production code changed
(instrumentation fully reverted), so tpch-spotcheck does not apply; commit
went through the pre-commit hook's mandatory pgbench smoke (PASS).

In-flight: none. Private binary `tmp/goopg-s2b2-bin` removed after use. The
shared SF0.25 goopg cluster (port 65437, `bench/tpcds/runtime_goopg/data-sf025`)
was left running per this milestone's established convention for that
semi-persistent resource (same as prior c-series loops).
