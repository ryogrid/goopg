# 0110-0008 — amcheck SQL surface wiring plan (M0110-0003)

**Status:** S1+S2+S3+S4 landed; S5 named-arg parsing landed (worktree `m0110-0003-amcheck-sql-surface`); S5 LATERAL/clog/TAP-port remain (merge blocked on a clean working tree)
**Scope:** the `CREATE EXTENSION amcheck` + SRF SQL surface that wires the
already-committed `internal/amcheck` engine to the wire protocol and promotes the
deferred `AC-002`…`AC-005` pg_amcheck TAP tests.
**Depends on:** [0110-0005](0110-0005-verify-heapam-engine.md) (heap engine),
[0110-0007](0110-0007-amcheck-heapallindexed.md) (B-tree engine),
[0110-0006](0110-0006-amcheck-bloom-filter.md) (Bloom primitive),
[0110-0003](0110-0003-pg-amcheck-tap-port.md) (CLI tier `AC-001`, already `port`).

## Why this doc exists

The `internal/amcheck` engine is **logic-complete and committed** (`bloomfilter.go`,
`heapallindexed.go`, `verify_heapam.go`, `verify_nbtree.go` + tests; last engine
commit `62e67c03`). The only remaining M0110-0003 work is the SQL surface. That
surface unavoidably edits files that currently carry a **separate manual session's
uncommitted gen-column WIP** (M0100-0010, `WITH OPTIONS GENERATED ALWAYS AS`),
static since 2026-06-13 14:28. Ralph must not commit foreign WIP, so the surface
cannot land until the tree is clean.

Loops #62 and #63 each re-derived this scope from scratch (re-reading
`002_nonesuch.pl`, re-finding the catalog gaps). This doc captures the derivation
**once**, in execute-ready form, so the unblocking loop executes a plan instead of
re-discovering it. It also records a scope refinement loop #63 missed (below).

## The exact client surface pg_amcheck requires

All three queries are issued by the upstream client binary
`postgres/src/bin/pg_amcheck/pg_amcheck.c`; goopg must answer them verbatim.

### 1. Per-database "is amcheck installed?" probe (`amcheck_sql`, line 173)

```sql
SELECT n.nspname, x.extversion FROM pg_catalog.pg_extension x
  JOIN pg_catalog.pg_namespace n ON x.extnamespace = n.oid
WHERE x.extname = 'amcheck'
```

- 1 row ⇒ amcheck installed; client uses `nspname` to schema-qualify the SRFs and
  `extversion` for the `--checkunique` version gate (≥ 1.4).
- 0 rows ⇒ client logs `warning: skipping database "<db>": amcheck is not installed`.

This single query is **the crux of `002_nonesuch`**: in `postgres` (after
`CREATE EXTENSION amcheck`) it returns a row; in `template1` (no extension) it
returns none, driving the "skipping … amcheck is not installed" warnings the test
asserts. `pg_namespace` already exists in goopg's catalog
(`internal/catalog/catalog.go:1895`); **`pg_extension` does not** — it is the one
new catalog relation required.

### 2. Heap check SRF (`prepare_heap_command`, line 843)

```sql
SELECT v.blkno, v.offnum, v.attnum, v.msg
FROM pg_catalog.pg_class c, <schema>.verify_heapam(
  relation := c.oid, on_error_stop := false, check_toast := false, skip := 'none'
  [, startblock := N][, endblock := N]
) v
WHERE c.oid = <reloid> AND c.relpersistence != 't'
```

SRF signature: `verify_heapam(regclass, on_error_stop bool DEFAULT false,
check_toast bool DEFAULT false, skip text DEFAULT 'none', startblock int8 DEFAULT NULL,
endblock int8 DEFAULT NULL) RETURNS SETOF (blkno int8, offnum int8, attnum int4, msg text)`.
Each engine `amcheck.Report{Blkno, Offnum, Attnum, Msg}` maps to one output row.
`check_toast` is goopg-divergent (see 0110-0005) — accept the arg, ignore it.

### 3. Index check SRFs (`prepare_btree_command`, line 887)

