Task: DU-002 slice 107 — pg_lsn/tid/xid/cid domain `CHECK (VALUE IN ...)` survive
pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: 4 new bare
  string-with-cast cases OIDPgLsn/OIDTid/OIDXid/OIDCid (castType = bare type
  name, no coerce, no quoted cast). Doc table + excluded-types note + slice list
  extended.
- internal/testport/pgdump_connsetup_test.go — fixtures lsn_in/tid_in/xid_in/
  cid_in; columns lsni/tidi/xidi/cidi on public.dom; domainDefs assertions.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 107 section.
- .ralph/fix_plan.md — loop #70 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDPgLsn/OIDTid/OIDXid/OIDCid.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du107, cluster removed):
  pg_lsn → VALUE = ANY (ARRAY['16/B374D848'::pg_lsn, '0/0'::pg_lsn])
  tid    → VALUE = ANY (ARRAY['(0,1)'::tid, '(1,2)'::tid])
  xid    → VALUE = ANY (ARRAY['100'::xid, '200'::xid])
  cid    → VALUE = ANY (ARRAY['5'::cid, '10'::cid])
All native eq operators + canonical forms → bare string-with-cast, no quoting.
EXCLUDED (probed): tsvector/tsquery normalize lexemes ('a b'→'''a'' ''b''');
"char" (OID 18) — TypeNameToOID maps "char"→bpchar OID 1042, ::"char" needs
parser quote-state. timestamptz/interval/money still excluded.

Gates run: go build+vet OK; catalog/parser/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.17s); pgbench pre-commit smoke on commit.

Next step: slice 108 candidates — remaining excluded base types each need
render-normalization (timestamptz session-tz, interval/tsvector/tsquery
canonical form) or parser quote-state ("char"); OR move to a NEW object type
(composite CREATE TYPE AS (...) / range type / enum CHECK). ADD fixture, RUN
real pg_dump, let it report the real blocker.
