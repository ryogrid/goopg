(idle — nothing in flight)

Last landed: DU-002 slice 187 (loop #155) — populated the `pg_collation` virtual
view (OID 3456) with the 7 built-in collations.

The view's `VirtualRows` was a `return nil` stub, so `SELECT * FROM pg_collation` /
psql `\dO` / collation-OID joins saw an empty relation (divergence from PG).
Filled it with `default`(100), `C`(950), `POSIX`(951), `ucs_basic`(962),
`unicode`(963), `pg_c_utf8`(811), `pg_unicode_fast`(6411) from PG18's
pg_collation.dat (collnamespace=11, collowner=10, collisdeterministic=t,
collicurules=NULL; libc rows carry collcollate/collctype, builtin/ICU rows carry
colllocale + collversion=1 for builtin). Mirrors initdb's
bootstrapPgCollationTuples seed; duplicated in catalog.go (cannot import initdb —
cycle). All OIDs < 16384 → pg_dump skips them, so no fixture output change; value
is \dO parity + prerequisite for per-column COLLATE round-trip.

Files: internal/catalog/catalog.go (pgCollation.VirtualRows),
internal/catalog/pg_collation_virtual_test.go (NEW — TestPgCollationVirtualRows),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 187), .ralph/fix_plan.md (loop-155).
Gates: gofmt OK; go build ./... clean; ./internal/catalog/ PASS; new test PASS;
pgbench pre-commit smoke on commit.

Next (slice 188 candidates): (1) per-column COLLATE round-trip — now unblocked on
OID-resolution side; capture column COLLATE in parser (internal/parser/ddl.go:2448
currently discards it) → store attcollation in pg_attribute heap (heap re-sync, see
[[pg_attribute_alter_needs_heap_resync]]) → pg_dump emits COLLATE when attcollation
≠ typcollation. (2) MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK: partition
routing). (3) attfdwoptions (foreign-table only, NULL today).
