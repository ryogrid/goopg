Task: M0142-0008a-3i-plumbing-c8 — LANDED and committed.
`problemPairsOuterWithDerived`'s derived-input guard was misclassifying a
semiAnti link's own multi-relation RHS leaf as "no statistics" purely
because it has no single base table. Fixed with a provenance flag (not
either candidate direction c7 filed literally — both failed on inspection,
see below), verified with a real regression test + controlled
fail-then-pass check, then root-caused the NEXT blocker (c9) as far as
static inspection allows without instrumentation.

Files this loop:
- internal/optimizer/cardinality.go: `baseRelInfo` gains
  `isSemiAntiSyntheticLeaf bool` (new field, doc comment explains it).
- internal/optimizer/joinsearchseam.go: the semiAnti synthetic-leaf loop
  (`for k := range semiAnti`, ~line 665-686) now sets
  `isSemiAntiSyntheticLeaf: true` on that leaf's `baseRelInfo` literal —
  the same site that already prices it via `EstimateRows(scan)` instead of
  a catalog lookup.
- internal/optimizer/relfromjoinlist.go: `leafIsDerivedInput` (~line 522)
  checks `info.isSemiAntiSyntheticLeaf` FIRST, returns `false` immediately
  if set, before the Filter/Project-unwrap loop and the
  CTEScan/WorkTableScan type switch.
- internal/optimizer/semiantichain_test.go: new
  `TestProblemPairsOuterWithDerivedSemiOverMultiRelationRHSDoesNotDecline`
  — a Semi sj whose RHS item has the flag set must NOT decline. Verified
  FAILS on pre-fix code (temporarily gated the check behind `if false &&
  …`, confirmed the predicted failure, then restored the real fix).
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §43 —
  why neither candidate (a) [fragile EstimateRows-value signal] nor
  literal (b) [bitmask hand-exemption breaks the 2 pinned CTE tests]
  survived (§43.1), the actual fix (§43.2), a residual gap deliberately
  NOT fixed this loop (§43.3, nested CTE inside a multi-relation semiAnti
  RHS body — no corpus query hits this), verification (§43.4-43.5), and
  §43.6's root-cause lead for the next blocker (c9).
- docs/design/README.md: m0142-0008a-1 row tail extended (Python exact-
  string-replace on the c7-row anchor — do NOT full-file-rewrite this row,
  it has embedded raw newlines/quotes).
- .ralph/fix_plan.md: `-3i-plumbing-c8` flipped `[x]`; new
  `-3i-plumbing-c9` filed (find what blocks `jointype=semi`/`anti` now
  that Q69 clears every seam gate).
- .ralph/deferral_ledger.md: new row, task-id `m0142-0008a-3i-plumbing-c8`
  (covers both the c9 handoff AND the nested-CTE residual gap).

