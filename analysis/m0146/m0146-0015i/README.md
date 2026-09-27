# M0146-0015i evidence — non-LATERAL comma-item derived table / VALUES sibling scope

Fixture on both engines: `tk(u1,u2,h,t)` = 1000 rows, `u1=u2=i`, `h=i%10`.

- `goopg.txt` — probes against goopg at 127.0.0.1:5533 (HEAD + fix).
- `pg18.txt` — identical probes against PostgreSQL 18.3 at 127.0.0.1:5534.

Pre-fix, goopg silently computed lateral semantics: `from tk o,
(select ... where x.h = o.h) s` returned `1000` where PG rejects with
`42P01 invalid reference to FROM-clause entry for table "o"`. Post-fix
goopg rejects every sibling-reference shape PG rejects (wording differs:
goopg `column "h" does not exist` / `SELECT * with no FROM clause`, PG
`invalid reference` — same rejection class), keeps explicit LATERAL
working (`1000` on both), and keeps the true outer-level ref through a
comma item inside a scalar subquery resolving at the right level
(`1000` per outer row on both).

Regression pin:
`internal/executor/derived_table_outer_ref_test.go`
`TestDerivedTableCommaItemSiblingScope`.

Gates: units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); acceptance arm
24/24 MATCH; SF0.25 sweep 96/0/0 plans 99/99 same; tpcds-fireset PASS
(fires=none, both corpora).
