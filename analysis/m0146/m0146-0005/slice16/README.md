# M0146-0005 slice 16 (M0146-0005p): UNION keeps its whole input as the group count

PG's `generate_union_paths` (prepunion.c) does not estimate a non-ALL
UNION's distinct groups. It takes "the number of distinct groups as equal
to the total input size, i.e., the worst case". goopg halved it.
`estimateSetOp` now returns l + r for UNION, ALL or not, which completes
the set-operation estimates M0146-0005o began.

Results: no plan changes in TPC-DS (the corpus's unions are UNION ALL; fire
set empty at both scales) or TPC-H (census identical, 5/22). Units PASS,
spotcheck PASS, the acceptance arm has 24 MATCH, and the sweep is 96/96.
The sweep and arm ran FORCE=1 during the nightly batch, so their timings
are void.