```sql
-- default (bt_index_check):
SELECT <schema>.bt_index_check(index := c.oid, heapallindexed := false)
FROM pg_catalog.pg_class c, pg_catalog.pg_index i
WHERE c.oid = <oid> AND c.oid = i.indexrelid
  AND c.relpersistence != 't' AND i.indisready AND i.indisvalid AND i.indislive
-- --parent-check uses bt_index_parent_check(index, heapallindexed, rootdescend[, checkunique])
```

Signatures: `bt_index_check(regclass, heapallindexed bool DEFAULT false) RETURNS void`
and `bt_index_parent_check(regclass, heapallindexed bool DEFAULT false,
rootdescend bool DEFAULT false, checkunique bool DEFAULT false) RETURNS void`.
Both return **void** and signal corruption by **raising an error** (ERRCODE
`ERRCODE_INDEX_CORRUPTED` / generic) — the engine's `[]BtreeReport` is joined into
the error message. `pg_index` must answer the `indisready/indisvalid/indislive`
predicate.

## Scope refinement (loop #63 over-estimated `002_nonesuch`)

`002_nonesuch.pl` (420 lines) is overwhelmingly **client-side argument / pattern
resolution** — "improper qualified name (too many dotted names)", "no connectable
databases matching …", "no relations to check". Those are decided inside
`pg_amcheck.c` before any server round-trip. The **only** server-side requirements
are: (a) `CREATE EXTENSION amcheck` succeeds in `postgres`, and (b) the §1 probe
query answers correctly per database. `002_nonesuch` does **not** invoke
`verify_heapam` against real corruption (that is `004_verify_heapam` /
`005_opclass_damage`). Therefore **Slice S1 + S2(probe only) alone promote
`AC-002`** — the SRFs need only *exist* in `pg_proc` for relation-resolution to
type-check; their execution is exercised by `003`/`004`, not `002`.

## Implementation slices (each independently committable once tree is clean)

| slice | lands | promotes | files |
|-------|-------|----------|-------|
| **S1** | `CREATE EXTENSION amcheck` DDL: parse → dispatch → executor → `pg_extension` row + `pg_namespace` resolve | — | parser `ast.go`/`ddl.go`, `server/dispatch.go`, `executor/operators_ddl.go`, `catalog/catalog.go` |
| **S2** | `pg_extension` catalog relation + the §1 probe answerable; seed `pg_proc` rows for the three SRFs (exist, not yet callable) | **AC-002** (`002_nonesuch`) | `catalog/catalog.go`, `planner/planner.go` (FROM whitelist) |
| **S3 ✅** | `verify_heapam(...)` SRF executor — a **thin adapter** over `amcheck.VerifyHeapRelation` (the block loop lives in the engine, `verify_heapam_relation.go`): fills a `PageSource` from the buffer pool, passes `nblocks` + `RelDesc.Natts`/`NextXid`/`RelFrozenXid` (clog-backed `XidStatusFunc` deferred — no clog handle in the executor yet), streams `HeapRelReport`s as `(blkno,offnum,attnum,msg)` rows | (toward AC-004 heap path) | `planner/{plan,planner,foldconst,unnest}.go`, `parser/select.go`, `analyzer/analyzer.go`, new `executor/operators_verify_heapam.go` + `executor.go` |
| **S4 ✅** | `bt_index_check` / `bt_index_parent_check` scalar `void` functions over `amcheck.VerifyBtree*` — they are SELECT-list scalars (not FROM SRFs like verify_heapam), so they live in `evalFuncCall` dispatch; structural tiers (`VerifyBtreePage`/`VerifyBtreeItemOrder` per block, `VerifyBtreeLevelSiblingLinks` per level via leftmost-descent, `VerifyBtreeParentDownlinks` per internal page on parent-check) raise `XX002` on findings; `heapallindexed`/`rootdescend`/`checkunique` args accepted, deeper tiers deferred to S5 | (toward AC-003/004 index path) | `planner/planner.go` (`exprType` void), `executor/expr.go` (dispatch), new `executor/operators_bt_index_check.go` |
| **S5 (partial)** | named-arg `:=`/`=>` parsing in both expr + FROM-SRF paths ✅; then LATERAL resolution of `c.oid`, clog `XidStatusFunc`, port `002`→`005` TAP tests, flip CSV `AC-002…AC-005`→`port`, regen md | **AC-002…AC-005** | `parser/select.go` ✅; `internal/testport/pgamcheck_port_test.go`, `docs/test-port/*` |

