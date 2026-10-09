# M0146-0007 slice 2 (M0146-0007b): a qual pushed into an inlined CTE moves

PG's subquery_push_qual moves a qual into the inlined subquery and leaves
no copy above it. goopg copied it and kept the outer Filter, so TPC-DS
Q78 printed `Subquery Scan on ss  Filter: (ss_sold_year = 1998)` for all
three channel CTEs.

## Change

- `pushConjunctIntoCTEBodyTraced` threads the C-02c move proof
  (`pushTrace`) through the body.
  - A projection hop keeps the proof when its layout checks hold.
  - A grouping-key crossing keeps it, except over grouping sets: rollup
    rows have NULL keys that only the outer copy rejects.
  - Sort, Gather Merge and HAVING are passthroughs.
  - The join-tree descent is `pushConjunctTraced`.
- `pushConjunctTraced`'s `*Project` arm keeps the proof only for a
  CTE-path descent (`pushTrace.cteMove`) whose ColumnRefs are all named.
  `remapConjunctThroughProjection`'s positional name check then ran for
  every reference, which closes the unnamed-ref seam that kept this arm
  placement-only. The join pass does not set `cteMove` and is unchanged.
- `pushFilterQualsThroughCTEScan` drops a proven conjunct (not planted by
  a sibling derivation) from the residual. An emptied residual becomes
  the transparent `true` wrapper.

## Results

`q78-residual.txt`: `ss` and `cs` no longer print a `Subquery Scan`.
`ws` still does: its body's join is a `NestedLoopIndexJoin`, which the
descent does not enter (ledgered). Q78's first-divergence record is that
`ws` arm, so the census is unchanged; SF0.25 join-method drops 49 -> 48
(fire-set CATEGORIES-EXCL-MATCH). Row counts, TPC-H and regress are
unchanged (`gates.txt`).
