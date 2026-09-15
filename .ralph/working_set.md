Task: M0139-0007 — absorption scoping recon (M0137–M0143 banner's TOP
PRIORITY item, per fix_plan.md's 2026-09-15 re-order). **DONE this loop,
recon-only, NO production diff** (verified: `git diff --stat -- '*.go'` empty).

Files: `docs/design/0100-0149/m0139-0007-absorption-scoping-recon.md` (new,
full inventory table), `docs/design/README.md` (+index row),
`.ralph/fix_plan.md` (M0139-0007 checked off with findings; filed
M0139-0007a and M0139-0007b as the two concrete next slices),
`.ralph/deferral_ledger.md` (+1 row, task-id `m0139-0007`).

What was found (grepped every production reference to
`hashsize.EntryBytes`/`MapSlotBytes` across `internal/optimizer`, cross-checked
each against `./postgres/src/backend/optimizer/path/costsize.c` +
`nodes/tidbitmap.c`):
- The task's own witness (hash-join spill decision) is **half-absorbed
  already**: `GOOPG_NARROW_COST_INPUTS` (R121/R128) landed the build-entry-
  footprint half default-ON since R128. The remaining **batch/spill-decision**
  half's absorption is **already built** — `hashjoin_pggeometry.go` /
  `hashjoin_pgtuplesizing.go` port PG's packed `HashJoinTuple` sizing — but
  gated behind `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108), default-off, **never
  measured** against the post-M0137–M0142 corpus.
- A parallel arm exists for **Sort's** spill decision (also feeds WindowAgg
  via `costWindow`): `sort_pgrelationbytes.go` ports PG's `relation_byte_size`
  behind `GOOPG_PG_SORT_RELATION_BYTES_COST` (R113), same never-measured
  default-off state.
- HashAggregate's width currency is **settled, already-decided** (R120 built,
  R124 measured net-neutral, M0137-0009 deleted the flag) — do not reopen
  without new evidence.
- A genuinely **new, unabsorbed** site: Memoize's entry-byte estimate
  (`joinpathsmemoize.go:133-139`) — PG's `cost_memoize_rescan`
  (`costsize.c:2541`) uses `relation_byte_size` (already ported in-tree as
  `pgRelationByteSize`, directly reusable) + `ExecEstimateCacheEntryOverheadBytes`
  (not yet ported, small named PG function in `nodeMemoize.c`).
- Bitmap heap scan's `tbmEntryBytes` is planner/executor self-consistent
  within goopg (not a PG-vs-goopg cross-currency case) — not sliced further.
- Index tuple width (`costindex.go`) not reconned in depth (no
  `hashsize.EntryBytes` reference found there; not named in the original task
  filing) — flagged only so a later recon doesn't rescan from zero.

Key symbols: `internal/optimizer/hashjoin_pgtuplesizing.go`
(`pgHashTupleSpillGeometryFor`, gate `GOOPG_PG_HASH_TUPLE_SPILL_COST`),
`internal/optimizer/hashjoin_pggeometry.go` (`pgHashGeometry`,
`pgHashSpillPages`), `internal/optimizer/sort_pgrelationbytes.go`
(`pgRelationByteSize`, gate `GOOPG_PG_SORT_RELATION_BYTES_COST`),
`internal/optimizer/cost_funcs.go` (`hashJoinCost:796`,
`costSortRunWithWidth:296`), `internal/optimizer/joinpathsmemoize.go`
(`estEntryBytes:133-139`, the M0139-0007b target),
`internal/optimizer/narrowcostinputs.go` (`GOOPG_NARROW_COST_INPUTS`, already
landed default-ON, R121/R128).

Gates run: no `.go` files touched this loop (confirmed via `git diff --stat`),
so no build/test gate was needed for the recon itself — the design doc's own
"No production diff" section records this check. `make ralph-state-guard`:
OK, consistent, no self-repair needed this time.

In-flight: none.

Next step: re-read the `## Current Priority` banner fresh (unchanged
precedence rule). M0139-0007 is now done; the banner's top-priority pairing
was "M0141-S2a-fix and M0139-0007" — M0141-S2a-fix (its own scoping pass,
still needed per the last several loops' findings — it's TWO coupled changes:
move narrowing earlier + a corrected width currency) is the other still-open
half of that pairing and is a reasonable next pick. Otherwise select
M0139-0007a (measure the two already-built R108/R113 arms — cheapest, most
concrete: machinery exists, just needs a live TPC-H/TPC-DS corpus run + a
design doc per arm) or M0139-0007b (port Memoize's currency, smaller net-new
code). Do not re-open HashAggregate's width currency (row 4, settled) or
Q9's L6 tie-break/enumeration-order thread (M0142-0003c, separate milestone,
not touched this loop).