Key symbols: `isSemiAntiSyntheticLeaf` (new field, cardinality.go
baseRelInfo struct); `leafIsDerivedInput` (relfromjoinlist.go:522, now
checks the flag first); the semiAnti synthetic-leaf loop in
`tryPGShapedJoinSearch`-adjacent code (joinsearchseam.go ~line 665-686,
sets the flag); `(*searchCtx).joinIsLegal` (joinsearchlevel.go:197) — the
NEXT blocker's prime suspect, already has a SEMI "unique-ified RHS" arm
calling `createUniquePath` (createuniquepath.go, landed as M0142-0008c-1/-2
independently of this c-series, confirmed via `git log` to exist NOW even
though the M0142-0008c recon row said it didn't back on 2026-09-16 — that
recon finding is stale, don't trust it without re-checking); `buildInitialRels`
(joinsearch.go:434, wraps every leaf incl. the synthetic one in
`PathPrebuilt` via `newPrebuiltPath` — by inspection this satisfies one of
createUniquePath's 3 preconditions, NOT traced with instrumentation).

Findings: (1) c8's fix is real — full-corpus sweep still shows
`semianti-on-qual`=0 (unchanged from c7) and NEW: `outer-over-derived` is
gone from Q69's own decline set entirely (the 3 corpus-wide occurrences
that remain are a DIFFERENT, unrelated query, confirmed by isolating Q69
alone and seeing zero of that reason). (2) Per-query isolation on Q69
alone: ZERO semiAnti-related or derived-input seam-declines — only ordinary
DP-search noise (`reason=illegal`x22, `reason=no-join-clause`x6,
`reason=strategy-or-mode`x1), the same channel every other query's search
also emits while exploring join orders, not semiAnti-specific. This is
qualitatively different from c5-c7's findings: Q69 no longer has ANY named
seam-level blocker left to fix — the remaining gap is somewhere in
ordinary DP-search legality/costing, not a firewall this milestone's
c-series built. (3) `jointype=semi`/`anti` DPPATH is STILL zero, both
corpus-wide and for Q69 specifically — reachability is NOT achieved this
loop, only every seam-level precondition for it. (4) Checked
`(*searchCtx).joinIsLegal` directly (not instrumentation): it already has
real production code (not a stub) for a SEMI "already unique-ified, treat
as ordinary inner join" admission arm via `createUniquePath`, landed as
M0142-0008c-1/-2 at some point AFTER the M0142-0008c recon row (which said
this mechanism was entirely absent) — did not confirm whether this is
what Q69 needs. IMPORTANT correction to keep straight: Q69 is `customer,
customer_address, customer_demographics WHERE … EXISTS(store_sales JOIN
date_dim …) AND NOT EXISTS(web_sales JOIN date_dim …) AND NOT
EXISTS(catalog_sales JOIN date_dim …)` (design doc §41.3) — ONE Semi link
(the EXISTS) plus TWO Anti links (the two NOT EXISTS), not uniformly one
or the other. `createUniquePath` explicitly requires `sjinfo.Jointype ==
parser.JoinSemi`, so it can only ever apply to Q69's FIRST link; the two
Anti links structurally cannot reach it regardless of what else is fixed.
Whether the Semi link's own pairing is what's still blocked, and whether
the two Anti links have some entirely different (not yet identified)
blocker of their own, are both open — do not assume a single fix covers
all three links. (5) Verified
the flag-based fix cannot regress the two existing pinned CTE-on-RHS tests
because they build `relInfos` directly in the test without ever setting
the new flag or going through the synthetic-leaf construction site.

Next step: pick up **M0142-0008a-3i-plumbing-c9** — per finding (4),
`createUniquePath`'s SEMI-only gate can only ever matter for Q69's ONE
Semi link (the EXISTS), never its two Anti links (the two NOT EXISTS), so
do not assume fixing/confirming one gives you all three. Also check
whether the search DOES admit the pairing but costing simply never
prefers it (a different class of task than an admission bug — grep DPPATH
for `jointype=anti`/`semi` with `verdict=` to see if any such candidate is
ever OFFERED at all, dominated or not, rather than assuming zero DPPATH
lines means zero offers were even attempted). Start by instrumenting Q69's
specific pairing attempts in `joinIsLegal` (env-gated, throwaway, revert
before commit per this milestone's established discipline) — static
reading has likely exhausted its value for this specific blocker.

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` green (13/13 TestProblemPairsOuterWithDerived*
cases including the new one; includes a controlled fail-then-pass check
against the pre-fix code before committing). Full-corpus
`GOOPG_PGSHAPED_DP_TRACE=1` sweep (private binary `tmp/goopg-m0142-c8-bin`,
built+removed this loop) — 96/100 EXPLAINs succeeded (4 pre-existing
unrelated parse gaps, unchanged), 150,255 DPPATH lines, 0
`jointype=semi`/`anti`, seam-decline reasons: `semianti-link-no-sjinfo`=3
(unchanged), `semianti-on-qual`=0 (unchanged from c7), `outer-over-derived`=3
(now a different, unrelated query — confirmed via per-query isolation that
Q69 contributes zero of these). `scripts/tpcds-sf025-regression.sh sweep`
(private binary) PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0, PLAN-SHAPE
same=99 changed=0 vs the c7 commit (Q78 byte-identical, checksum unchanged
since c2). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` —
same pre-existing `internal/parser` `GroupedJoinUnaliased` AST-drift
failure as every recent loop (`internal/optimizer` itself green,
re-confirmed with a direct `go test ./internal/optimizer/...` run). `make
ralph-state-guard` auto-repaired the same benign prior-loop clean-exit
marker seen every recent loop, then PASS. Commit's own pre-commit hook
runs the pgbench smoke.

In-flight: none. Private trace binary and sf025 server both stopped/removed
(`GOOPG_BIN=tmp/goopg-m0142-c8-bin bench/tpcds/server.sh stop sf025`, then
`rm tmp/goopg-m0142-c8-bin`).
