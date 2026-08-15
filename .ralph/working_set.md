# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 4 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 4 — DROP INDEX constraint-guard (2BP01)**.

**Status:** C2 slice 4 COMPLETE + committed (`36a2d3d0`) + pushed. C2 remains OPEN.

**Findings:** `execDropIndex` (`operators_ddl.go:6786`) now raises 2BP01
`cannot drop index %s because constraint %s on table %s requires it` + HINT
`You can drop constraint %s on table %s instead.` (unquoted names, PG
`getObjectDescription`, `dependency.c:780-795`) when the target index backs a
live UNIQUE/PK/EXCLUDE constraint (`idx.IsConstraint || idx.IsExclusion`;
constraint name == index name). A bare `CREATE UNIQUE INDEX` still drops.
Closes the onek :294-296 `DROP INDEX`→`RENAME CONSTRAINT`→`DROP INDEX <new>`
sequence (0 `onek_unique1_constraint` occurrences in the diff). Sibling test
`operators_ddl_rename_constraint_test.go` "unique re-keys" verify block flipped
from success-assert to 2BP01-assert (the old assert tested the now-fixed
non-PG behavior). New table-driven test
`operators_ddl_drop_index_constraint_guard_test.go`.

**Files:** `internal/executor/operators_ddl.go` (guard),
`internal/executor/operators_ddl_drop_index_constraint_guard_test.go` (new),
`internal/executor/operators_ddl_rename_constraint_test.go` (sibling assert);
`docs/design/0134-0002-alter-table-sql-divergence.md` (4th-slice note);
`.ralph/deferral_ledger.md` (row 1396 flipped `resolved` + new lock-ordering row);
`.ralph/fix_plan.md` (C2 progress bullet).

**Key symbols:** `execDropIndex` (operators_ddl.go:6786), guard at :6845-6852;
`catalog.Index.IsConstraint`/`IsExclusion` (catalog.go:1800-1801).

**Remaining C2 sub-gaps (ranked by error-site count ÷ risk):** TYPE…USING (11,
C10-entangled — MUST thread the USING expression, never parse-and-ignore; needs
a researcher pass first), comma multi-action (7, structural), RENAME `<col>` TO
bare (3, one-line), ANALYZE tab(col) (4, re-route — an ANALYZE/VACUUM gap),
NOT VALID (2), STORAGE (2), OF/NOT OF (3), DROP COLUMN IF EXISTS (1),
DROP CONSTRAINT IF EXISTS (1), SET WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** C2 slice 5 = **TYPE…USING** — research-first (delegate to
`researcher`): map how `parseAlterTableAction`'s ALTER COLUMN … TYPE arm parses
the type, where a `USING <expr>` trailer is dropped, and how `ATExecAlterColumnType`
(`operators_ddl.go`) would thread the USING expression through the data-loss-prone
type rewrite (class C10). Design note → brief → implement. NEVER parse-and-ignore
the USING expression (silent data loss). If research shows C10 entanglement is
too large, fall back to comma multi-action (7, structural) instead.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/
-p 4 -run TestDropIndexConstraintGuard` PASS (0.012s); `go test
./internal/executor/ -p 4` PASS (6.7s); `scripts/pg-regress-runner.sh alter_table`
— onek :294-296 closes (0 occurrences in diff), overall diff still FAILs on
pre-existing upstream gaps; pre-commit pgbench smoke PASS (hook); `make
ralph-state-guard` — self-repaired a stale progress.json marker, then OK.

**Delegation:** implementer `0134-0002-c2-dropindex-guard` DONE (one round).

**In-flight:** none.
