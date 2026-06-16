Task: DU-002 slice 112 — domain over `xid8` round-trips through pg_dump
(COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new
  `case catalog.OIDXid8: castType = "xid8"` arm.
- internal/testport/pgdump_connsetup_test.go — fixture domain x8_in; column
  x8i; domainDefs assertions ('100'/'200'::xid8 literals).
- docs/design/0110-0001-pg-dump-tap-port.md — slice 112 section.
- .ralph/fix_plan.md — loop #75 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDXid8 (5069),
userTypeAttrsForOID, format_type/formatTypeOID (5069→"xid8").

Findings: xid8 is the simplest render mode (bare string-with-cast, native eq,
decimal form round-trips verbatim, no normalization) — same as xid/cid (slice
107). All plumbing already present from xid8-column work (M0097-0018). Verified
byte-identical to real pg_dump 18.3 (/tmp/pgcheck_du112).

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain unit
PASS; TestPort_PgDumpConnectionSetup PASS (2.27s); pgbench pre-commit smoke on
commit (githook).

Next step: slice 113 candidates. EASY single-arm types are nearly exhausted.
Remaining excluded base types (tsvector/tsquery output requote, internal
"char" parser quote-state) each need real engine work. Range/composite-type
domains (int4range, CREATE TYPE AS) need FULL catalog support for those type
families — NO OIDInt4Range, no int4range in TypeNameToOID — a much larger task.
Probe int2vector/oidvector (both produce bare string-with-cast; need to verify
TypeNameToOID→OID + userTypeAttrsForOID + format_type all handle them) as the
last cheap single-arm candidates before moving to a structural type family.
