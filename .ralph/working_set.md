Loop #48 landed and committed (`fix(parser): add ALTER CONVERSION RENAME/OWNER/SET
SCHEMA support (M0122-0007 4e follow-up)`), resuming exactly the next-step
pointer loop #47 left behind.

**Task:** M0122-0007 4e follow-up — the parser gap loop #47 root-caused:
`ALTER CONVERSION <name> OWNER TO <role>` was not a recognized `ALTER`
production at all (parser fell through to a table-only production), the
first non-catalog-scoping blocker the DU-002 round-trip probe
(`TestPort_PgDumpConnectionSetup`) had hit in this whole audit series.

**Fix (full ALTER CONVERSION support, not just OWNER TO):**
- `AlterConversionStmt` AST node (internal/parser/ast.go) + `parseAlter()`
  grammar branch (internal/parser/ddl.go, right after the `ALTER COLLATION`
  branch), mirroring `AlterCollationStmt` minus REFRESH VERSION. Modelled
  RENAME TO / OWNER TO / SET SCHEMA (confirmed against
  postgres/src/backend/parser/gram.y's 3 `ALTER CONVERSION_P` productions),
  not just the probe's OWNER TO form.
- Catalog: `RenameConversion`/`SetConversionOwner`/`SetConversionSchema` +
  `*DuringRecovery` counterparts (internal/catalog/catalog.go, right after
  `DropConversionDuringRecovery`, ~line 12719 onward), mirroring the
  `RenameCollation`/`SetCollationOwner`/`SetCollationSchema` trio.
- Executor: `execAlterConversion` (internal/executor/operators_ddl.go, right
  after `execAlterCollation`) + `*parser.AlterConversionStmt` case in the DDL
  dispatch switch (~line 172).
- WAL: 3 new record kinds `RecordKindAlterConversionRename`/`Owner`/
  `SetSchema` = 130/131/132 (internal/wal/recovery.go) + Encode/Decode pairs
  + `internal/initdb/conversion_ddl_recovery.go` replay wiring (mirrors
  `collation_ddl_recovery.go`) + physical-replay no-op classification case
  (default-case fallthrough in `recordKindToRmgrInfo`, no explicit entry
  needed).
- **Two extra wiring sites the collation-precedent grep missed, only
  surfaced by a `Plan()` test failure** ("unsupported statement type
  *parser.AlterConversionStmt"): `internal/planner/planner.go`'s
  DDL-passthrough type list (~line 155, added `*parser.AlterConversionStmt`
  next to `*parser.AlterCollationStmt`) and `internal/server/dispatch.go`'s
  command-tag switch (~line 2854, added a case returning "ALTER
  CONVERSION"). **Lesson for the next new-statement-type wiring job:** a DDL
  statement type needs registration at 4 sites, not 2 — parser+executor,
  catalog, planner's passthrough list, AND dispatch's tag-lookup switch.
- New tests: internal/parser/alter_conversion_test.go (rename/owner/
  setschema parse shapes incl. the probe's exact
  `ALTER CONVERSION public.aliasconv OWNER TO postgres` SQL),
  internal/executor/alter_conversion_test.go (mirrors
  alter_collation_test.go's rename/owner/setschema/IfExists/42704 coverage,
  using a local `conversionByName` helper since catalog has no
  `ConversionAttrsByName` analog to `CollationAttrsByName`).

**Files touched:** internal/parser/ast.go (AlterConversionStmt),
internal/parser/ddl.go (parseAlter branch),
internal/parser/alter_conversion_test.go (new),
internal/catalog/catalog.go (Rename/SetOwner/SetSchema trio + recovery
counterparts), internal/executor/operators_ddl.go (execAlterConversion +
dispatch case), internal/executor/alter_conversion_test.go (new),
internal/wal/recovery.go (3 record kinds + encode/decode),
internal/initdb/conversion_ddl_recovery.go (replay wiring),
internal/planner/planner.go (passthrough list),
internal/server/dispatch.go (command-tag switch), .ralph/fix_plan.md
(closed the ALTER CONVERSION bullet, added the new ts-dict bullet),
.ralph/deferral_ledger.md (2 new rows: resolved landing + fresh open
ts-dict row), docs/design/0122-0018-per-database-catalog-namespace.md (new
"ALTER CONVERSION grammar..." section), docs/design/README.md (surgical
Python string-replace on the single-line 0122-0018 row — do NOT use Edit on
that row, it's one 70KB+ line; verify line count stays 921 after the
replace).

**Confirmed via a LIVE re-run of `TestPort_PgDumpConnectionSetup`**: the
DU-002 probe's failure point moved past `aliasconv`'s `ALTER CONVERSION ...
OWNER TO` entirely to a NEW blocker — the same cross-database
catalog-key-collision shape this whole series keeps finding, now hitting
text search dictionaries: `text search dictionary "simple_dict" already
exists` restoring into the fresh `dumprestore_du002` database.

**Next resume point:** apply the identical M0122-0007 4e dbOid-scoping
pattern to `catalog.UserTSDict`/`InMemory.CreateTSDict` (added in the
2026-07-06 pg_dump-slice-437 ledger row — that row's own deferred column
already flagged "no WAL/recovery persistence" as a separate, still-open gap;
this loop's blocker is the *scoping* gap, independent of that one). Grep
`CreateTSDict`/`ListUserTSDicts`/`UserTSDict` in catalog.go (neighbors of
`CreateConversion`/`ListUserConversionsForDBOid`) for the exact current
shape (map vs slice — check before assuming). Add: `DBOid uint32` field,
trailing variadic `dbOid ...uint32` on Create/Drop via `resolveDBOid`, a
`ListUserTSDictsForDBOid`/`PGTSDictRowsForDBOid`-shaped lister mirroring
`ListUserConversionsForDBOid`/`PGConversionRowsForDBOid`, per-connection
wiring through `executor.Context`/`operators.go`'s virtual-row
materializer/`dispatch.go`'s `wireExtensionRows` (mirrors the `pg_conversion`
wiring 2 loops ago). Verify via `go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/` (soft `t.Logf`
probe) — expect the blocker to advance past `simple_dict`.

**Key symbols:** `catalog.InMemory.CreateTSDict` (catalog.go, exact line not
yet located this loop — grep it fresh, don't trust a stale line number),
`ListUserConversionsForDBOid`/`PGConversionRowsForDBOid` (catalog.go
~12847-12900, the precedent pair to mirror), `executor.Context` (context.go,
where `PgConversionRows` lives — a `PgTSDictRows` sibling field goes here),
`dispatch.go`'s `wireExtensionRows` (where `pgConversionRowLister` is wired
— a `pgTSDictRowLister` sibling goes here).

Housekeeping: same pre-existing untracked scratch content noted by prior
loops (`postgres` submodule placeholder, `Markdown_Table_Repair_Design_Doc.md`,
`tools/mdtablefix/`, `analysis/perf-optimize3/runs/...`, various `.txt`/
`.csv`/`.png` files, `ci/batch/lib/__pycache__/`, `kaitai-struct-dash*.txt`)
— all left untouched, not part of this loop's commit.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean; `go
test ./internal/parser/... ./internal/catalog/... ./internal/wal/...
./internal/initdb/... ./internal/executor/... ./internal/planner/...
./internal/server/...` PASS (initdb ~223s, included); `go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/` PASS (soft-log
confirms advance to the ts_dict blocker); `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 pgbench workloads);
`make ralph-state-guard` — found+auto-repaired the same stale
`progress.json` "completed"/loop-48-vs-running mismatch pattern prior loops
have hit, then reported consistent.
`scripts/tpch-spotcheck.sh` NOT run this loop (parser/catalog/executor
changes are DDL-only — ALTER CONVERSION grammar/registry, no
query-plan/row-count-affecting code path touched); a future
executor/planner-adjacent loop touching SELECT/DML should still run it per
Hard-won Rule #1.

In-flight: none. git push status: not yet checked this loop — check ahead-
count and push status before deciding whether to push (repo's standing
git-safety protocol requires explicit human direction to push).
