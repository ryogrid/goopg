Task: DU-002 slice 109 — domain over a user-defined ENUM base type round-trips
through pg_dump (COMPLETE, committing).

Files:
- internal/catalog/catalog.go — Domain struct: new BaseOID/BaseIsEnum fields
  (resolved base OID; enum flag).
- internal/executor/operators_ddl.go — execCreateDomain sets BaseOID/BaseIsEnum
  via new enumForDomainBaseType helper; domainInValuesCheckExpr detects enum
  BEFORE the switch (TypeNameToOID falls back to OIDText for enum names).
- internal/executor/pg18_user_catalog_rows.go — buildUserPGTypeRowForDomain uses
  d.BaseOID and inherits enum attrs (4B/int-align/plain/'E').
- internal/testport/pgdump_connsetup_test.go — fixture enum_in AS public.mood;
  column eni; domainDefs assertions.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 109 section.
- .ralph/fix_plan.md — loop #72 progress note.

Key symbols: domainInValuesCheckExpr, enumForDomainBaseType, Domain.BaseOID/
BaseIsEnum, buildUserPGTypeRowForDomain, catalog.LookupEnum/LookupEnumByOID.

Findings: real pg_dump 18.3 emits `CREATE DOMAIN public.enum_in AS public.mood`
+ `CONSTRAINT enum_in_check CHECK ((VALUE = ANY (ARRAY['sad'::public.mood,
'happy'::public.mood])))` — schema-qualified (empty search_path). TWO blockers:
typbasetype resolved to text (TypeNameToOID OIDText fallback) → dumped AS text;
and the CHECK cast mis-rendered ::text (same fallback). Both fixed. Enum labels
round-trip verbatim. The domain COLUMN ref (eni public.enum_in) already worked.

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain/enum
unit PASS; TestPort_PgDumpConnectionSetup PASS (2.42s); pgbench pre-commit smoke
on commit.

Next step: slice 110 candidates — composite type domain (`CREATE TYPE AS (...)`)
or range type as a domain base; OR the remaining excluded base types
(timestamptz session-tz render, tsvector/tsquery lexeme requote, "char"
quote-state). ADD fixture, RUN real pg_dump, let it report the real blocker.