New-file slices (the executor ops, the testport additions) touch **zero**
contaminated files; only S1/S2's parser/dispatch/catalog edits and S3/S4's
planner edits land in files that today carry the foreign WIP.

## Contaminated-file map & separability

The foreign gen-column hunks sit in partition / lockrows / gen-column functions
(`parseCreateTableTail`, `PartitionColGenerated`, …), **not** in extension/SRF
code paths. Once the manual session commits or stashes its WIP the wiring is a
clean additive overlay. If a slice must land while the tree is still dirty, stage
only amcheck hunks with `git add -p` (never `git add -A`); the regions do not
overlap. Preferred path remains: wait for a clean tree.

## Engine API the SRFs call (already committed, stable)

- `amcheck.VerifyHeapPage(p, blkno) []Report` / `VerifyHeapPageWithRel(p, blkno, rel)` /
  `VerifyHeapPageWithXminStatus(p, blkno, rel, xidStatus)` — `Report{Offset, Msg}` (per page).
- `amcheck.VerifyHeapRelation(src PageSource, nblocks, HeapRelOptions{StartBlock, EndBlock, Rel, XidStatus})
  []HeapRelReport` — the relation walk (block-range resolve + iterate + per-page check),
  `HeapRelReport{Blkno, Offset, Msg}` → one SRF row each (attnum -1, set by the SRF);
  **this is what the S3 SRF calls** — reuses the same `PageSource` seam as the B-tree side.
- `amcheck.VerifyBtreePage`, `VerifyBtreeItemOrder`, `VerifyBtreeLevelSiblingLinks`,
  `VerifyBtreeParentDownlinks` (via `PageSource func(BlockNumber)(Page,error)` seam — the
  SRF fills it from smgr), `VerifyBtreeHeapAllIndexed` — all return `[]BtreeReport`.

## Oracle

Client: `postgres/src/bin/pg_amcheck/pg_amcheck.c` (queries above).
Functions: `postgres/contrib/amcheck/verify_heapam.c`,
`postgres/contrib/amcheck/verify_nbtree.c`. Extension catalog shape:
`postgres/src/include/catalog/pg_extension.h`. Validate against PG 18.3 via
`postgres/local_install/bin/pg_amcheck` + `psql \dx`.

## Implementation status

- **Slices S1 + S2 — LANDED** (worktree `m0110-0003-amcheck-sql-surface`, off clean
  HEAD `b8dd6403`; isolated from the main-tree foreign gen-column WIP per
  `worktree_isolation_escapes_foreign_wip_block`). Delivered:
  - **S1 (DDL):** `parser.CreateExtensionStmt` + `parseCreateExtensionTail`
    (`internal/parser/ast.go`, `ddl.go`) parsing
    `CREATE EXTENSION [IF NOT EXISTS] name [WITH] [SCHEMA s] [VERSION v] [CASCADE]`
    in any option order; planner DDL passthrough (`internal/planner/planner.go`);
    `CREATE EXTENSION` command tag (`internal/server/dispatch.go`);
    `ddlOp.execCreateExtension` (`internal/executor/operators_ddl.go`) — unknown
    extension → `58P01` (ERRCODE_UNDEFINED_FILE, "control file" message), duplicate
    without IF NOT EXISTS → `42710`. Built-in allow-list `knownExtensions`
    (`amcheck` → `1.4`).
  - **S2 (`pg_extension` + install probe):** `pg_extension` virtual catalog
    (OID 3079, upstream column order/names; `extconfig`/`extcondition` NULL) +
    `InMemory.CreateExtension` runtime registry, `extnamespace` resolved to OID at
    read time (`internal/catalog/catalog.go`). Backs pg_amcheck's
    `pg_extension ⋈ pg_namespace` install probe (`pg_amcheck.c:173`).
  - Tests: `parser.TestParseCreateExtension` (5 shapes),
    `testport.TestPort_AmcheckCreateExtension` (pre-install 0 rows → install →
    1 row `(public, 1.4)` → duplicate errors → IF NOT EXISTS idempotent →
    unknown extension errors). Both PASS.
