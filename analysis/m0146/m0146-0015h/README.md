# M0146-0015h evidence — non-LATERAL derived table / VALUES outer-scope resolution

Fixture on both engines: `tk(u1,u2,h,t)` = 1000 rows, `u1=u2=i`, `h=i%10`.

- `goopg.txt` — probes against goopg at 127.0.0.1:5533 (HEAD + fix).
- `pg18.txt` — identical probes against PostgreSQL 18.3 at 127.0.0.1:5534.

Pre-fix, every correlated case raised `column "h" does not exist` on
goopg; post-fix all values match PG. The sibling-reference probe errors
on both engines (PG `42P01 invalid reference`, goopg `42P01 missing
FROM-clause entry` — different wording, same rejection class).

Regression pin: `internal/executor/derived_table_outer_ref_test.go`.

Gates: units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); acceptance arm
24/24 MATCH; SF0.25 sweep 96/0/0 plans 99/99 same; tpcds-fireset PASS
(fires=none, both corpora).
