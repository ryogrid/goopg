# M0125-0010 acceptance — FROM-subquery Project remap

Fix: `remapSubqueryColumnRefs` is now verify-then-repair (see §9 of
`docs/design/0125-0009-parser-expr-key-structural.md`).

## SF=1 value acceptance (goopg :65436 vs PostgreSQL 18.3 :65438)

`{goopg,pg}_q<N>.txt` are the raw `psql -At` outputs. Comparison is byte-for-byte
and, where that differs, again with `char(n)` blank padding normalised
(`sed 's/ *|/|/g; s/ *$//'`) — that padding gap is a separate recorded defect
(M0124-0006 / ledger 2026-07-06).

| query | before the fix | after |
|---|---|---|
| reproducer `select * from (select sum(d_dom) a, sum(d_year) b from date_dim) d` | `1149021\|1149021` | byte-identical to PG (`1149021\|146061700`) |
| Q21 | `1516\|1516` (PG `1516\|2833`) | byte-identical |
| Q28 | `count`/`count distinct` pair wrong in all six blocks | identical mod `char(n)` padding |
| Q46 | `profit` = `amt` | identical mod `char(n)` padding |
| Q66 | 34 replicated columns in 5 rows | identical mod `char(n)` padding |
| Q68 | `extended_tax` and `list_price` both = `extended_price` | identical mod `char(n)` padding |
| Q79 | `profit` = `amt` | identical mod `char(n)` padding |

Q21 and Q66 needed BOTH this fix and M0125-0009.

## SF0.5 row-count regression gate

Run in two parts because the harness has no query-range option and one sweep
exceeds the 60 min headless Bash ceiling.

- `sf05-sweep.log` — `scripts/tpcds-sf05-regression.sh sweep`, reached Q53
  before the wall clock cut it: 43 PASS / 2 SKIP / 6 TIMEOUT (Q5 Q10 Q14 Q30
  Q31 Q35) / 1 ERROR (Q8) / 1 MISMATCH (Q47). **Strictly better than the
  2026-07-27 baseline over the same range** — Q4 Q39 Q49 Q50 Q51 recovered to
  PASS, nothing newly failed. (The recoveries are the 9 intervening engine
  commits, not this change; the point here is the absence of new failures.)
- `sf05-tail.txt` — Q55..Q99 restricted to the queries that PASSED in the
  baseline, i.e. the ones that carry row-count signal at all (a query that
  timed out in the baseline yields no comparison either way). All PASS except
  **Q75**, which is the pre-existing `ERROR: division by zero` cell recorded by
  M0124-0001 at SF=1 and named in the M0125 banner. **Verified pre-existing**:
  reverting this loop's `planner.go` hunk, rebuilding and re-running Q75
  reproduces the identical error.

Net: zero regressions attributable to this change. This class of defect is
invisible to a row-count gate by construction (it swaps one column's values for
another's, leaving cardinality intact), which is why the SF=1 value comparison
above is the real acceptance.
