(idle — nothing in flight)

Last landed: DU-002 slice 120 (loop #84) — IDENTITY columns now dump byte-identically.
First MULTI-statement pg_dump object beyond a single sequence: backing sequence emits
`ALTER TABLE … ADD GENERATED ALWAYS|BY DEFAULT AS IDENTITY (SEQUENCE NAME …)`, not a
standalone CREATE SEQUENCE / OWNED BY. Five coupled changes:
  1. catalog.Column.IdentityAlways stores ALWAYS vs BY DEFAULT (plumbed from parser).
  2. attIdentityFor(col) emits pg_attribute.attidentity 'a'/'d' (was hardcoded empty).
  3. dependVirtualRows flips synthesized pg_depend deptype to 'i' for identity columns.
  4. DISCOVERY FIX: implicit identity sequence had no catalog IsSequence relation →
     invisible to pg_dump; extracted createSeqCatalogTable, call it for identity cols.
  5. LATENT-TYPE FIX: bigint identity seqDataType mapped to "integer" → seqtypid=int4
     but seqmax=INT64_MAX → spurious MAXVALUE; switch now mirrors seqMin/seqMax.
Verified byte-identical vs real pg_dump 18.3 (/tmp/pgref_du120). Fixtures ident_tbl
(integer ALWAYS) + ident_def (bigint BY DEFAULT) + negative guards.
Files: catalog.go, operators_ddl.go, pg18_user_catalog_rows.go,
pgdump_connsetup_test.go, docs/design/0110-0001-pg-dump-tap-port.md.

Next direction (slice 121): SERIAL column pg_dump round-trip — distinct deptype='a'
path: CREATE SEQUENCE + column DEFAULT nextval('seq') + ALTER SEQUENCE OWNED BY. The
slice-120 createSeqCatalogTable currently runs ONLY for identity columns; extending it
to SERIAL needs the column DEFAULT nextval emission too (check whether goopg surfaces
pg_attrdef for serial). Alternatively a table+sequence+view dependency-ordering case.
