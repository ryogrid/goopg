(idle — nothing in flight)

M0127-P5.4b-i is CLOSED, committed and pushed.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P5.4b-ii` (parameterised PATHS: NLI +
Memoize, 03 §5.2). Bar: UNITS + SPOT + DS05.**

P5.4b was SPLIT this loop into P5.4b-i (the 03 §9 discipline, done) and
P5.4b-ii (the paths). The tracker table, IMPLEMENTATION-TODO and fix_plan all
carry the split. The hard ordering constraint the previous baton flagged is
now DISCHARGED: `setCheapest` is param-aware, so a parameterised path may
safely enter a pathlist.

Carry-over facts a next loop should not re-derive:

- **P5.4b-ii's first sub-step is P5.1's deferred parameterised base index
  paths** — no inner can be parameterised until they exist. It is this item's
  own work, not a prerequisite someone else supplies.
- **A consequence P5.4b-i deliberately left:** a pair whose inner
  cheapest-total is parameterised by the outer now yields NO path from
  `addPathsToJoinrel`. PG reconsiders that pair through
  `cheapest_parameterized_paths` in `match_unsorted_outer`'s NLI arm
  (joinpath.c:1874-2010) — that arm IS P5.4b-ii. Unreachable today.
- **`RequiredOuter` means "what this path still needs from ABOVE"**, never
  "what it consumes below". A nested loop SUBTRACTS (discharges the inner's
  need for the outer); hash/merge UNION. `pathparam.go`.
- **PG's `ppi_rows` is `Path.Rows`, not a new field.** Cost primitives must
  read the child PATH's `Rows`, never `child.Rel.Rows`.
- **`sizeJoinRel` is STILL the open half of the `joinRelBuilder` seam** (P5.6).
  Until it lands there is no concrete builder and no `planSelect` call site;
  `GOOPG_PGSHAPED_DP` stays OFF. Do not write a stand-in sizer.
- **P4.1 ledger row #3 still open**: `mergeJoinStream.bufferGroup` keeps its
  hand-rolled twin of `materialBuffer`.
- **NL inner work_mem bound stays OFF** (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`);
  the flip needs `cost_rescan` in `costInnerNestLoop` (`joincost.go:115`) = P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is touched only at its
  IMPLEMENTATION-TODO checkboxes. Tracker = `docs/design/0127-pg-shaped-join-search.md` §6.
- **Gate recipes** — PLAN: `bench/tpch/setup_goopg.sh` (no --reset), then
  `PATH=$PWD/postgres/local_install/bin:$PATH make plan-gate`, then
  `bench/tpch/stop_goopg.sh`. SPOT: `scripts/tpch-spotcheck.sh` (own server).
  DS05: `scripts/tpcds-sf05-regression.sh sweep` (~1 h, goopg-only).

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p54bi.log`);
build + `go vet` + gofmt clean; pgbench SMOKE PASS via the commit hook.
PLAN/SPOT/DS05 not run — no `planSelect` call site and the flag is OFF, so no
plan and no row can move; P5.3's PLAN 22/22 and SPOT anchors stand.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
(fix_plan lines ~1096-1129). Nothing new to file.

In-flight: none.
