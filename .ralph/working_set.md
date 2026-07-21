(idle — nothing in flight)

Last loop (#46): M-NIGHTLY triage of nightly run 20260720-005224 (6 items). All 6
are stale or rare flakes at HEAD `fb5de5c4` — no product change this loop; fix_plan
M-NIGHTLY + deferral_ledger updated, committed.

- 002-005 regress/errors,index_including,portals_p2,select — RE-VERIFIED stale at
  HEAD in isolation (errors/portals_p2/select PASS, index_including SKIPs, suite
  18.95s). New AI-ids appended to the existing checked task; divergence only in the
  nightly full-suite ordering/co-load, never the isolated repro. Not reopened.
- 001 TestE2E_FailoverPGtoGoopg/sync_on — zero-loss `count=5 want 6` was a co-load
  timing flake (pg.log MAP_HUGETLB OOM). Passes 4/4 at HEAD isolated. New checked
  task + ledger row (line 461): sync-rep flushLSN-vs-fsync feedback race OR
  promotion-replay-short — uninstrumented, chase if it recurs (force fsync stall +
  kill-after-sync-ACK). Did NOT weaken the test.
- 006 pgbench/nightly — 4/19.5M "current transaction is aborted" at TPC-B cmd4 =
  the known non-FIFO tuple-lock gap (goopg_dml_conflict_no_fifo_tuple_lock /
  ledger 0021-0012). Checked, tracked by existing row, no separate fix.

Next (M0123-S4 REMAINING, resume the active milestone): float SPECIALS
(`'Infinity'::float8`/`'NaN'::float4` → IEEE inf/nan Const, mirror
parseNumericSpecial recognition — resume-point in the 29c ledger row); typmod'd
string numeric cast `'5.5'::numeric(10,2)`; bare-integer→int2 implicit cast FuncExpr
(funcid 314); float4-common CASE mix; operator-driven view-qual coercion;
other length types (varchar(N)/timestamp(N)/bit(N)); broader date input forms.
