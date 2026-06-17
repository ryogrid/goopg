(idle — nothing in flight)

Last landed: DU-002 slice 131 (loop #96) — pg_dump round-trip coverage for a
table-level UNIQUE constraint with an INCLUDE (covering) column
(`UNIQUE (a) INCLUDE (b)`). Regression guard, NO production change: empirically
confirmed vs real pg_dump 18.3 that PG folds the covering column into the
auto-generated name (`uniqi_a_b_key`, not `uniqi_a_key`) because
`allIndexParams = list_concat_copy(indexParams, indexIncludingParams)` feeds the
name chooser, and `pg_get_constraintdef` appends ` INCLUDE (b)`. goopg already
matched both via `autoIndexNameWithIncludes(keyCols+inclCols)` +
`buildConstraintDefString`'s INCLUDE branch + `catalog.Index.IncludeColumns`.
Added `uniqi` fixture + positive assert + 2 negative guards.
Files: internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: TestPort_PgDumpConnectionSetup PASS (2.18s); executor/catalog PASS.
Committed + pushed.

Next direction (slice 132): a table+VIEW dependency-ordering case (verify
topological emission ORDER — view dumped after the table it depends on), OR a
partial-index predicate round-trip (`CREATE INDEX ... WHERE`), OR a UNIQUE
NULLS NOT DISTINCT constraint.
