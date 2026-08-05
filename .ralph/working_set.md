(idle — nothing in flight)

Last loop: **M0127-P5.7-a** — LANDED, gates green, committed + pushed.
It also decomposed P5.7 and UNBLOCKED P5.6-d.
Facts the next loop must NOT re-derive:

1. `hashJoinCost` (`internal/planner/cost_funcs.go`) now takes a
   `hashJoinInputs` struct and calls `hashsize.Choose` — the executor's own
   geometry function (`joinOp.buildGeometry`, operators_join_agg.go:624) with
   the executor's own argument shape. `NBatch > 1` applies PG's charge verbatim
   (costsize.c:4239-4248): `seq*innerPages` at STARTUP, `seq*(innerPages +
   2*outerPages)` at run. `spillPages` = `page_size` with
   `relation_byte_size` → `hashsize.EntryBytes`.
2. **M0126-0013's `seq_page_cost * innerRows/100` is GONE.** It cited
   costsize.c:4166 for a charge upstream does not make there — PG charges
   pages only under `numbatches > 1`, and for the SPILL, not the resident
   table. Do not re-add it.
3. **The finding is the width that crosses the sibling boundary.** PG passes a
   BYTE width (packed MinimalTuple); goopg's entry is a `[]Datum` of 48-byte
   structs, so size follows the COLUMN count and the executor passes
   `len(schema)`. Hence new `RelOptInfo.NCols` (leaf schema for a base rel,
   sum of inputs for a join rel) + `relNCols` / `entryNCols` accessors.
   Feeding the byte-valued `Width` here would mis-size the same build ~25×.
4. **PLAN was not run and that is not a skip.** Both `hashJoinCost` callers
   are behind OFF-by-default gates: `costJoinCandidate` only runs under
   `costDrivenJoinOrder` (bushy.go:785), and the PG-shaped DP's `pathgen.go`
   has NO `planSelect` caller at all. The default arm has zero *reachable*
   plan movement — verified structurally, then empirically by SPOT.
5. Ledgered, NOT implemented (4 rows): per-session `work_mem` never reaches
   the planner (`costParams.workMem` pinned at `hashsize.DefaultMemLimitBytes`
   = the executor's own fallback, so the two agree at the default and ONLY
   there); `spillPages` prices the in-memory footprint, not `spillWriter`'s
   narrower uvarint encoding; `nbatch` unexposed on `Path` for EXPLAIN;
   the LIMIT `tuple_fraction` → new item **M0127-P5.7-b**.

Gates run: UNITS green (`/tmp/units-p57a.log`, exit 0, zero FAIL lines,
executor re-ran at 6.17 s); SPOT `scripts/tpch-spotcheck.sh` RESULT=PASS
(`/tmp/spot-p57a.log`, Q12 rows=2, Q13 rows=35, both canonical, 27.7 s query
phase, peak 10 638 MB); commit-hook pgbench smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), the head of open M0127
work is **M0127-P5.6-d** (now unblocked — delete `costJoinCandidate`'s
`largeBuildThreshold` quadratic overshoot, which the `NBatch > 1` charge
supersedes and prices better, since 2 M rows is a fixed count while the real
threshold depends on width). Note: it lives on the `costDrivenJoinOrder` arm,
so DS05 shows no movement unless that flag is ON.

In-flight: none.