- **S4 `bt_index_check` / `bt_index_parent_check`** (landed, executor +
    planner): scalar `void` functions in `executor/operators_bt_index_check.go`,
    dispatched from `evalFuncCall` (they are SELECT-list scalars, *not* FROM-clause
    SRFs — `SELECT bt_index_check(c.oid, false) FROM pg_class c, pg_index i …`),
    with `exprType` returning `void` (OID 2278). Each resolves the index regclass
    (OID or name), fills a `PageSource` from the buffer pool over the index's
    `IndexRelFileNode`, and drives the engine's structural tiers; any
    `[]amcheck.BtreeReport` finding raises `XX002` (ERRCODE_INDEX_CORRUPTED) with
    the first message + a multi-finding DETAIL. Clean index → `void`.
    Tests: `executor.TestBtIndexCheck_{CleanIndexNoError (no-false-positive gate,
    both funcs, all positional shapes), DetectsCorruptMetapage (clobbered magic →
    raise), NonexistentIndex}`. All PASS.
- **S5 (in progress) — named-argument parser support LANDED** (parser only, zero
  contaminated files): pg_amcheck emits its amcheck calls with the legacy
  `name := value` named-argument spelling — `bt_index_check(index := c.oid,
  heapallindexed := false)` and, as a FROM-clause SRF,
  `verify_heapam(relation := c.oid, on_error_stop := false, …)`. goopg already
  stripped the modern `name => value` spelling positionally (M0097-0003) in the
  **expression** function-call path (`parseFuncCallTail`), but (a) only for `=>`,
  not `:=`, and (b) **not at all** in the **FROM-clause SRF** arg loop
  (`parseRangeVar`) — the sibling path. Both paths now accept `:=`/`=>` and strip
  the name, mapping positionally; the verify_heapam executor's positional order
  (`relation, on_error_stop, check_toast, skip, startblock, endblock`) and
  bt_index_check's (`index` first) already match pg_amcheck's named order, so the
  strip binds correctly. The argument *name* may be an unreserved keyword (e.g.
  `index`, `skip`), so the token check accepts `TokenKeyword` too — unambiguous
  because the `:=`/`=>` lookahead gates it (`isNamedArgNameToken`). Files:
  `internal/parser/select.go` only. Tests:
  `parser.TestParseNamedArgColonEqual{,FromSRF,EquivalentToFatArrow}` (4 cases:
  both scalar funcs, the full 6-arg FROM-clause SRF with the correlated `c.oid`
  first arg, and `:=`≡`=>` equivalence). All PASS; full parser/analyzer/planner
  suites green. **Still remaining for the client query shape:** LATERAL
  *resolution* — the FROM-clause `verify_heapam(relation := c.oid, …)` correlates
  against `pg_class c` (an implicit-LATERAL comma-join). Parsing now succeeds and
  `c.oid` lands as a `ColumnRef`, but the planner/executor still need to resolve
  that outer reference per-row. That, plus the clog `XidStatusFunc` wiring and the
  TAP port itself, is the rest of S5.
