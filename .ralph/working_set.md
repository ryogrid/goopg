(idle — nothing in flight)

Last loop (#29): M0119-0004 **`NOT VALID` CHECK constraint round-trip in pg_dump**
(DU-002 slice 308) — LANDED. Design `0119-0004-check-not-valid-roundtrip.md`.

The CHECK half of the shared ` NOT VALID` tail (slice 307 did FK). PG's
`pg_get_constraintdef_worker` (ruleutils.c:2604) appends ` NOT VALID` for any
contype with `convalidated='f'`. Unlike a valid CHECK (dumped INLINE in CREATE
TABLE), pg_dump sets `separate=!validated` for an unvalidated CHECK
(pg_dump.c:9757) → standalone post-data `ALTER TABLE public.nvc_tbl\n    ADD
CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;` (pg_dump.c:18564, NOT `ONLY`).

Five-site thread (mirrors FK):
- parser ddl.go AddCheck arm: capture `act.NotValid` (was discarded).
- catalog.go: new `NamedCheckConstraint.NotValid` + `AddCheckWithNotValid`.
- executor operators_ddl.go AlterTableAddCheck: call AddCheckWithNotValid.
- catalog.go pg_constraint builder (~4715): project convalidated='f'.
- executor expr.go pg_get_constraintdef CHECK branch (~7063): append ` NOT VALID`.

Gates: new DU-002 slice 308 in `TestPort_PgDumpConnectionSetup` (`nvc_chk` →
`ADD CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;` asserted in pg_dump stdout)
PASS; executor+parser+catalog suites PASS; `go build ./...` clean; pgbench smoke =
pre-commit hook.

NEXT loop — remaining open under M0119-0004 (probe TestPort_PgDumpConnectionSetup
for the next getter-battery gap):
- **FK MATCH FULL round-trip** — parser/deparse do not surface the FK match type;
  `buildForeignKeyDefString` always omits MATCH (only MATCH SIMPLE default today).
  PG stores confmatchtype='f' for MATCH FULL; pg_get_constraintdef emits ` MATCH
  FULL` after the REFERENCES col list, before ON UPDATE/DELETE.
- extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement; see memory).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).

NOTE on order in CHECK def string: `CHECK ((expr))` → optional ` NO INHERIT` →
` NOT VALID` (matches PG byte order). Inline NOT VALID in CREATE TABLE is NOT a
PG-valid form (no existing rows to grandfather) → intentionally unsupported.
