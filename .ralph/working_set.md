(idle — nothing in flight)

## Loop #4 (2026-09-01) result — nightly filing + M0134-0183 (typed_table.sql)
## sized & PARKED, 5 typed-table ALTER TABLE restrictions shipped

**Nightly triage:** `ci/logs/action-items.md` run `20260901-010436` (7 items).
5/7 subjects already had open M-NIGHTLY rows (re-failed, not new). Filed the
2 genuinely new subjects (`TestPort_PgStatActivity`,
`TestSyntax_Catalog_PgStatActivity` — both `pg_stat_activity`-family,
likely one root cause) as unchecked rows in `.ralph/fix_plan.md` under a
new "Nightly run 20260901-010436" subsection. Not selected/worked — M0134
stays next-priority per banner.

**Task:** M0134-0183 — `typed_table.sql`. **PARKED** (CSV `not-tried` →
`failed`, 150 → 135 diff lines). Design
`docs/design/0100-0149/m0134-0183-typed-table-sizing.md`.

**Shipped:** PG's five `ALTER TABLE` restrictions on a typed table
(`CREATE TABLE ... OF composite_type`) — ADD COLUMN/DROP COLUMN/RENAME
COLUMN/ALTER COLUMN TYPE/INHERIT, each 42809 `cannot ... typed table`,
`tablecmds.c` `ATPrepAddColumn:7200`/`ATPrepDropColumn:9260`/
`renameatt_check:3798`/`ATPrepAlterColumnType:14395`/
`ATPrepAddInherit:17237` — were entirely unchecked in goopg. Added one
`tbl.OfTypeOID != 0` guard per handler in
`internal/executor/operators_ddl.go`, each placed as the FIRST check to
mirror PG's prep-pass-before-exec-pass ordering (ALTER COLUMN TYPE is the
one with a nonzero `Pos`, matching PG's `parser_errposition`). New test
`internal/executor/alter_table_typed_table_restrictions_test.go`
(`TestAlterTableTypedTableRestrictions`).

**Finding:** the DROP COLUMN gap was silently masking a second bug — once
DROP COLUMN was (wrongly) allowed to succeed on `name`, the next statement
(`ALTER COLUMN name TYPE varchar`) reported "column does not exist" instead
of the typed-table message. Fixing DROP COLUMN made ALTER TYPE's message
correct too — one root cause, two visible symptoms (same recurring
"serially masked cause" shape as M0134-0014/-0025/-0026/-0182).

**Biggest remaining bucket (ledgered, NOT fixed — REFACTOR-tier):**
`DROP TYPE` has no dependency tracking against tables (`reloftype`) or
functions referencing the composite type. `RESTRICT` silently succeeds
where PG should refuse (listing dependents); `CASCADE` doesn't cascade-drop
them — by the time it runs, the type is already gone via the earlier silent
RESTRICT. This desyncs roughly half the file's remaining diff (stale
un-dropped tables block re-creation later; two `CREATE TABLE OF` statements
misreport generic "type does not exist" instead of PG's specific
row-type/composite-type errors). Resume: `execDropType`/`DropTypeStmt` in
`internal/executor/operators_ddl.go` — add a dependency scan + real CASCADE
walk, reusing DROP TABLE/DROP FUNCTION paths. Three smaller buckets also
ledgered (composite-SRF star-expansion, default `::text` cast decoration,
duplicate `WITH OPTIONS` detection, `$1.field` parser gap) — see
`.ralph/deferral_ledger.md` 2026-09-01 rows.

**Gates run:** `go build ./...` clean; `go test ./internal/executor/...`
full package PASS (new test + `TestAlterTableOfNotOfRegressMatrix`/
`TestAlterTableOfReassignAndNotOf` unaffected);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full units
suite PASS; `scripts/pg-regress-runner.sh -v typed_table` before/after
(150→135 diff lines, all 5 restriction lines now byte-identical);
`make check-testport-inventory` PASS; `make regen-testport` clean 6-file
regen; `make ralph-state-guard` PASS (one auto-repair: progress.json
stale "completed" marker reconciled to in_progress — pre-existing state
from a prior loop's clean exit, not caused by this loop); pre-commit
pgbench smoke PASS (499/640/11625 TPS, 0 failed).

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next
unclaimed M0134 case per ordering is **M0134-0184** (`unicode.sql`,
`not-tried`, never sized) — pick that up unless the banner changes.
Separately, the two newly-filed M-NIGHTLY `pg_stat_activity` failures
(AI-20260901-010436-005/-007) are NOT yet triaged (repro not run) — a
future M-NIGHTLY selection loop should run
`go test -v -run '^TestPort_PgStatActivity$' ./internal/testport/` first.

**In-flight:** none.