- **S5 (in progress) — AC-002 bootstrap-query SQL-engine gaps #1+#2 LANDED**
  (parser + analyzer, zero contaminated files): the `002_nonesuch` port runs the
  real `pg_amcheck` end-to-end, and its bootstrap queries (database/relation
  resolution in `pg_amcheck.c compile_database_list` / `compile_relation_list_one_db`)
  hit two general goopg SQL-engine gaps — neither amcheck-specific:
  1. **`index` rejected as a CTE name.** The relation-gathering query declares a
     CTE literally named `index`. `parseCTE`→`parseIdent` already accepts an
     unreserved/col_name keyword as an identifier, but the post-`,` look-ahead
     guard in `parseWithClause` only allowed `TokenIdent`/`TokenQuotedIdent`, so
     `WITH a AS (…), index AS (…)` was wrongly rejected with "expected CTE name
     after ','". Fix: the guard now also accepts a `TokenKeyword` that
     `IsColNameKeyword` admits — mirroring `parseIdent` exactly. A reserved
     keyword (e.g. `select`) is still rejected. File: `internal/parser/with.go`.
  2. **VALUES-list-as-CTE reports 0 columns.** The database-resolution query uses
     `include_raw (pattern_id, rgx) AS (VALUES (0,'^(x)$'), …)`. The non-recursive
     `analyzer.registerAnalyzedCTE` built the CTE's column set only from
     `cte.Query.Targets`, which is empty for a VALUES body → the alias-arity check
     errored "has 2 column aliases but inner query produces 0 columns". Fix:
     when `Targets` is empty and `ValuesRows` is non-empty, derive the column
     count from the first VALUES row (`column1`, `column2`, … / type `unknown`) —
     mirroring the VALUES anchor already handled in `analyzeRecursiveCTE` (a
     sibling-path fix; the two paths must stay in sync). File:
     `internal/analyzer/analyzer.go`.

  Tests: `parser.TestParseCTENamedIndex` (first-position + comma-position `index`,
  reserved-keyword still rejected); `analyzer.TestAnalyzeWithValuesCTE{ColumnAliases,
  ArityMismatch,DefaultColumnNames}`. Full parser + analyzer suites green;
  `go build ./...` + gofmt + vet clean.

