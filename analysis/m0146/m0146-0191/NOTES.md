# M0146-0191 — Q38/Q87 partial DISTINCT record: a cost tie inside PG itself

Record: TPC-DS SF0.25 Q38, `depth=5 [sort-strategy] under Gather Merge: PG Unique | goopg Sort`. On the
reference cluster (:65438), PG runs a per-worker Unique below the Gather Merge on the web_sales branch.

## Finding: PG flips between the two shapes with its statistics sample

Each INTERSECT/EXCEPT branch has two candidates:

- **unsplit:** `Unique -> Gather Merge -> Sort`;
- **split:** `Unique -> Gather Merge -> Unique -> Sort`, from create_partial_distinct_paths.

The per-worker Unique estimates no reduction (rows in = rows out), so the two differ only by the worker Unique's
comparisons, about 0.03% of the branch cost. Which one PG keeps depends on its ANALYZE sample:

| query / branch | :65438 (reference sample) | tmp/pgdbg (own sample, db tpcds025) |
|---|---|---|
| Q38 web_sales | split (8918.69..9031.81, 890 rows) | unsplit (8912.77..9019.01, 857 rows) |
| Q87 store_sales | unsplit (3260 rows) | split (19351.12..19768.50, 3255 rows) |

goopg prices the Q38 web_sales pair at 8859.03..8977.08 (split) and 8859.03..8974.17 (unsplit), 2.9 apart.

## Ruled out: M0146-0190's scan/join admission

The lead was that the unsplit arm in `addPartialDistinctPaths` skips the scan/join add_path contest. Gating that
arm on `scanJoinRelKeepsGatherMerge` changed nothing:

- goopg's searched rel for the branch holds only a serial Nested Loop, a Gather and the search's own Gather Merge.
  There is no sorted non-gathered candidate, because the distinct keys come from customer, the top inner, so no
  lower rel can carry them.
- The rel does not consider startup, since there is no LIMIT inside the branch.
- The fire set was identical at both scales (SF0.25 79/99, SF1 65/99). The edit was not landed.
