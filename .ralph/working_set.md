# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 11 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 11 — OF / NOT OF** (commit `17513b6b`).

**Status:** C2 slice 11 COMPLETE + committed. C2 remains OPEN (2 tiny sub-gaps left).

**Findings:** The slice's parser arms + AST kinds + executor dispatch were already
scaffolded UNCOMMITTED in the tree (build was broken: `execAlterTableAddOf`/`DropOf`
undefined at operators_ddl.go:9345/9349). This loop landed the scaffold together
with the two missing executor bodies. `execAlterTableAddOf` resolves the composite
type, rejects an inheritance parent (42809 `typed tables cannot inherit`), then
order-strictly zips the composite's compacted fields vs the table's non-dropped
columns emitting the four 42804 messages in PG order; type match derives the
expected `catalog.Type` exactly as CREATE's `addCol` does (typmod + base-type both
fail; NOT NULL ignored). `execAlterTableDropOf` clears `OfTypeOID` (42809
`"…" is not a typed table` when never typed). Closes 3 syntax-error sites + all 6
validation errors byte-exact (ATExecAddOf/ATExecDropOf, tablecmds.c:18216-18390).

**Files:** internal/parser/{ast.go,ddl.go} (scaffold), internal/executor/
{operators_ddl.go, alter_table_of_type_test.go} (new methods + 2 tests),
docs/design/0134-0002-alter-table-sql-divergence.md (slice-11 entry),
.ralph/fix_plan.md, .ralph/deferral_ledger.md (2 rows: check_of_type 42809 parity;
reloftype restart-durability + table↔type dependency edge).

**Key symbols:** `execAlterTableAddOf`/`execAlterTableDropOf` (operators_ddl.go
:9387/:9462); `typeArgsEqual` (:9363); `AlterTableAddOf`/`AlterTableDropOf` +
`OfType` field (ast.go:3180-3189/3265); parser arms ddl.go:9461-9481;
reuse points `compositeFieldColumnType` (:1567), `addCol` (:1917),
`InMemory.LookupCompositeType` (catalog.go:21788), `tbl.OfTypeOID`.

**Remaining C2 sub-gaps (ranked):** ANALYZE tab(col) (4 — re-route: an
ANALYZE/VACUUM statement gap, NOT ALTER TABLE), SET WITHOUT OIDS (1), ENFORCED dup
(1, C9-masked). After C2: classes C3/C4/C9/C10/C11 (correctness, larger).

**Next step:** C2 slice 12 — **SET WITHOUT OIDS** (1 site) and/or **ENFORCED dup**
(1, C9-masked): the last two tiny C2 grammar sub-gaps. Both need a short
`researcher` round first (SET WITHOUT OIDS = legacy gram.y `SET WITHOUT OIDS` no-op
arm; ENFORCED dup = C9-masked, verify it's not actually fixed by a later C9 change
before writing it off). Delegate researcher → implementer, same pattern as slice 11.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/
./internal/executor/ ./internal/catalog/` PASS (2 new named tests);
`scripts/pg-regress-runner.sh alter_table` — OF/NOT OF block closed (syntax errors
→ 0, 6 validation errors byte-exact; residual LINE/^ is the pre-existing suite-wide
error-position class); pre-commit pgbench smoke PASS (12451 tps select-only).
M-NIGHTLY 20260816 already triaged last loop: race/internal/initdb stays open
(unchecked, infra time-budget), TestPort_FunctionSurvivesRestart + build-broke
marked checked (stale at HEAD).

**Delegation:** researcher `0134-0002-c2-slice11-of-notof` DONE; implementer
`0134-0002-c2-slice11-of-notof` DONE (1 round).

**In-flight:** none.
