Loop #33 COMPLETE: M0119-0004 DU-002 slice 393 — libc two-clause + builtin
provider collation pg_dump round-trip (TEST-ONLY). Closes the last two
unexercised dumpCollation provider limbs (pg_dump.c:14934+).

The libc `else` limb (lc_collate != lc_ctype → `lc_collate=…, lc_ctype=…`) and
the PG17+ `provider = builtin` branch were already implemented in
execCreateCollation + the pg_collation virtual builder (slices 389–391) but had
no end-to-end pg_dump round-trip. Added two fixtures only — no production change.

Files (commit pending):
- internal/testport/pgdump_connsetup_test.go: libc_diff + builtin_coll fixtures
  (~line 4955) + Contains assertions (~line 8065).
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 393 section.
- .ralph/fix_plan.md slice 393 entry; deferral ledger row appended.

Gates: TestPort_PgDumpConnectionSetup PASS (4.9s, byte-identical vs real
pg_dump 18.3); go build ./... clean; TestCreateCollationVirtualRows +
TestParseCreateCollation PASS. pgbench smoke runs via pre-commit hook on commit.

Collation pg_dump fidelity is now COMPLETE — every dumpCollation provider limb
goopg can emit is round-trip-asserted (libc collapse, libc two-clause, icu
locale/rules/deterministic, builtin).

Next loop: fresh M0119-0004 pg_dump slice — collation work is exhausted at the
dump-parity level. Candidates: persist user collations to a heap-backed
pg_collation (restart durability — the recurring 389–393 deferral, larger
subsystem); CREATE CONVERSION dump (new object, needs conproc regproc +
pg_encoding_to_char); ALTER/DROP COLLATION; or a different object family
entirely (CREATE CAST WITHOUT FUNCTION, aggregates prokind='a').
