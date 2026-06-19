(idle — nothing in flight)

Last landed: DU-002 slice 246 (loop #12) — composite field whose type is an ARRAY
(built-in or enum) now round-trips through pg_dump. Mirrors table-column array path
(slices 62–83 built-ins, slice 89 enum).

Mechanism:
- parseCompositeFieldType (pg18_user_catalog_rows.go) now detects+strips the `[]` suffix
  (first '[' after the optional typmod), returns a 4th bool isArray; OID/typmod/base
  describe the ELEMENT type.
- buildUserPGAttributeRowForCompositeField: when isArray, remaps to the array OID
  (built-in via catalog.ArrayOIDForBase, enum via et.ArrayOID) and stamps attndims=1.
  Built-in array attrs from userTypeAttrsForOID(arrayOID); enum array gets varlena-array
  shape {TypLen:-1, ByVal:false, Align:'i', Storage:'x'}.

Files: internal/executor/pg18_user_catalog_rows.go, pg18_user_catalog_rows_test.go
(+TestUserPGAttributeCompositeFieldArray); docs/design/0110-0001-pg-dump-tap-port.md (Slice 246).

Gates: gofmt clean; go build ./internal/... clean; executor pkg tests PASS (full);
live-verified port 5546 (pg_dump emits `tags text[]`, `scores integer[]`,
`feelings public.mood[]`); pgbench pre-commit smoke on commit.

OBSERVED pre-existing PARSER gap (out of slice scope): parser.parseCreateType collects a
composite field's type tokens until the first ','/')' , so a typmod field
(`amount numeric(10,2)[]`, `code varchar(8)`) FAILS TO PARSE via SQL. The catalog-row
builder handles it (unit test builds the ColType directly), but it's unreachable until the
parser balances parens inside a composite field type. → candidate next slice.

Next (slice 247+): fix composite-field typmod parsing (parser parens-balance), then DOMAIN
composite field (slice 90 analog: DeclaredTypeName→cat.LookupDomain), then nested-composite
field. Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
