# M0146-0005 slice 14 (M0146-0005m): nested loops over every outer path

`match_unsorted_outer` (joinpath.c) builds nested loops over every path in
`outerrel->pathlist`. JOIN_UNIQUE_OUTER is the exception and keeps only
the cheapest-total outer, unique-ified. goopg used only the cheapest-total
outer.

The first attempt (`../recon-0005m/`) regressed Q13, Q48 and Q91, because
join paths kept their outer's whole ordering. Slice 13 (M0146-0005n) ported
`truncate_useless_pathkeys`. Re-applied on top of it:
- `nestLoopOuterPaths` (joinpathsnli.go) gives every unparameterised outer
  path, deduplicated.
- `addNLIPaths` and `addNestLoopPath` / `addNestLoopPathFor` (pathgen.go)
  loop over it.
- `TestNestLoopTriesEveryOuterPath` pins the rule.

## Results

- `census-diff-*.txt`: empty. Nothing regresses at either scale.
  `q44-plans.txt`: Q44's join tree is now exactly PG's (two ordered
  nested loops into `item_pkey` over the rank merge join, no Sort under
  the Limit). Its census record stays at the same node on a
  parameterisation-rendering difference.
- `tpcds-fireset.txt`: fires Q4, Q11, Q22, Q31, Q44, Q62, Q64, Q77, Q99.
  No timeouts.
- `sf025-sweep-FORCE-nightly-running.txt`: 96/96 on values. The nightly CI
  batch was running, so the sweep and the TPC-H acceptance arm ran with
  FORCE=1 (values-only). Their timings, +20.8 % total, are void. The first
  attempt measured −4.2 % total without the nightly load.
- TPC-H: 5/22, census identical, spotcheck PASS, the acceptance arm has 24
  MATCH on values.
