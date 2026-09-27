# M0146-0015g evidence — JOIN ON outer references

Private cluster pair: goopg `tmp/bpchar2-data` on :5533 (HEAD + the
`planJoinPredicate` parent stamp) vs a throwaway PG 18.3 initdb on :5534.
Fixture: `tk(u1,u2,h,t)` = `generate_series(0,99)`, `h=i%10`, `t=i%5`;
`tenk1(unique1,unique2,hundred,thousand)` = `generate_series(0,999)`,
`hundred=i%100`, `thousand=i%10`.

- `goopg.txt` / `pg18.txt` — byte-identical: LEFT/INNER/RIGHT joins with an
  ON-clause outer ref, EXISTS, `IS NOT DISTINCT FROM`, an unqualified outer
  ref (`u4` lives only on the outer alias), and the filed regress shape
  (both answer 200 on the synthetic tenk1; PG returns 2 on the real one).
- `siblings.txt` — the two distinct defects found while probing, filed in
  fix_plan under M0146: (A) a non-LATERAL derived table inside a correlated
  subquery gets `planSelectWithParent(…, nil, …)` and sees no outer scope;
  (B) `coalesce(d.u1,e.u1) IS NOT NULL` demotes a FULL JOIN to inner
  (goopg 10 vs PG 190).
