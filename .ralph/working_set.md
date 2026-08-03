(idle — nothing in flight)

M0127-P4.4 is CLOSED, committed and pushed. **All four P4 tasks have landed;
S4's stage exit is NOT met** (§3 also wants the regress-port outer-join files
green, which stays gated on P4.2's `GOOPG_HASH_OUTER_JOIN` flip = P5).

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — `M0127-P5.1` (joinrels level lists + relset map over `RelOptInfo`;
`buildInitialRels` incl. `PathPrebuilt` leaves — also closes M0125-0037 stage
(ii)). IMPLEMENTATION-TODO P5.1; 03 §1-§2. Bar: UNITS + PLAN (default arm ZERO
diffs — the task lands dark behind `GOOPG_PGSHAPED_DP`).**

Carry-over facts a next loop should not re-derive:

- **`joinOp` no longer has `rows`/`idx`/`leftCTIDs`/`rowSourceLeft`.** Every
  arm of `Next` streams; a joinOp with no `*Stream` set returns EOF. Tests that
  asserted "output did not accumulate" by reading `o.rows` had to move to the
  stream's own counters (`join_merge_stream_test.go` now checks
  `mergeStream.steps`).
- **LATERAL correlation binding is per-CALL, not per-iteration**
  (`bindOuter`/`unbindOuter`, `join_lateral_stream.go`). This is load-bearing:
  a streaming inner side yields to the PARENT between tuples, so a held
  `ctx.OuterRows` push would capture the parent's own `OuterColumnRef`.
  `CTERowCache` rides the same window.
- **P4.4 ledger row #1 (PARAM_EXEC rescan) is what blocks Materialize/Memoize
  under a LATERAL RHS.** goopg re-Opens/Closes the whole right subtree per
  outer tuple because there is no changed-param signal. → P5.4.
- **P4.1's ledger row #3 is STILL OPEN**: `mergeJoinStream.bufferGroup` keeps
  its hand-rolled twin of `materialBuffer`.
- **NL inner work_mem bound stays OFF** (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`) on
  DS05 Q54 measurement; the flip needs `cost_rescan` in `costInnerNestLoop`
  (`internal/planner/joincost.go:115`) = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
  `operators_join_agg.go` is gofmt-dirty AT HEAD under the local tool; verify
  you added none with the before/after drift diff (see this loop's method).
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is only ever touched
  at its IMPLEMENTATION-TODO checkboxes. Tracker = `docs/design/0127-pg-shaped-join-search.md` §6.

Gates run this loop: UNITS PASS; SPOT PASS (Q12=2 / 15.8 s, Q13=35 / 11.4 s,
query phase 28.7 s, peak 11,662 MB); DS05 PASS=94 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=1 (Q72, pre-existing) SKIP=4 — all 94 passing queries
byte-identical to the P4.3 sweep in row count AND checksum, no query slower by
>20%, total 2310 s → 2332 s
(`analysis/leftdeep-joins/2026-08-04-p44-ds05-sweep.txt`); SMOKE via the commit
hook. REGRESS not run — no plan surface changed (no new plan node, no EXPLAIN
line, no planner edit). RACE not run — no new shared state (the stream is
per-joinOp; the only ctx mutation is the push/pop already done by the eager
path).

In-flight: none.
