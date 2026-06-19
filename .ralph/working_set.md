(idle — nothing in flight)

Last landed: DU-002 slice 245 (loop #11) — composite field whose type is a USER-DEFINED
ENUM now renders as the enum type in pg_dump (was `text`). Mirrors table-column slice 88.

Mechanism:
- parseCompositeFieldType (pg18_user_catalog_rows.go) now also returns the collapsed base
  type name (3rd return value).
- buildUserPGAttributeRowForCompositeField gained a `cat catalog.Catalog` first param; when
  the field folds to OIDText, cat.LookupEnum(base) re-resolves atttypid to the enum scalar OID
  and overrides physical attrs to enum shape {TypLen:4, ByVal:false, Align:'i', Storage:'p'}.
  nil catalog → text fallback preserved.
- Call site operators_ddl.go syncCompositeTypeToCatalogHeap passes ctx.Catalog.

Files: internal/executor/pg18_user_catalog_rows.go, operators_ddl.go (1 call site),
pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldEnum, 3 call-site fixups);
docs/design/0110-0001-pg-dump-tap-port.md (Slice 245).

Gates: gofmt clean; go build ./internal/... clean; executor package tests PASS (full);
live-verified port 5544 (pg_dump emits `feeling public.mood`, was `feeling text`); pgbench
pre-commit smoke on commit.

OBSERVED pre-existing (out of slice scope): pg_dump goopg→restore into a SECOND goopg db
duplicates composite pg_attribute/pg_type rows — shared catalog heap, no per-db isolation for
CREATE TYPE writes. Clean first dump is correct; duplication only after a 2nd restore.

Next (slice 246+): enum-ARRAY composite field (mirror slice 89: et.ArrayOID, attndims=1,
varlena-array attrs), then DOMAIN field (slice 90: DeclaredTypeName→cat.LookupDomain, base
physical layout), then nested-composite field. Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
