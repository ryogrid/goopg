# M0144-0006 — Instrumented PG 18.3: `-finstrument-functions` planner-route trace

Status: landed 2026-09-20 (hooks + GUC + arg-tag sites + captures + distiller).
Analysis record: `analysis/m0144/m0144-0006-callgraph-build.md`.

## Purpose

Instrument 3 of M0144's Phase A (0004 survivors → 0005 per-candidate
verdicts → **0006 route trace**). Answers "which route through the planner
did this query take" — `make_one_rel`'s DP search vs
`join_search_one_level` ordering vs `create_unique_path` vs a degenerate
joinlist — so the corresponding goopg code, and only that code, is the
audit surface (03-forward-plan §3).

## Design decisions

1. **`-finstrument-functions` scoped to `src/backend/optimizer/`** via
   `CFLAGS +=` in the five subdir Makefiles (`geqo path plan prep util`).
   Only optimizer functions fire the hooks — bounded overhead and volume.
2. **`debug_plan_callgraph` GUC** (`PGC_USERSET`/`DEVELOPER_OPTIONS`,
   default off) — same registration style as `debug_plan_candidates`.
3. **`optimizer/util/calltrace.c`** holds `__cyg_profile_func_enter/exit`.
   Every event writes one line to backend stdout (same channel as
   `pprint`/`PLANCAND`, so one `server.log` slice interleaves all three
   instruments):
   `CGT e <depth> <fn> <site>`, `CGT x <depth> <fn>`,
   `CGT tag <key=value ...>`, `CGT base <addr>` (PIE anchor).
4. **Raw addresses, offline name resolution.** Hooks emit `this_fn` hex —
   zero string lookup in the hot path. `scripts/pg-calltrace-distill.py`
   maps addresses through `nm -n` on the binary (statics and local clones
   included). `CGT base` emits `&standard_planner` once per backend so the
   PIE load delta is computable.
5. **No allocation in hooks.** Hooks are `no_instrument_function`, touch
   only a static depth counter + `fprintf(stdout)` (buffered — no per-line
   fflush, order with pprint/PLANCAND preserved). `cgt_tag` uses a static
   buffer.
6. **Explicit `CGT tag` markers where arguments matter** — the hook ABI
   carries no args, so three sites emit them directly:
   - `make_rel_from_joinlist` (allpaths.c): `joinlist levels=N
     route=degenerate|hook|geqo|standard`
   - `join_search_one_level` (joinrels.c): `level=N prev=<relids list>`
   - `make_join_rel` (joinrels.c): `mkjoin jt=N joinrel=(b..) outer=(b..)
     inner=(b..)` or `mkjoin illegal outer=.. inner=..`

## Gotchas recorded (build machinery)

- **`.SECONDARY:` in `src/Makefile.global` makes every target secondary**:
  a deleted `.o` is never rebuilt as a mere prerequisite — `make all`
  reports "does not exist" then "No need to remake". Rebuild after a
  flag-only change by naming objects explicitly:
  `make -C src/backend/optimizer/<dir> $(ls *.c | sed 's/\.c$/.o/')`.
  A Makefile `CFLAGS +=` likewise does NOT trigger rebuilds — the first
  pass instrumented only the three edited TUs (planner/query_planner/
  add_paths_to_joinrel silently absent from the trace until the forced
  recompile).
- **postgres is PIE**: runtime `this_fn` = load_base + static addr; the
  `CGT base` anchor is required, not optional.
- **GCC clones extern leaf functions into instrumented TUs** — local
  `list_nth_cell`/`newNode`/`castNodeImpl` copies inside `allpaths.o`
  appear in the trace as correctly-named leaf noise; the distiller's
  route set filters them.

## Verification

- `off` emits zero CGT lines; `on` leaves plans identical (Q7
  `Limit→GroupAggregate→Gather Merge` preserved).
- All 13 slices e/x-balanced (~1.4 M events, zero unwinding losses).
- Resolved route for the trivial 2-join: `planner → standard_planner →
  subquery_planner → grouping_planner → query_planner → make_one_rel →
  set_base_rel_pathlists → make_rel_from_joinlist [route=standard] →
  join_search_one_level [level=2] → make_join_rel [mkjoin tag] →
  add_paths_to_joinrel → {sort_inner_and_outer, match_unsorted_outer,
  hash_inner_and_outer}` — complete and correctly nested.

## Captures

Same 12-query set as 0004/0005 (Q23 split per statement): raw slices
`tmp/pg18-optdebug/calltrace/q*.log`, distilled route skeletons
`analysis/m0144/optdebug-0006/q*-route.txt`.
