(idle — nothing in flight)

Loop #25 landed `ALTER TEXT SEARCH CONFIGURATION name {RENAME TO|SET SCHEMA|
DROP MAPPING [IF EXISTS] FOR tok [, ...]}` — the slice 446 follow-up named by
the prior loop's working_set/ledger row. All three forms previously fell
through to a discarded compat no-op; now parsed, applied to the catalog, and
persisted across restart via 3 new WAL record kinds.

Files: `internal/parser/ast.go` (`AlterTSConfigAddMappingStmt` generalized to
`AlterTSConfigStmt` with an `Action` field: "addmapping"/"dropmapping"/
"rename"/"setschema", mirroring `AlterCollationStmt`), `internal/parser/
ddl.go` (3 new dispatch branches + shared `parseTSMappingTokenTypeList`
helper), `internal/catalog/catalog.go` (`DropTSConfigMapping`/
`RenameTSConfig`/`SetTSConfigSchema` + `*DuringRecovery` counterparts),
`internal/executor/operators_ddl.go` (`execAlterTSConfigAddMapping` now a
private helper under new `execAlterTSConfig` action dispatcher; new
`execAlterTSConfigDropMapping` implements PG's 42704-vs-NOTICE IF EXISTS
distinction), `internal/wal/recovery.go` (3 new record kinds 109-111 +
Encode/Decode pairs), `internal/initdb/tsconfig_ddl_recovery.go` (3 new
replay cases + interface methods), `internal/planner/planner.go` +
`internal/server/dispatch.go` (renamed-type reference; also fixed a latent
bug — this statement type had NO `ddlTag` case at all, silently returning
"OK" instead of "ALTER TEXT SEARCH CONFIGURATION" for every form including
the pre-existing ADD MAPPING). New tests: `internal/executor/
tsconfig_rename_setschema_dropmapping_test.go` (3 funcs), `internal/wal/
tsdict_tsconfig_ddl_test.go` (+9 funcs for the 3 new record kinds),
`internal/initdb/tsdict_tsconfig_ddl_recovery_test.go`
(`TestTSConfigDDLRecoveryReplaysRenameSetSchemaDropMapping`). Design doc:
`docs/design/0110-0001-pg-dump-tap-port.md` new "Slice 446 follow-up: RENAME
TO / SET SCHEMA / DROP MAPPING" section; `docs/design/README.md` row
extended. Deferral ledger: new `-` row (ALTER MAPPING REPLACE / OWNER TO /
CONFIGURATION=source_config COPY form remain deferred).

Gates this loop: `go build ./...` clean; `go vet` on all touched packages
clean; `go test -count=1 ./internal/executor/... ./internal/catalog/...
./internal/parser/... ./internal/planner/... ./internal/wal/...
./internal/initdb/... ./internal/server/...` all PASS; `go test -race
-count=1 ./internal/wal/...` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `make ralph-state-guard` OK (auto-repaired the
running/completed status mismatch, as every loop does). About to commit
(pre-commit pgbench smoke runs via git hook) and push to
`origin/align-data-structure-with-pg`.

Next candidate (not started): per this loop's deferral ledger row, pick up
`ALTER MAPPING REPLACE` (both `FOR tok REPLACE old WITH new` and bare
`REPLACE old WITH new` forms — needs a 4th `AlterTSConfigStmt` action plus a
catalog method substituting one dictionary OID for another across matched
mapping entries), the `CONFIGURATION = source_config` copy-from-existing
form of `CREATE TEXT SEARCH CONFIGURATION`, or survey the deferral ledger
for a fresh DU-002 slice (e.g. the slice-436 row's unpicked `GRANT ... WITH
GRANT OPTION GRANTED BY` candidate). Re-check the ledger first in case a
concurrent loop already picked one up. Note: `postgres/` shows as an
untracked (`??`) top-level dir in `git status` despite `.gitignore`'s
`/postgres/` entry (`git check-ignore` returns exit 1, not ignored) — this
predates this loop, is the read-only upstream PG reference clone per its own
`.gitignore` comment, and was NOT modified; leave it alone, do not `git add`
it, and flag to the user if it becomes a recurring point of confusion.
