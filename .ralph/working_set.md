(idle — nothing in flight)

Last landed: DU-002 slice 180 (loop #148) — interval-literal column DEFAULT now
round-trips through pg_dump; sibling-path divergence with the executor twin closed.

`DEFAULT INTERVAL '1' day` on an `interval` column parses to a `*parser.IntervalLit`
(Value="1", Unit="day"). `interval` is an accepted column type (operators_ddl.go type
normalizer). validateDefaultExpr accepts it (falls through to nil; rejects only column
refs / subqueries / aggregate-or-SRF calls), so the node reaches pg_attrdef.adbin. Neither
catalog.formatExprForAttrdef nor executor.defaultExprToSQL had a `*IntervalLit` arm → both
fell through to fmt.Sprintf("%v", e) (Go pointer string), corrupting the dump. Fix: both
twins render `INTERVAL '<value>' <unit>` (value escaped for embedded quotes). goopg has no
interval output function so it emits its native INTERVAL form; PG's pg_get_expr would emit
`'1 day'::interval` — both valid/re-parseable/round-tripping. Display-only; can't share
code (catalog below executor in import graph). Verified end-to-end: e2e test PASSES, so the
DDL stores the IntervalLit raw (no coercion wrapping) and pg_dump renders it correctly.

Files: internal/catalog/catalog.go (+1 case), internal/executor/operators_ddl.go (+1 case,
twin), internal/catalog/catalog_test.go (interval lit + multi cases),
internal/testport/pgdump_connsetup_test.go (defcol gains `span interval DEFAULT INTERVAL '1'
day` + assertion `DEFAULT INTERVAL '1' day`), docs/design/0110-0001-pg-dump-tap-port.md (Slice
180 section). NOTE: also backfilled missing fix_plan PROGRESS entries for slices 178+179
(commits eb54ed51, 6d34c910) which had landed without their fix_plan rows.
Gates: gofmt OK; go vet clean; catalog format test PASS;
TestPort_PgDumpConnectionSetup PASS (3.17s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 181 candidates): the distinct fall-through-corruption audit for column DEFAULTs
is near-exhausted. Remaining unhandled Expr kinds (IsNullExpr/IsBoolExpr/IsDistinctFromExpr/
CollateExpr/InExpr/ArraySubscriptExpr) are contrived as column defaults (low value).
Faithfulness-only items: FuncCall "row"/COALESCE/NULLIF/GREATEST/LEAST render lowercase vs
PG uppercase (round-trips fine). HIGHER value: (1) close validateDefaultExpr array/row/CASE/
interval-element recursion gap (executor semantic change — needs its own gates); (2) deferred
MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK: partition routing); (3) pivot to a
different pg_dump catalog-surface gap (002 schema dump per fix_plan).
