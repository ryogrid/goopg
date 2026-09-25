# M0139-0007 — absorption scoping recon: every site a goopg-native quantity reaches a PG-derived cost formula

Status: accepted
Task: M0139-0007 (recon slice only — **no production diff**, per the task's own
"Start with a scoping recon" instruction)

## Goal

Per `AGENT.md` §"Plan-parity harness" B2 (the absorption principle): where
goopg and PG differ irreducibly in representation (`Datum` 48 B vs PG's
MinimalTuple ~22 B; a Go `map[K][]Row` vs PG's pointer array), the cost model
must be given the **PG-equivalent logical quantity**, not goopg's native one.
This recon's job is narrow: **name every site** in `internal/optimizer` where
a goopg-native byte/width quantity currently reaches a formula that is
supposed to be pricing PG's representation, so a later loop can slice
absorption work one site at a time instead of rediscovering the map from
scratch. No cost formula is changed here.

## Method

Grepped every production (non-test) reference to `hashsize.EntryBytes`,
`MapSlotBytes`, and sibling per-row/per-entry byte models across
`internal/optimizer` and `internal/executor/hashsize`, read each call site's
surrounding cost function, and cross-checked against `./postgres/src/backend/
optimizer/path/costsize.c` (and `nodes/tidbitmap.c` for the one non-hash-join
site) for whether PG has a named formula the goopg quantity should be
substituted for (rule 2 of B2's two operational rules: "the quantity must
exist in PG").

## Inventory

| # | site | current currency | PG-faithful formula available? | status |
|---|------|------------------|-------------------------------|--------|
| 1 | Hash join **spill/batch decision** (`hashJoinCost`, `cost_funcs.go:796`) | `hashsize.Choose`/`MapSlotBytes` (goopg map geometry) | **Yes — already ported and wired.** `pgHashGeometry`/`pgHashSpillPages` (`hashjoin_pggeometry.go`) reproduce PG's packed `HashJoinTuple` sizing and `page_size()` (costsize.c, cited by file:line in the source comments) | **Built, default-OFF** behind `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108, "deliberately opt-in", never measured against the post-M0137–M0142 corpus) |
| 2 | Hash join **build-entry footprint** (`ncols`/`avgVarBytes` reaching `hashJoinCost`) | narrowed to the columns the build actually retains | N/A — this is the entry-footprint half of the same witness, already absorbed | **Landed, default-ON** since R128 (`narrowcostinputs.go`, `GOOPG_NARROW_COST_INPUTS`); moved TPC-H `join-method` 10→9. This is why the M0139-0007 task filing's witness ("`hashsize.EntryBytes` into the hash-join spill decision") is only half-open: the entry-footprint half is done, the batch-decision half (row 1) is not. |
| 3 | **Sort spill decision** (`costSortRunWithWidth`, `cost_funcs.go:296`) — also feeds `costWindow` (WindowAgg's internal sort, `windowsetoppaths.go`) | `hashsize.EntryBytes(ncols, avgVarBytes)` | **Yes — already ported and wired.** `pgRelationByteSize` (`sort_pgrelationbytes.go`) reproduces PG's `relation_byte_size` (`MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader)`) exactly, keyed on `pathWidth`/`Rel.Width` — a field the source comment (`path.go:686-691`) already documents as deliberately PG's packed-tuple-width currency, separate from the map-footprint model | **Built, default-OFF** behind `GOOPG_PG_SORT_RELATION_BYTES_COST` (R113, same never-measured-since state as row 1) |
| 4 | **HashAggregate width currency** (`hashAggEntrySize`, `cost_funcs.go` ~490-515) | variable payload only (`inAvgVarBytes`), while the competing Sort/GroupAgg rival is priced on the full `hashsize.EntryBytes` currency (row 3) | Yes in principle (`hash_agg_entry_size`, costsize.c:2801-2802) | **Attempted and deliberately reverted.** R120 built a corrected currency behind `GOOPG_HASHAGG_WIDTH_CURRENCY`; R124 measured it paired with ncols-narrowing and found it identical to the currency fix alone (hypothesis refuted); M0137-0009 deleted the flag. The source comment calls the residual divergence "a KNOWN, currently-accepted PG divergence, not an oversight." **Do not reopen without new evidence** — this is settled project history, cited here only so a later loop does not rediscover and re-litigate it. |
| 5 | **Memoize entry-byte estimate** (`estEntryBytes`, `joinpathsmemoize.go:133-139`) | `hashsize.EntryBytes(ncols, 0)*tuples + hashsize.EntryBytes(nkeys, 0)` (goopg map-entry currency) | **Yes, and partially free.** PG's `cost_memoize_rescan` (`costsize.c:2541-2578`) computes `est_entry_bytes = relation_byte_size(tuples, width) + ExecEstimateCacheEntryOverheadBytes(tuples)`, plus the cache keys' own width. `relation_byte_size` is **already ported** in goopg as `pgRelationByteSize` (row 3's machinery) — reusable directly. `ExecEstimateCacheEntryOverheadBytes` (PG's `nodeMemoize.c`) is not yet ported; it is a small named function, so the "quantity must exist in PG" rule is satisfiable. | **Unabsorbed, no prior attempt found.** Newest, most concrete candidate — half the machinery already exists in-tree. |
| 6 | **Bitmap heap scan entry sizing** (`tbmEntryBytes`, `costbitmap.go:235-237`) | `bitmapWords + 64`, explicitly documented as mirroring goopg's *own executor* `tbmCalculateMaxEntries` (`executor/tidbitmap.go`) | PG has `tbm_calculate_entries` (`nodes/tidbitmap.c:1545`, `sizeof(PagetableEntry) + 2*sizeof(Pointer)`) but it sizes **PG's own** `PagetableEntry` struct — a different concrete byte count from goopg's `bitmapWords=256` layout (goopg's TIDBitmap representation is itself not byte-identical to PG's) | **Not a cross-currency divergence in the B2 sense.** Planner and executor already agree with EACH OTHER here (that is what the comment states the code is for); the goopg-vs-PG gap is a representational difference in the *executor's own* bitmap struct, not a case of a goopg-native quantity leaking into a PG formula that expects a different currency. Lower priority; not sliced further here. |
| 7 | **Index tuple width** (`indexTupleWidth`, `costindex.go:597`) | derived from `catalog.Index`/`catalog.Table` | — | Not reconned in depth (not named as a candidate in the M0139-0007 task filing, and grep found no `hashsize.EntryBytes`/`MapSlotBytes` reference in `costindex.go`). Flagged only so a later recon does not need to re-scan this file from zero. |

## What this means for slicing

Two concrete, bounded next slices fall out, both consistent with B2's two
operational rules (derive-before-measure; quantity-must-exist-in-PG):

- **M0139-0007a** — re-measure rows 1 and 3 (`GOOPG_PG_HASH_TUPLE_SPILL_COST`,
  `GOOPG_PG_SORT_RELATION_BYTES_COST`) against the current TPC-H/TPC-DS corpus.
  Both arms' derivation already exists in the source (file:line citations into
  `costsize.c`), predating B2's formalization but satisfying it in substance;
  what has never happened is a measurement of their effect on the plan-parity
  metric post-M0137–M0142. Decide adopt/hold per the metric, one design doc
  per arm, following the `GOOPG_GATHER_PATHS` promotion precedent
  (`docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md`).
  Measure independently before considering them together — row 3 feeds row 1's
  competing Sort-based plan shapes, so a combined flip could move a plan for a
  reason neither arm alone explains.
- **M0139-0007b** — port PG's `relation_byte_size` (reuse `pgRelationByteSize`,
  already built) and `ExecEstimateCacheEntryOverheadBytes` into Memoize's
  entry-byte estimate (`joinpathsmemoize.go`). New absorption, no prior
  attempt exists for this site.

Row 4 (HashAggregate) is explicitly **not** re-opened by this recon — it is
settled, evidenced history, and reopening it needs new evidence, not a
scoping pass rediscovering the same trade R124 already measured.

## Ledger

See `.ralph/deferral_ledger.md` (task-id `m0139-0007`): the PG behavior this
recon found unabsorbed (Memoize's `cost_memoize_rescan` currency; the two
built-but-unmeasured hash-join/sort arms) is recorded there with the resume
points above.

## No production diff

Confirmed: this task touched no file under `internal/`. `go build ./...` was
not re-run since nothing changed; the inventory above was built entirely by
reading existing code and `./postgres` source, per the task's own scope
boundary.
