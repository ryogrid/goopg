(idle — nothing in flight)

M0127-P5.6-f is DONE, gated and committed. Step 0 (the eight UNIQUE indexes on
the TPC-H bench cluster, db `tpch`) is done and PERSISTENT — do not redo it, and
note that the estimate-audit baseline is now
`analysis/leftdeep-joins/2026-08-04-p56f.txt` (taken WITH those indexes). Any
audit diffed against a `p56eiii`-or-earlier report is comparing across schemas.

Three follow-ups were filed this loop; the banner selects, not this note:

- **P5.6-f-ii** (the substantive one). Q9's joinrel estimate is now EXACT
  (5 997 241 vs 5 997 241) and its PLAN did not move — Q9 runs 291.8 s with the
  5.3 %-selective `part` filter still above three full-cardinality hash joins.
  Cause, measured: the legacy join-order search never calls `estimateJoin`.
  `estimateJoinCost` (bushy.go:1257) is a second cardinality implementation
  whose production branch computes `ndv` as max NDistinct over EVERY column of
  the edge's two tables; the multi-edge + superkey arm beside it is gated on
  `costDrivenJoinOrder`, which M0126 left OFF, and its FK case divides by the
  CHILD's raw count where upstream divides by the PARENT's.
- **P5.6-f-iii** the DS05 gate's single TIMEOUT hopped Q72 → Q47, unattributed
  (Q47's estimates were checked and are unchanged; likely the sweep-tail GC
  confound). Needs a two-sweep A/B, not a code fix.
- ledger row: `columnStatsForChild` is the last of the three column resolvers
  still keeping its own arm list, and still lacks the `*IndexScan` arm the
  other two have.

Gates run: build + vet + gofmt-clean; planner/executor/catalog/server `go test`
PASS; UNITS PASS (`/tmp/units_p56f.log`); SPOT PASS Q12=2 Q13=35
(`/tmp/spot_p56f.log`); DS05 PASS=94 MISMATCH=0 CKMISMATCH=0
(`/tmp/ds05_p56f.log`); re-baselined estimate audit + post-fix audit
(`analysis/leftdeep-joins/2026-08-04-p56f-README.md`); pgbench smoke via the
commit hook.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file.

In-flight: none.
