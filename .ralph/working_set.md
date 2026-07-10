(idle — nothing in flight)

## Loop summary (2026-07-10, loop #12)

**Outcome: M0122-0024 — `CREATE TABLE name OF type_name (col WITH OPTIONS
column_constraint [...])` implemented and closed 2 matching
unimplemented_feat.json entries. Real feature build (parser + executor),
not just a stale-audit verify. Committed (pathspec-scoped, peer-safe) and
pushed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task from a prior loop; `go build ./...`
  clean at loop start — no new M-NIGHTLY task needed.
- Continued loop #11's survey list: picked entries #160/#165
  ("Per-column optional type specification in CREATE TABLE OF syntax
  (col WITH OPTIONS form)" / "WITH OPTIONS clause ... implemented as
  no-op") — both describe the same underlying gap.
- Before this fix ANY parenthesized list after `OF type_name` was
  rejected outright with "typed-table column option list is not
  supported" (`internal/parser/ddl.go`), even PG's own canonical doc
  example (`employees OF employee_type (salary WITH OPTIONS
  DEFAULT 1000)`).
- Parser: extracted `parseColumnDef`'s per-column constraint suffix
  (NOT NULL/DEFAULT/CHECK/UNIQUE/PRIMARY KEY/REFERENCES/COLLATE/...)
  into a new shared `parseColumnConstraintList(col *ColumnDef) error`
  (mechanical transform: `return ColumnDef{}, err` → `return err`,
  final `return col, nil` → `return nil`; verified byte-for-byte via a
  python script transform + `go build`/`go test ./internal/parser/...`).
  Implemented real `OF type_name (...)` list parsing: each
  `column_name WITH OPTIONS column_constraint [...]` entry parses via
  the shared helper into new `CreateTableStmt.OfTypeColumnOptions
  []ColumnDef` (`internal/parser/ast.go`). A bare `table_constraint`
  entry in the same list (PRIMARY KEY/UNIQUE/CHECK/FOREIGN KEY/
  CONSTRAINT at table level — also grammar-legal per PG's gram.y
  `TypedTableElement: columnOptions | TableConstraint`, confirmed via
  research subagent) is explicitly rejected with a clear parse error —
  narrower remaining scope, NOT silently supported.
- Executor: `execCreateTable` (`internal/executor/operators_ddl.go`)
  merges each override onto the matching composite-derived `ColumnDef`
  by name before the normal column-build path runs, so NOT NULL/
  DEFAULT/CHECK ride the same enforcement machinery as an explicit
  column — no new downstream plumbing needed. Unknown-column override
  rejected with `42703` ("column %q does not exist"), verified against
  real PG's `MergeAttributes` (`postgres/src/backend/commands/
  tablecmds.c:2589-2605` — the check is NOT in `transformOfType`,
  confirmed via research subagent).
- New tests: `TestCreateTableOfTypeColumnWithOptions`,
  `TestCreateTableOfTypeEmptyColumnList`,
  `TestCreateTableOfTypeTableConstraintRejected`,
  `TestCreateTableOfTypeUnknownColumnRequiresWithOptions`
  (`internal/parser/create_table_of_type_test.go`);
  `TestCreateTableOfTypeWithOptionsAppliesConstraints` (23502 NOT NULL
  + DEFAULT application), `TestCreateTableOfTypeWithOptionsUnknownColumn`
  (42703) (`internal/executor/create_table_of_type_options_test.go`).
- Flipped both `unimplemented_feat.json` entries `open`→`resolved` via
  surgical `Edit`s (86/181 resolved, 95 open).
- Added `.ralph/fix_plan.md` entry `M0122-0024`.
- Added a `.ralph/deferral_ledger.md` row: the `table_constraint` half
  of the same OF-type-name paren list (PRIMARY KEY/UNIQUE/CHECK/
  FOREIGN KEY at table level) is out of bounds for this loop — resume
  point is `internal/parser/ddl.go`'s `OF type_name` block (~line 3008),
  reusing the ordinary CREATE TABLE body's existing table-constraint
  field-population logic (`stmt.TableChecks`/`TableUniques`/
  `PrimaryKey`/etc., ~line 3404) rather than re-deriving it.
- Updated `docs/design/0110-0001-pg-dump-tap-port.md` (slice 374's
  "deferred, not supported" note — added an addendum section, did not
  rewrite) and its `docs/design/README.md` index row (appended a short
  addendum sentence to the existing long row, did not rewrite it).
- Gates run (foreground, all PASS): `go build ./...`, `go vet ./...`;
  `go test -count=1 ./internal/parser/...` (full package, includes new
  tests); `go test -count=1 ./internal/executor/...` (full package, 4s);
  JSON validity check; `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`
  (0 failed, all 3 workloads); `make ralph-state-guard` (self-repaired a
  stale running/completed mismatch, same pattern as loops #7-#12).
- Committed, pathspec-scoped to `.ralph/deferral_ledger.md`,
  `.ralph/fix_plan.md`, `.ralph/progress.json`,
  `docs/design/0110-0001-pg-dump-tap-port.md`, `docs/design/README.md`,
  `internal/executor/operators_ddl.go`,
  `internal/executor/create_table_of_type_options_test.go` (new),
  `internal/parser/ast.go`, `internal/parser/ddl.go`,
  `internal/parser/create_table_of_type_test.go` (new),
  `unimplemented_feat.json` — did NOT touch the concurrently-modified
  `ci/logs/*`, `analysis/tpch-explain-baseline.md`, `postgres`
  submodule, or untracked `weekly_loc.*` files sitting in the tree from
  another process. Pushed to `origin/align-data-structure-with-pg`
  (commit 0f40a98b).

**Next natural work:** continue surveying `unimplemented_feat.json`'s
remaining ~95 open entries. One carried over from loop #10/#11's survey
is still CONFIRMED genuinely open (not yet picked up): #67
`pg_get_expr()` (pass-through-only, no real node-tree reconstruction —
likely fine in practice since every populated pg_node_tree column
already stores pre-formatted text, but worth a closer architecture
review before declaring done). OR the table_constraint half of THIS
loop's own deferral (M0122-0024 ledger row — bounded, has a concrete
resume point). OR M0122-0007's remaining real scope (index/typed-table
TEMPLATE copying — confirmed large, do not attempt as a single-loop
bounded fix). OR pick a different milestone entirely for continued
variety.

Gates run: go build, go vet, go test (parser + executor full packages),
tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired) — all PASS.
In-flight: none