- **AC-002 gap #3 (connection-level) LANDED** + a newly-surfaced gap #2b
  (CTE column-alias under-aliasing). Two parts of gap #3 plus the bootstrap-query
  follow-on:
  1. **Bootstrap databases registered in `pg_database`.** `catalog.NewInMemory`
     now seeds `{postgres, template1, template0}` (was `{postgres}` only). The
     `pg_database` virtual rows carry each template's canonical PG attributes:
     `template1` (oid 1, `datallowconn=t`, `datistemplate=t`), `template0` (oid 4,
     `datallowconn=f`, `datistemplate=t`), mirroring initdb's `buildRow` seed.
     pg_amcheck's database-list query filters `WHERE datallowconn AND
     datconnlimit != -2`, so `template0` is correctly omitted from `--all` while
     `template1` is included. File: `internal/catalog/catalog.go`.
  2. **Non-existent-database connection rejection (3D000).** After `checkAuth`
     succeeds in `internal/server/server.go`'s connection path, a non-replication
     connection whose `database` param is absent from the registry is rejected
     with a FATAL `ErrorResponse` SQLSTATE `3D000` (`database "%s" does not
     exist`) and the connection closes — mirroring PG's post-authentication
     `InitPostgres` check. Guarded for `Catalog == nil` / non-`databaseRegistry`
     embedded-test paths (mirror `tryHandleDatabaseDDL`); replication connections
     bind no database and are skipped. File: `internal/server/server.go`.
  3. **gap #2b — CTE alias list shorter than the inner query (under-aliasing).**
     pg_amcheck's empty-exclude-pattern CTE is `exclude_raw (pattern_id, rgx) AS
     (SELECT NULL, NULL, NULL WHERE false)` — two aliases over three columns. PG
     (`parse_cte.c analyzeCTE`, lines 583-585: "the alias list must be empty or
     exactly as long … but we allow it to be shorter") accepts this; the trailing
     unaliased column keeps its query name. goopg rejected it with 42P10 at SIX
     sites — two in `analyzer.go` (`analyzeRecursiveCTE` VALUES anchor +
     `registerAnalyzedCTE`) and **five** alias-validation blocks in
     `planner/with.go` (the recursive-branch, the DML-CTE skip, the non-recursive
     "Bypass Plan() entry", `planRecursiveCTE`'s non-recursive fallback, and the
     recursive-UNION anchor). Fix: change every `len(cte.Columns) != len(cols)`
     guard to `>` (over-aliasing only) and rename only the first
     `len(cte.Columns)` columns, leaving the rest with their query names. Files:
     `internal/analyzer/analyzer.go`, `internal/planner/with.go`. (The DML-CTE
     block at `with.go` keeps its `== len(schema)` rename gate — it does not
     error on under-aliasing, it merely skips the prefix rename; that path is not
     exercised by amcheck and is left bounded.)

  Tests: `server.TestConnectNonexistentDatabaseRejected` (FATAL 3D000 + close),
  `server.TestConnectBootstrapDatabasesAccepted` (postgres/template1/template0
  pass the check), `catalog.TestNewInMemorySeedsPostgresDatabase` (3 bootstrap
  DBs), `analyzer.TestAnalyzeWithFewerColumnAliasesAccepted`,
  `planner.TestPlanWithFewerColumnAliasesAccepted` (sibling twins). Live-cluster
  smoke verified: `psql -d qqq` → `FATAL: database "qqq" does not exist`;
  `pg_database` shows the three rows with correct `datallowconn`/`datistemplate`;
  the amcheck filter yields `{postgres, template1}`. Full `internal/server`,
  `internal/analyzer`, `internal/planner`, `internal/catalog`, `internal/executor`
  suites green; `go build ./...` + gofmt clean.

- **AC-002 gap #4 (CTE not visible inside a FROM-subquery of the main statement)
  — LANDED.** With gaps #1/#2/#2b/#3 closed, `002_nonesuch`'s
  database-resolution bootstrap query advanced to its FINAL select,
  `SELECT … FROM (SELECT … FROM filtered_databases) AS combined_records`, and
  errored `relation "filtered_databases" does not exist`. Root cause isolated to
  a minimal reproducer: `WITH x(a) AS (SELECT 1) SELECT a FROM (SELECT a FROM x) s`
  → `relation "x" does not exist`. The **analyzer** already resolved this
  correctly (`resolveTable` walks the parent scope chain for CTEs); the bug was
  in the **planner**. `planSubqueryRangeVar`'s non-correlated branch
  (`lateralCtx == nil`) re-planned the derived table via `Plan(rv.Subquery, cat)`,
  which re-runs `analyzer.Analyze` on the subquery **standalone** — without the
  enclosing WITH scope — and rejects the CTE reference as a missing relation. The
  fix routes that branch through `planSelectWithParent(rv.Subquery, cat, nil)`
  (one line): it skips the analyzer re-pass (the outer `Plan()` already analyzed
  the whole tree under the correct scope) and inherits the package-level
  `planCTEs` map, so the CTE substitutes in. This mirrors the lateral branch,
  which never had the bug. Regression coverage:
  `executor.TestCompatCTEVisibleInFromSubquery` (single + two-level nesting,
  end-to-end). `internal/planner/planner.go`.

- **AC-002 gap #5 (schema-qualified function in the FROM clause) — LANDED.**
  pg_amcheck builds each heap check as
  `… FROM pg_catalog.pg_class c, "public".verify_heapam(...) v` — a
  **schema-qualified** function call in FROM. goopg's `parseRangeVar` only
  dispatched an SRF when the name was **unqualified or pg_catalog-qualified**
  (`obj.Schema == "" || strings.EqualFold(obj.Schema, "pg_catalog")`); a
  user-schema qualifier (`public`) fell through to the derived-subquery branch
  and errored `expected ')' after subquery in FROM (got ()`. Fix
  (`internal/parser/select.go`, `parseRangeVar`): restructure the gate so that in
  FROM-clause context a **schema-qualified** `name(args)` is also accepted — the
  schema qualifier is discarded and dispatch is by bare name (builtins by their
  lowercased canonical name so the executor's name switch matches; everything
  else as a user-defined SRF). The pre-existing unqualified / pg_catalog behavior
  is preserved exactly. Regression: `TestParseSchemaQualifiedFromSRF`
  (`internal/parser/named_arg_colon_equal_test.go`, quoted + bare schema forms,
  sibling to the existing `TestParseNamedArgColonEqualFromSRF`).

- **Remaining for AC-002 — gap #6 (LATERAL `c.oid` resolution into the SRF's
  arguments).** With gap #5 closed, `002_nonesuch` parses the per-relation heap
  check and reaches the executor, surfacing the next gap: the implicit-LATERAL
  comma-join `FROM pg_catalog.pg_class c, "public".verify_heapam(relation := c.oid, …)`
  does not resolve the correlated `c.oid` inside the SRF's argument list against
  the sibling `pg_class c` range-table entry, so verify_heapam errors
  `column "oid" does not exist`. This is the same LATERAL-resolution follow-up
  S3/S4 already record, now the live blocker. Until it lands, `002_nonesuch`
  self-skips via its updated gap-#6 preflight probe (runs `pg_amcheck postgres`,
  detects the `column "oid" does not exist` stdout signature).

- **After #6:** S5 still needs the clog `XidStatusFunc` wiring (for the
  clog-dependent verify_heapam tier), and the `AC-002`…`AC-005` TAP port + CSV
  flip.

## Deferral

S1+S2+S3+S4 landed (above) in worktree `m0110-0003-amcheck-sql-surface`; S5
remains. Merging the worktree to the active branch still waits for a clean tree
(the main-tree foreign gen-column WIP). Resume point is **Slice S5** (port the
`AC-002`…`AC-005` pg_amcheck TAP tests, add named-arg/LATERAL + clog wiring).
See `.ralph/deferral_ledger.md`.

**S4 carries three intentional follow-ups** (recorded so S5 does not re-discover
them):

1. **`heapallindexed` (heap↔index completeness)** — the deepest tier. The engine
   seam exists (`amcheck.VerifyBtreeHeapAllIndexedRelation` +
   `CollectBtreeLeafEntries`), and the executor already has the heap-tuple →
   index-key encoder (`encodeIndexKeyFromCols`), but forming the MVCC-visible
   heap entry set (which tuples *should* be indexed) is the missing piece. The
   default pg_amcheck B-tree probe passes `heapallindexed := false`, so S4 serves
   it; the arg is accepted and the structural tiers always run.
2. **`rootdescend` / `checkunique`** (`bt_index_parent_check` only) — accepted but
   their bt_index_parent_check-specific deeper checks (root-to-leaf re-descent,
   cross-entry uniqueness) are not yet wired; the parent-downlink structural tier
   is active.
3. **named-argument + LATERAL call shape** — pg_amcheck issues
   `bt_index_check(index := c.oid, heapallindexed := false)` correlated against
   `pg_class ⋈ pg_index`. **Named-argument parsing landed in S5** (see
   "S5 (in progress)" above) — both `:=`/`=>` spellings are now stripped
   positionally in both the expression and FROM-clause paths, so the call *parses*.
   The remaining piece, shared with S3's identical follow-up, is **LATERAL
   resolution** of the `c.oid` outer reference at plan/exec time.

**S3 carries two intentional follow-ups** (recorded so S5 does not re-discover
them):

1. **clog-backed `XidStatusFunc`** — the executor `Context` has no clog handle
   (only `TxnMgr`, which does not hold the `CLog`), so the operator passes a nil
   `XidStatus`, which disables exactly the clog-dependent HOT-chain tier. The
   page-structural, natts, and xmin/xmax numeric-bounds tiers (all clog-free) are
   active. Wiring clog through `Context` enables the remaining tier.
2. **named-argument + LATERAL call shape** — pg_amcheck itself issues
   `verify_heapam(relation := c.oid, on_error_stop := false, …)` correlated
   against `pg_class` (a LATERAL cross join with `:=` named args). **Named-argument
   parsing landed in S5** (see "S5 (in progress)" above): the FROM-clause SRF arg
   loop now strips `:=`/`=>` names so the six-arg form parses with `c.oid` as the
   first positional `ColumnRef`. The positional executor path (`verify_heapam('t')`,
   `verify_heapam('t', false, false, 'none', 0, 5)`) is unchanged and still
   verified by the direct Go executor test. The remaining piece is **LATERAL
   resolution** of `c.oid`, part of the S5 TAP port.
