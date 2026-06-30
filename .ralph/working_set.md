Loop #39 COMPLETE: M0119-0004 DU-002 slice 399 — CREATE [DEFAULT] CONVERSION
pg_dump round-trip (new feature). goopg now re-emits a user conversion through
real pg_dump 18.3's getConversions/dumpConversion.

Landed:
- internal/parser/ddl.go: parseCreateConversionTail(pos,isDefault) parses
  `name FOR 'src' TO 'dest' FROM func`; new `case p.acceptKeyword(KwDefault)`
  dispatch arm handles CREATE DEFAULT CONVERSION. New CompatNoopStmt fields
  ConvForEncoding/ConvToEncoding/ConvFuncName/ConvDefault (ast.go).
- internal/catalog/encoding.go (NEW): EncodingIDToName / EncodingNameToID +
  pgConvEncNames (42 canonical pg_enc names, dup'd from initdb — cycle).
- internal/catalog/catalog.go: UserConversion struct + userConversions field +
  CreateConversion/DropConversion/ListUserConversions; pg_conversion VirtualRows
  populated (conproc retyped oid→regproc).
- internal/executor/expr.go: new pg_encoding_to_char(int4) builtin.
- internal/executor/operators_ddl.go: execCompatNoop case "conversion" registers;
  DROP path calls DropConversion.
- docs/design/0110-0001-pg-dump-tap-port.md: slice 399 section.
- .ralph/deferral_ledger.md: slice-399 row (deferrals a–c below).
- Tests: parser/conversion_test.go, catalog/conversion_test.go, slice-399
  asserts in testport/pgdump_connsetup_test.go.

Gates: go build PASS; TestParseCreateConversion PASS; TestEncodingIDNameRoundTrip
+ TestPgConversionVirtualRows PASS; full parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (5.3s, byte-identical vs pg_dump 18.3);
ralph-state-guard OK; pgbench smoke = pre-commit hook on commit.

Deferred (carry-forward): (a) EncodingNameToID resolves only 42 canonical names,
not pg_encname_tbl aliases (unicode→UTF8); (b) conversion func not validated
against pg_proc (stored as written, lenient); (c) restart persistence (in-memory
only — recurring 389–398 shared-catalog runtime-write gap).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: CREATE CONVERSION
encoding-alias resolution + pg_proc func validation (closes 399 (a)/(b));
cast/conversion/collation registry RESTART PERSISTENCE (the recurring deferral);
column-level attacl heap re-sync GRANT slice; CREATE TRANSFORM round-trip.
