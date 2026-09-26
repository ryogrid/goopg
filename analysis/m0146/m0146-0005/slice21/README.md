# M0146-0005 slice 21 (M0146-0005u): grouping inputs read the searched rel's cheapest-total path

TPC-DS Q22 (SF0.25) was a `join-method` record at depth 5: PG elects a
**Parallel Hash Join** for `inventory ⋈ date_dim` beneath a costed
`Gather`, goopg elected a serial memoized-NLI subtree and then
re-parallelised it with the post-pass `MaybeAddGather`.

## Cause

Not a missing or mispriced candidate — the parallel hash arm was
offered, accepted and strictly cheaper (DPPATH: `gather` total 27443 vs
the fractional serial NLI 96692 at the joinrel). The boundary commits
the searched subtree to `finalPath` — the `get_cheapest_fractional_path`
pick — so under `LIMIT 100` the committed node is startup-optimal, not
total-optimal. PG never makes that choice twice:
`add_paths_to_grouping_rel` reads `input_rel->cheapest_total_path` from
the live rel (planner.c:7122; the same read the sorted arms make at
:7460 and the hashed arm at :7584). goopg's `createGroupingPaths` built
every arm's seed over the committed subtree instead, so the gathered arm
re-parallelised the serial NLI (inputtotal 33316 ≈ 96692/3.1 + gather
overhead) while the rel's own cheapest-total path (27443) never stood in
the comparison.

## Change

`internal/optimizer/searchedtree.go`:

- `searchedCheapestTotalInput(child)` — descends the `*Project`/`*Sort`
  pass-through chain above the agg input's searched root, and when the
  stamped rel's `CheapestTotal` is strictly cheaper than the committed
  subtree's stamped cost, rebuilds a node over that path through
  `searchedBoundaryRebuild` (layout coverage pre-check, then the real
  `createPlanAtSearchRootRange` boundary, plus an output-schema compare)
  and splices it back under the same wrappers. Everything it cannot
  promise — no searched root, no cheapest-total path, equal-or-dearer
  total, a row the boundary cannot reproduce, a lowering panic —
  declines and the committed input stands.
- `createGroupingPaths` (groupingpaths.go) swaps `seed`/`child` to the
  rebuilt input for `addGroupingPaths` only.
  `addPartialAggSplitPath` keeps the committed child: its pseed derives
  `parallelSeedCost(seed.Cost, d)` — serial-subtree currency — and the
  rebuilt input's Gather arm already covers the case the split would
  re-derive.

## Results

- Q22 SF0.25 plan is now `Limit -> Sort -> MixedAggregate -> Gather ->
  Nested Loop -> Parallel Hash Join(Parallel Seq Scan inventory,
  Parallel Seq Scan date_dim) -> Index Scan item_pkey` — PG's shape.
  PG additionally prints a `Parallel Hash` wrapper on the date_dim build
  side; the census normalises it. `q22-goopg-final.txt` /
  `q22-pg-sf025.txt`.
- Result values identical to PG 18.3 SF0.25, all 102 rows
  (whitespace-normalised diff, `tmp/m0146-0005-q22/q22-rows-*.txt`).
- First-divergence census, SF0.25: Q22 `depth=5 [join-method]` →
  `MATCH`; every other record identical (`census-sf025-after.txt` vs
  `census-sf025.txt`; symmetric same-clone arm
  `census-sf025-prev-arm.txt` confirms Q22 is the only delta).
- TPC-DS SF1 (fireset captures): Q22 moves to `Gather -> Hash Join(item)
  -> Parallel Hash Join(inventory,date_dim)` — one divergent category
  short of match (PG files a `Parallel Hash Join` outer); join-method,
  scan-type and parameterisation each drop one. Q49 is cost-digits
  drift only.
- Churn: goopg-vs-goopg self-diff on the same private clone —
  TPC-DS SF0.25 94/99 MATCH, the only SHAPE-DIFF is Q22 itself
  (`sf025-selfdiff.txt`); TPC-H SF1 22/22 MATCH (`tpch-selfdiff.txt`,
  `estimate-audit -plan-only` arms on the same clone).
- Unit pin `TestSearchedCheapestTotalInputRebuildsADisplacedInput`
  (searchedtree_test.go): undisplaced/nil/short-emission decline;
  displaced rebuild through a boundary *Project* keeps the searched tag,
  the stamped rel and the identical row; splice under `*Sort`.

## Q73 panic — pre-existing latent bug, NOT this change

The SF0.25 capture hit a backend panic on Q73:
`assertSearchedTreeNeedsNoReconcile` — name resolution moves
`ss_ticket_number` col 9→0 in a 2-rel `searchOneProblem` root build
(`relfromjoinlist.go:721` → `createplanroot.go:142`). Reproduces
deterministically on BOTH the candidate binary and clean HEAD 3a5fbe5bd
on this private clone (`q73-panic-serverlog.txt` — two panics,
02:06:11 new binary, 02:06:41 clean HEAD). The same Q73 EXPLAINs fine
on the live `:65437` cluster running the candidate binary, so the
trigger is this clone's autovacuum-drifted statistics, not the diff —
the assertion is a latent searched-tree bug a particular stats state
reaches. Filed in the deferral ledger; the clone at
`tmp/m0146-0005-q22/data` preserves the reproducing stats state.

## Gates

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS
- `scripts/tpch-spotcheck.sh` — PASS stamped
- `scripts/tpcds-sf025-regression.sh sweep` — PASS stamped
  (PASS=96 MISMATCH=0; the first run's server was OOM-killed under
  three concurrent heavy gates — re-run alone clean)
- `tpch-acceptance-arm` vs `tmp/m0145-0008m/arm-on.txt` — PASS stamped
  (24/24 value-identical)
- `scripts/tpcds-fireset-gate.sh` — PASS stamped (fire sets
  SF0.25 {Q22 Q53 Q63}, SF1 {Q22 Q49}, all execute PASS)
- `go test ./internal/optimizer/` — PASS

Full detail: `gates.txt`; fire-set artifacts: `fireset/`.
