Task: DU-002 slice 95 — DOMAIN base-type TYPMOD survives pg_dump (COMPLETE, commit pending)

Files:
- internal/parser/ast.go — CreateDomainStmt gains BaseTypeArgs []int64.
- internal/parser/ddl.go — parseCreateDomain now CAPTURES the `(n[,m])` base-type
  modifier into stmt.BaseTypeArgs (was: skipped/discarded), mirroring parseColumnType.
- internal/executor/operators_ddl.go — execCreateDomain threads BaseTypeArgs onto
  catalog.Type{Name, Args} → RegisterDomain → d.Base.Args.
- internal/testport/pgdump_connsetup_test.go — fixtures vc20/ch4/numd + dom.v20/c4/nd;
  domainDefs asserts `character varying(20)`, `character(4) ... ::bpchar`, `numeric(10,2)`.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 95 narrative.

Key symbols: parseCreateDomain, CreateDomainStmt.BaseTypeArgs, execCreateDomain,
buildUserPGTypeRowForDomain (pgAttTypmod(baseOID, d.Base.Args)), formatTypeOID.

Findings: verified real pg_dump 18.3 — base render carries typmod
(format_type(typbasetype, typtypmod)); the string-DEFAULT cast stays typmod-LESS
(format_type(consttype, -1)) so varchar(20)→`::character varying`, char(4)→`::bpchar`,
already produced by domainConstCastTypeName (no cast change). numeric default bare.
Known residual divergence (unreachable here): formatTypeOID(1042,-1) returns
`character` where real PG returns `bpchar` for a bare bpchar base name — part of
broader format_type fidelity work.

Gates run: go build parser/executor/catalog OK; go vet OK; parser+catalog unit PASS;
executor Domain/PGType unit PASS; TestPort_PgDumpConnectionSetup PASS (2.2s);
pre-commit pgbench smoke runs on commit.

Next step: slice 96 — next domain/object increment. Candidates:
  (a) DOMAIN CHECK (VALUE ...) — needs pg_constraint contype='c' for domains +
      pg_get_constraintdef over a domain check (CheckInValues already parsed/stored).
  (b) composite type (CREATE TYPE AS (...)) — third object type; dumpCompositeType.
  (c) bare-bpchar base-name render (formatTypeOID(1042,-1) → bpchar) — format_type gap.
ADD fixture object, RUN TestPort_PgDumpConnectionSetup, let it report the real blocker.
