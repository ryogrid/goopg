# M0146-0005 slice 7 (M0146-0005f): nested loops with a unique inner

PG's `final_cost_nestloop` takes its semi/anti early-exit branch for an INNER
join whose inner rel is proven unique (`extra->inner_unique`). It uses the
factors slice 6 observed: `outer_match_frac` is the inner-join selectivity
and `match_count` is the inner's row count.

For a parameterised index probe into a primary key, `has_indexed_join_quals`
holds. Almost every outer row is then "unmatched", so it pays
`inner_rescan_run_cost / inner_rows`, and `ntuples` is about 0. PG therefore
drops `cpu_tuple_cost` on the join's output rows. A Memoize inner is not
"indexed", so its cost barely moves. See `goopg-q31-ws-nestloop-dppath.txt`:
the Memoize nested loop is 11597.23 against 11597.26 before, and the plain
index nested loop is 66496.7 against 68296.5.

goopg now builds the factors once per pair (`joinpaths.go`) from
`innerRelProvenUnique` (the uniqueness proof the hash join already used,
skipping non-key clauses) and `innerUniqueMatchFactors`. Unique-ified pairs
are left alone, because PG computes their factors with the SEMI
SpecialJoinInfo.

Results:
- `plan-changes.txt`: 6 structural plan changes per TPC-DS scale. The
  first-divergence census is unchanged. Q21's changed subtree is now PG's.
- `sf025-sweep.txt`: 96/96 PASS. The one runtime move (Q2) has an unchanged
  plan.
- `tpcds-fireset.txt`: no introduced timeouts.
- `tpch-plan-parity.txt`: TPC-H 5/22, census identical. Spotcheck PASS; the
  acceptance arm has 24 MATCH on values.
