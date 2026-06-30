Loop #35 COMPLETE: M0119-0004 DU-002 slice 395 — a user-defined CAST
(`CREATE CAST (text AS bytea) WITHOUT FUNCTION`) now round-trips through real
pg_dump 18.3. A NEW object family (first CREATE CAST support of any kind).

Root cause: goopg had NO CREATE CAST dispatch case — parseCreate fell through to
its `expected TABLE, INDEX, …` error and rejected the statement outright, so the
catalog never saw it. pg_cast virtual view existed but always returned 0 rows.

Three-layer fix (committed):
- internal/parser/ddl.go: new `case "cast"` → parseCreateCastTail parses
  `(src AS tgt) {WITHOUT FUNCTION|WITH INOUT|WITH FUNCTION …} [AS ASSIGNMENT|IMPLICIT]`
  → CompatNoopStmt{ArgTypes:[src,tgt], CastMethod:b/i/f, CastContext:e/a/i}.
  + parseCastTypeName helper. New CastContext/CastMethod fields in ast.go.
- internal/executor/operators_ddl.go: execCompatNoop `case "cast"` →
  catalog.RegisterCast for binary/INOUT forms (castfunc=0; WITH FUNCTION skipped).
  DROP CAST path now calls DropCast before the "does not exist" error.
- internal/catalog/catalog.go: Cast type + casts map field + RegisterCast/DropCast/
  ListCasts; pg_cast VirtualRows surfaces each cast, resolving type names to
  castsource/casttarget OIDs via TypeNameToOID (text=25, bytea=17).
- internal/testport/pgdump_connsetup_test.go: 2 fixtures (text→bytea explicit;
  bytea→text AS ASSIGNMENT) + exit-0 block assertions, byte-identical vs pg_dump 18.3.
- docs/design/0110-0001-pg-dump-tap-port.md slice 395 section; fix_plan + ledger row.

Gates: TestPort_PgDumpConnectionSetup PASS (5.4s); parser+catalog units PASS;
go build ./... clean. pgbench smoke runs via pre-commit hook on commit. No
query-execution/codec/planner path touched (DDL registration only).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: WITH FUNCTION cast (needs a
pg_proc row so dumpCast→findFuncByOid resolves the function — see ledger resume
point); CREATE CONVERSION (needs pg_encoding_to_char builtin + conproc regproc
resolution — harder, conversion funcs are C-language); per-schema collation
disambiguation + heap-backed pg_collation persistence (recurring 389–395 deferral).
