# M0146-0015j evidence — coalesce(...) IS [NOT] NULL no longer demotes outer joins

Fixture on both engines: `tk(u1,u2,h,t)` = 1000 rows, `u1=u2=i`, `h=i%10`
(goopg clone on :5533 vs PG 18.3 on :5534).

- `goopg.txt` / `pg18.txt` — EXPLAIN (costs off) + row probes.

Pre-fix, goopg demoted the FULL JOIN to inner under
`coalesce(d.u1, e.u1) IS NOT NULL` and returned 10 rows; PG keeps
`Hash Full Join` and returns 190. Post-fix every probed shape agrees
on join TYPE and row count:

- `coalesce(d.u1, e.u1) IS NOT NULL` on FULL JOIN → Full kept (190).
- `coalesce(d.u1, e.u1) IS NULL` → Full kept (0) — forced-null side
  tightened to plain-Var operands like PG's find_forced_null_var.
- `coalesce(e.u1, 0) IS NOT NULL` on LEFT JOIN → Left kept.
- `abs(e.u1) IS NOT NULL` on LEFT JOIN → still demotes to inner
  (strict builtin — pg_proc proisstrict via LookupBuiltinProcByProname).
- `e.u1 IS NULL` on LEFT JOIN → still Anti Join.
- `e.u1 IS NOT NULL` on FULL JOIN → still demotes.

Residual plan diffs in the files are join-METHOD choice (Merge vs Hash)
and `COALESCE` render casing — costing/cosmetic, unrelated to the
demotion verdict.

Regression pins: `internal/optimizer/reduce_outer_joins_test.go`
(TestReduceOuterJoinsCoalesceIsNotNullKeepsFull,
TestReduceOuterJoinsCoalesceConstIsNotNullKeepsLeft,
TestReduceOuterJoinsStrictFuncIsNotNullStillDemotes,
TestReduceOuterJoinsCoalesceIsNullDoesNotForce).

Gates: units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); acceptance arm
24/24 MATCH; SF0.25 sweep 96/0/0 plans 99/99 same; tpcds-fireset PASS
(fires=none, both corpora).
