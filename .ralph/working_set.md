Task: DU-002 slice 94 — varchar DOMAIN with string DEFAULT survives pg_dump (COMPLETE, commit pending)

Files:
- internal/catalog/catalog.go — new domainConstCastTypeName helper maps base name
  to format_type(-1) spelling (varchar→character varying, char→bpchar, else
  passthrough); Domain.DefaultBin routes the StringConst cast suffix through it.
- internal/testport/pgdump_connsetup_test.go — fixture CREATE DOMAIN public.vcdef
  AS varchar DEFAULT 'na' + dom.vc column; domainDefs asserts
  `CREATE DOMAIN public.vcdef AS character varying DEFAULT 'na'::character varying;`.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 94 narrative.

Key symbols: domainConstCastTypeName (catalog), Domain.DefaultBin, get_const_expr
(PG oracle: cast name = format_type(consttype, -1)).

Findings: verified real pg_dump 18.3 — varchar→`::character varying`,
char→`::bpchar` (internal name, NOT `character`), text→`::text`. Bare varchar (no
length) chosen to isolate cast-name concern; CREATE DOMAIN parser DISCARDS the
`(n)` typmod (ddl.go ~5142), so varchar(20)/char(4) domains lose their length in
both base render and cast — separate larger gap (base-type typmod capture for
domains). TestPort_PgDumpConnectionSetup PASS (2.1s).

Gates run: go vet catalog OK; catalog unit PASS; TestPort_PgDumpConnectionSetup
PASS; pre-commit pgbench smoke runs on commit.

Next step: slice 95 — next domain/object increment. Candidates:
  (a) char/bpchar domain bare DEFAULT (`AS character ... DEFAULT 'x'::bpchar`) —
      exercises the bpchar branch of domainConstCastTypeName directly.
  (b) DOMAIN base-type typmod capture (varchar(20)→`character varying(20)` +
      `(20)`-less cast) — parser change in parseCreateDomain to keep type args.
  (c) DOMAIN CHECK (VALUE ...) — needs pg_constraint contype='c' + pg_get_constraintdef.
  (d) composite type (CREATE TYPE AS (...)) — third object type; dumpCompositeType.
ADD fixture object, RUN TestPort_PgDumpConnectionSetup, let it report the real blocker.
