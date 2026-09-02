# Finding — ANALYZE histograms do not survive a restart

Discovered 2026-09-02 while validating the P0-02 cost instrument against the
TPC-H bench cluster. Recorded here because it is, on the evidence below, the
**largest single planner-quality defect currently known**, and it changes how
every recorded goopg-vs-PG benchmark number should be read.

---

## 1. The observation

Against the goopg TPC-H SF=1 bench cluster (:65433, db `tpch`), for
`SELECT count(*) FROM lineitem WHERE l_shipdate < date '1995-01-01'`:

| state | goopg `rows=` | |
|---|---|---|
| fresh connection, after a server restart | **2 000 418** | = 6 001 255 / 3, exactly `DEFAULT_INEQ_SEL` |
| after `ANALYZE lineitem` in the same session | 2 582 059 | ~0.430 selectivity |
| a different connection, same server lifetime | 2 582 059 | stats are shared, not per-connection |
| after `goopg stop` / `start`, fresh connection | **2 000 418** | back to the default |

PostgreSQL's estimate for the same predicate is ~2.58 M, so the *analyzed*
goopg number is good. The unanalyzed one is the default selectivity — the
planner is estimating **blind**.

## 2. What survives and what does not

`pg_stats` for `lineitem` immediately after `ANALYZE`, and again after a
restart:

| column | `histogram_bounds` after ANALYZE | after restart |
|---|---|---|
| `l_shipdate` | 101 | **NULL** |
| `l_quantity` | 49 | **NULL** |
| `l_comment` | 101 | **NULL** |

`n_distinct` survives for all 16 columns. The relation SIZE survives — the
post-restart estimate is `6 001 255 / 3`, so `RowCount` is intact. **Only the
histograms are lost.**

This is broader than the wide-text/TOAST problem TODO P1-11 records.
`l_quantity` is `numeric` with 49 bounds and `l_shipdate` is a `date` with 101
ISO strings (~1.1 kB) — neither comes close to a heap page, so neither can be
explained by the missing TOAST path in the catalog heap writer.

## 3. Where it is not

The **restore path is not the culprit**, or not the only one.
`internal/initdb/open.go:3995-3998` restores histograms:

```go
// Histogram
if len(sr.HistBounds) > 0 {
    cs.Histogram = append([]string(nil), sr.HistBounds...)
}
```

and the surrounding code restores MCV and correlation from the same row, both of
which survive. So `sr.HistBounds` arrives empty. The next step is to determine
whether `persistStatsToPGStatistic`
(`internal/executor/operators_analyze.go:361`) writes `stakind`/`stavalues` for
the histogram slot at all, or whether it writes them and the reader's decode
drops them. **This has not been established** — do not assume the writer.

## 4. Why this matters more than the cost model

Without a histogram, every range predicate falls to `DEFAULT_INEQ_SEL` (1/3) and
every `BETWEEN` to `DEFAULT_RANGE_INEQ_SEL`. Date-window and range restrictions
are the dominant restriction shape in both benchmark suites — TPC-H Q1, Q3, Q4,
Q5, Q6, Q7, Q8, Q10, Q12, Q14, Q15, Q20 all carry one.

So on any restarted server:

- every such scan is mis-sized, typically by 2-3x;
- the error propagates multiplicatively up the join tree, which is where
  join-order choices are made;
- no amount of cost-model fidelity can recover it, because the *input* to the
  cost model is wrong.

**Every recorded goopg TPC-H benchmark figure — including the 227.0 s / 9.9x
headline in 07 §2 — was almost certainly measured on a server with no
histograms**, since the benchmark lifecycle restarts the server and nothing in
it runs an in-session `ANALYZE`. That does not make the numbers wrong, but it
means they measure a *blind* planner, and the gap attributable to planning logic
cannot be separated from the gap attributable to missing statistics until this
is fixed.

## 5. Two recorded notes this corrects

- The memory note "ANALYZE stats are per-connection" is **out of date**: stats
  are shared across connections within a server lifetime (row 3 of §1). What
  they do not survive is a **restart**.
- `CLAUDE.md` records that "`ANALYZE <table>` inside db `tpch` errors 'relation
  does not exist' (per-DB scoping gap)". That is **fixed** — `ANALYZE lineitem`
  in db `tpch` succeeded here. The deferral-ledger row `bench-reorg
  ANALYZE-scope` should be re-checked and probably closed.

## 6. Consequence for P1-11b

TODO P1-11b calls `convert_to_scalar` for non-numeric types "the highest-value
single item in Phase 1", on the reasoning that `bucketFraction` returns a flat
0.5 for dates so "every histogram interpolation on a date column lands
mid-bucket by construction".

That reasoning needs revising, in **both** directions:

- **Downward.** goopg's histogram bounds are ISO-8601 date strings, which sort
  lexicographically in date order, so the *bucket* containing the literal is
  found correctly by string comparison. Only the fraction *within* the chosen
  bucket falls back to 0.5. With ~100 buckets the worst-case error is about half
  a bucket, i.e. ~0.5% — not the 0.31-vs-0.14 error P1-11b claims. The measured
  analyzed estimate (0.430 where PG says ~0.430) is consistent with that.
- **Upward, for a different reason.** The item is moot on a restarted server,
  because there is no histogram to interpolate into at all.

So **this finding should be fixed before P1-11b is attempted**, and P1-11b's
value re-measured afterwards against a server that actually has histograms.

## 7. Bench-cluster state changed

Diagnosing this ran `ANALYZE lineitem` against the goopg TPC-H bench cluster
several times. The persisted `n_distinct`/size rows for `lineitem` are therefore
newer than they were, and the cluster was restarted repeatedly. Any A/B timing
that straddles 2026-09-02 on :65433 is confounded and must be re-based.

## 8. Resume point

1. Read `persistStatsToPGStatistic` (`internal/executor/operators_analyze.go`,
   from :361) and establish whether the histogram slot is written.
2. Read the matching decode in `internal/initdb/open.go` around `sr.HistBounds`.
3. Whichever side drops it, fix and add a test that ANALYZEs, restarts the
   catalog, and asserts `Histogram` is non-empty for a narrow column
   (`l_quantity` is the cheap case; `l_comment` additionally exercises the TOAST
   gap of P1-11 and may legitimately still fail).
4. Re-measure the TPC-H headline with histograms present, before any Phase 1
   selectivity work.
