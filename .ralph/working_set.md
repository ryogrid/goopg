# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 3 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 is the
"ALTER-TABLE grammar cluster". This loop landed **C2 slice 3 — `RENAME
CONSTRAINT old TO new`**.

**Status:** C2 slice 3 COMPLETE + committed. C2 remains OPEN.

**Findings:** new parser kind `AlterTableRenameConstraint` + `OldConstraintName`
field (reuses `NewName`; `ConstraintName` stays "ADD CONSTRAINT name") + a RENAME
CONSTRAINT arm in the RENAME branch (mirrors the ALTER DOMAIN arm ddl.go:8215).
Executor `case AlterTableRenameConstraint` renames CHECK (OID-stable,
partition-child cascade mirroring drop :9873)/FK (slice mutation)/UNIQUE/PK/EXCLUDE
(via `InMemory.RenameIndex` — constraint name == index name — + `resyncIndexClassHeapRow`
for restart durability). Errors byte-match PG: 42704 `constraint %q for table %q
does not exist`, 42710 `for relation %q already exists` (CHECK/FK), 42P07
(index-backed). Closes con2/con3/cache-pkey rename sites + `\d` `"con3foo" PRIMARY
KEY, btree (a)`.

**Files:** `internal/parser/ast.go`, `internal/parser/ddl.go`,
`internal/parser/alter_test.go` (+TestParseAlterTableRenameConstraint),
`internal/executor/operators_ddl.go`,
`internal/executor/operators_ddl_rename_constraint_test.go` (new, table-driven);
`docs/design/0134-0002-alter-table-sql-divergence.md` (3rd-slice note);
`.ralph/deferral_ledger.md` (1 row); `.ralph/fix_plan.md` (C2 progress).

**Key symbols:** `AlterTableRenameConstraint` (ast.go:3167),
`AlterTableAction.OldConstraintName`, `parseAlter` RENAME branch (ddl.go:8551-8574),
`execAlterTable` `case AlterTableRenameConstraint` (operators_ddl.go:8317-8463),
`InMemory.RenameIndex` (catalog.go:12319), `resyncIndexClassHeapRow`.

**Remaining C2 sub-gaps (ranked):** DROP INDEX constraint-guard (2BP01 — the
blocker for the onek :294-296 block; next slice, small + self-contained), TYPE…USING
(11, C10-entangled — must THREAD USING, never parse-and-ignore), comma multi-action
(7, structural), RENAME `<col>` TO bare (3, one-line), ANALYZE tab(col) (4, re-route),
NOT VALID (2), STORAGE (2), OF/NOT OF (3), DROP COLUMN IF EXISTS (1), DROP
CONSTRAINT IF EXISTS (1), SET WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** implement C2 slice 4 = **DROP INDEX constraint-guard** — add a
dependency guard to `execDropIndex` (`operators_ddl.go:6786`): when the target
index backs a live UNIQUE/PK/EXCLUDE constraint, raise 2BP01 `cannot drop index %q
because constraint %q on table %q requires it` + HINT `You can drop %s instead.`
(PG `checkDependencyForDeletion`, `dependency.c:787-792`). Unblocks the onek
:294-296 `DROP INDEX`→`RENAME CONSTRAINT`→`DROP INDEX <new>` sequence. Full row in
the deferral ledger (tail, task-id M0134-0002).

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/ -p 4`
PASS; `go test ./internal/executor/ -p 4` PASS (6.6s); `scripts/pg-regress-runner.sh
alter_table` — RENAME CONSTRAINT sites closed (con2/con3/cache-pkey zero diff), onek
:294-296 still open on the DROP INDEX guard (out of scope); pre-commit pgbench smoke
(via hook on commit); `make ralph-state-guard` (below).

**Delegation:** researcher `0134-0002-c2-renameconstraint-research` DONE;
implementer `0134-0002-c2-renameconstraint-impl` DONE (PASS, one round).

**In-flight:** none.
