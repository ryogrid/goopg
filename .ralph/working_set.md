(idle — nothing in flight)

Last landed: DU-002 slice 189 (loop #157) — extended the slice-188
attcollation/typcollation fix to ARRAY types, closing the slice-187 regression
that was STILL LATENT for array-of-collatable columns.

What happened: slice 188 fixed heap pg_type.typcollation for the collatable
scalars (name/text/bpchar/varchar) + _text, but left _name(1003), _bpchar(1014),
_varchar(1015) at 0. Meanwhile executor.userTypeAttrsForOID already reports the
element-inherited collation for those array OIDs (_name→950, _bpchar/_varchar→100,
since a PG array inherits its element's typcollation). So a varchar[]/bpchar[]/
name[] column had attcollation=100/100/950 vs heap typcollation=0 → pg_dump's
getTableAttrs (a.attcollation <> t.typcollation) fired → spurious
`COLLATE pg_catalog."default"` on a column the user never collated. Invisible
until a column of one of those array types was dumped (no prior fixture used one).

Fix: added 1003→950, 1014→100, 1015→100 to pgTypeCollationForOID in
internal/initdb/pg_type_bootstrap.go. Audit complete — no other built-in heap
type is collatable.

Files: internal/initdb/{pg_type_bootstrap.go,pg_type_bootstrap_test.go}
(NEW TestPgTypeArrayCollationMatchesElement),
internal/testport/pgdump_connsetup_test.go (NEW collarr 4-array-column fixture +
no-spurious-COLLATE assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 189).
Gates: gofmt OK; build clean; initdb + executor PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench smoke on commit.

Next (slice 190 candidates): (1) MINVALUE/MAXVALUE keyword-AST-node slice — but
note partition RANGE bounds ALREADY round-trip via the StringConst sentinel
(slice 169); a proper AST node would be a refactor of working code (avoid).
(2) attfdwoptions (foreign-table column OPTIONS, NULL today). (3) composite types
(CREATE TYPE AS) — pg_class.reltype is hardcoded 0 ("no composite type seeded
yet", pg18_user_catalog_rows.go:453); larger feature.
