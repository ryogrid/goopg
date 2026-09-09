# R38 status — REJECTED on review, and then refuted by measurement

Design reviewed: **REJECT as written**. Two independent corrections
followed, the second of which says the round cannot achieve its own
primary target. **Nothing implemented.** Tree green at HEAD.

## Correction 1 (review): the design targeted the wrong field

§3 proposed narrowing `RelOptInfo.Width`. That would have been a
measured no-op on the round's own target:

- `pathNCols` / `pathAvgVarBytes` (`path.go:634-658`) read `NCols` and
  `AvgVarBytes` — **never `Width`**.
- The spill term is a function of COLUMN COUNT, not bytes:
  `EntryBytes = 48*ncols + 24 + avgVarBytes`
  (`hashsize/hashsize.go:144-152`, `DatumBytes=48`, `RowSliceBytes=24`).
- The 641 MB reconciles as `1.5M x (48x9 + 24) = 652 MB` from `orders`'
  **9 columns**, not from `width=448`.
- EXPLAIN's `width=` is `TupleWidth(n.Output())` (`plancost.go:100`) —
  the built node's schema. A symptom, not the costing input.

Correct target would be `RelOptInfo.NCols` + `AvgVarBytes`.

Other review findings worth keeping:

- **§6.2 hazard REFUTED.** The executor sizes its table from the
  RUNTIME schema (`operators_join_agg.go:607-609`), never from
  `RelOptInfo`. Narrowing planner width cannot under-size the real
  table.
- **§6.1 refuted.** Nothing derives a schema or layout from
  `RelOptInfo.Width`. But `considerparallel.go:567` feeds it to
  `estScanPages` as a PAGE COUNT, which PG never does — narrowing it
  there would understate I/O.
- `joinKeepSet`/`buildKeepSet` are structurally post-plan (they need a
  built node). The node-free piece is
  `neededKeepSet(leaf.Output(), rel.NeededCols)`
  (`narrowoutput.go:789-806`).
- Correct placement is between `relfromjoinlist.go:699`
  (`stampNeededColsOnRels`) and `:707` — exactly PG's `set_rel_width`
  position — with the base seq-scan path re-costed
  (`joinsearch.go:433-438`).
- Do NOT mutate `RelOptInfo.AvgVarBytes`/`ColVarBytes` in place:
  `entrywidth.go:53-56` uses them as the executor's deliberate
  over-charge fail-safe.
- **PG order CONFIRMED**: `set_rel_size` (allpaths.c:322) completes
  before `set_rel_pathlist` (:351); width is final before any path is
  costed.

## Correction 2 (measurement): the retargeted round still would not work

Applying the correct model to the correct target:

| | B/row | 1.5M `orders` | at `work_mem=64MB` |
|---|---|---|---|
| goopg, full 9 columns | 456 | 652 MB | spills |
| goopg, **narrowed to 1 column** | **72** | **103 MB** | **still spills** |
| PG | 22 | 31 MB | fits |

Perfect column pruning takes goopg from 456 to 72 B/row — a real 6.3x
win — and it is **still 3.3x above PG**, so the `orders` build still
multi-batches and PG's build side stays priced out. **Q12's divergence
would not flip.**

The residue is goopg's in-memory row representation: 48 bytes per
column (`DatumBytes`) against PG's whole-tuple MinimalTuple of ~22
bytes. No amount of column pruning closes that.

## Conclusion: K65 is necessary but NOT sufficient

Q12's build-side divergence needs BOTH:

1. **column pruning visible to the cost model** (K65, this round's
   corrected target), and
2. **a smaller per-column footprint** — which is the existing
   `docs/design/not_ralph/minimize_datum/` workstream, already in
   flight and tracked separately.

Filing K65 as blocked-on-2 rather than implementing a round whose own
prediction (§4: "the spill term disappears") is now known to be false.
Landing it alone would produce shape churn on both corpora while
leaving the target query unmoved — the worst outcome to attribute
later.

## Resume point

Re-scope K65 as "make column pruning visible to costing" on its own
merits — it is PG-faithful and a prerequisite — but predict and gate it
as a 6.3x width reduction, NOT as a fix for Q12. Check first whether
`minimize_datum` has moved `DatumBytes`; if the two land together the
combined effect is 456 -> 72 -> near PG's 22, and only then does the
build side flip.
