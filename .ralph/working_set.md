Task: M0146-0002 — `Parallel Hash` over a genuinely partial inner (item 3's
M0146 continuation; M0145-0001 lineage is [!], so M0146-0001 is held and
this is the first selectable M0146 task in file order).

Files: slice 1 landed as 171c5d58a. internal/executor/parallel_hash_shared.go
(barrier, participant protocol), parallel_scan.go (hashBuildBranch claim
sets), operators_gather*.go (register/retract), plus optimizer.Join.ParallelHash.
Design: docs/design/0100-0149/m0146-0002-parallel-hash-partial-inner.md.

Key symbols: parallelHashBuild{attach,finish,wait,merge};
openParallelHashJoin; registerParallelHashBuilds;
cs.attachParallelHashBuildSides; addPartialHashJoinPath (slice 2 target).

Findings: the executor model is proven (identity 5 join types under -race,
5 builders). "Spilled" means nbatch > 1, because a batch state is always
installed.

Next step: slice 2, the planner. In internal/optimizer/joinpathsparallel.go
add the parallel_hash = true arm: read inner.PartialPathlist and price per
costsize.c initial_cost_hashjoin/final_cost_hashjoin with parallel_hash.
Restrict to INNER/SEMI/ANTI/LEFT-build-right and an inner that fits
hash_mem. Stamp Join.ParallelHash in createPlan and render EXPLAIN
`Parallel Hash Join` / `Parallel Hash` (take PG's Q14 text from the PG
TPC-H reference cluster, EXPLAIN only). Then the full gate set plus the
parallel TPC-H capture (Q14/Q16 witnesses).

Gates run (slice 1): units; tpch-spotcheck; tpcds-sf025 (same=99);
acceptance arm; fireset.

In-flight: none. Parked: tmp/m0122-alter-system-wip.patch (M0122 ALTER SYSTEM).
