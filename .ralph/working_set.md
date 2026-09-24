Task: M0146-0002 — `Parallel Hash` over a genuinely partial inner. Slices 1
(executor, 171c5d58a) and 2 (planner + label, cf02e88b9) landed. TPC-H
match 2→3 (Q14 now matches), SF0.25 match 2→4.

Files: internal/optimizer/joinpathsparallel.go (addParallelHashJoinPath),
cost_funcs.go (hashGeometryInputs), parallel.go (ParallelHashJoinsIn,
stamp/unstamp), gatherpaths.go (driving kind), executor parallel_hash_shared.go,
parallel_scan.go (attachParallelHashBuildSides), operators_explain.go (label).
Design: docs/design/0100-0149/m0146-0002-parallel-hash-partial-inner.md.
Raw: analysis/m0146/m0146-0002/.

Findings: Q16 still carries `parallelism` although both engines build over
`part`. Q12/Q21/Q4 gained categories (M0146-0002a, recon).

Next step: slice 3, Q16. Diff goopg's Q16 plan (the "=== Q16" section of
tmp/m0146-0002-cap/s2-tpch.plans.txt) against PG's
(s2-tpch-pg.plans.txt) and name the remaining parallel difference, then
M0146-0002a in file order.

Gates run: units; tpch-spotcheck (Q12=2, Q13=33); sf025 96/96
(changed=59); acceptance arm identical; fireset; TPC-H + SF0.25 parity
captures.

In-flight: none. Parked: tmp/m0122-alter-system-wip.patch (M0122 ALTER SYSTEM).
