(idle — nothing in flight)

Last loop: **M0127-P5.9-h CLOSED** (via new slice **P5.9-k**). The TPC-H clause
2/3 timing gap was a MISSING cost term, not a mispriced one. Do NOT re-derive:

1. **The suspect P5.9-h named was innocent.** `costIndexScan` at
   selectivity 1.0 is roughly what PG charges. The defect was on the OTHER side
   of the `addPath` comparison.
2. `costSortRun` implemented only `cost_sort`'s COMPARISON term; its own
   comment justified skipping the external-merge arm with "TPC-H sorts are
   small dimension outputs". This phase invalidates that: a merge join sorts a
   JOIN INPUT, and Q12's is 5 997 241 lineitem rows (~4.7 GB).
3. **Why it is a defect and not an approximation**: the hash rival has been
   charged its spill since P5.7-a, so Q12's two candidates were billed
   **1 326 616** and **0** for spilling the same bytes through the same
   work_mem — the asymmetry 04 §1 forbids, as an entire missing term.
4. Fix: `cost_tuplesort`'s disk branch reproduced term for term, sized through
   `hashsize.EntryBytes` (the SAME model `spillPages` uses, so both charges are
   in one currency); `ncols==0` ⇒ no disk term (matches `hashJoinCost`'s zero
   `innerCols`); PG's `tuples<2 ⇒ 2` clamp replaces `return Cost{}`.
5. **Measured** (5 queries, one binary, both arms one session): Q7 26.71→16.29,
   Q9 54.95→15.86, Q10 22.93→5.65, Q12 20.79→9.82, Q18 74.71→29.79 s;
   **ON/OFF 2.61× → 1.007×**; all digests identical to run 3; Q12 back to Hash
   Join over two Seq Scans; §5 audit ON arm reduced to the OFF arm's single
   pre-existing Q18 violation; §4 `parity_violations=0`.

Files: `internal/planner/cost_funcs.go` (`costSortRun`, new
`tuplesortMergeOrder`), `joinpathsmerge.go` (`sortPathFor` threads
`relNCols`), `cost_sort_external_test.go` (new, 6 tests), 2 test call-site
updates. Docs: 09 §3.9, bundle README, docs/design/README.md,
IMPLEMENTATION-TODO P5.9-h [x] + P5.9-k [x], fix_plan same, 2 ledger rows,
`analysis/leftdeep-joins/2026-08-05-p59k-{on,off}.txt` + `-audit-on.{txt,plans.txt}`.

Gates run: `go test ./internal/planner/` green, UNITS precommit green,
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35), DS05 subset probe
under the flag `PASS=7 MISMATCH=0 ERROR=0 TIMEOUT=0`, pgbench smoke via the
commit hook, `make ralph-state-guard` (repaired a stale progress marker).
**NOT run: full DS05 `sweep` (~1 h) and `make plan-diff`** — ledgered since
P5.9-h, discharge at run 4.

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9 run 4** — the full S5 acceptance bar (09 §3), now with
NO known flag-owned defect outstanding. Its clause-4 DS05 clause is no longer
optional.

In-flight: none.
