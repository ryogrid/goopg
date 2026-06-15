# 0110-0008 — amcheck SQL surface wiring plan (M0110-0003)

**Status:** S1+S2 landed (worktree `m0110-0003-amcheck-sql-surface`); S3–S5 remain (merge blocked on a clean working tree)
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
| **S3** | `verify_heapam(...)` SRF executor — now a **thin adapter** over `amcheck.VerifyHeapRelation` (the block loop already lives in the engine, `verify_heapam_relation.go`): fill a `PageSource` from smgr, pass `nblocks` + `RelDesc.Natts` + a clog-backed `XidStatusFunc`, stream `HeapRelReport`s as rows | AC-004 (heap path) | `planner/plan.go`, `planner/planner.go`, new `executor/operators_verify_heapam.go` |
| **S4** | `bt_index_check` / `bt_index_parent_check` SRFs over `amcheck.VerifyBtree*` (+ heapallindexed) | AC-003/004 (index path) | `planner/plan.go`, new `executor/operators_bt_index_check.go` |
| **S5** | port `002`→`005` TAP tests; flip CSV `AC-002…AC-005`→`port`; regen md | — | `internal/testport/pgamcheck_port_test.go`, `docs/test-port/*` |

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
- **Remaining:** S3 `verify_heapam` SRF, S4 `bt_index_check`/`bt_index_parent_check`
  SRFs, S5 port the `AC-002`…`AC-005` pg_amcheck TAP tests. Per the scope
  refinement above, `002_nonesuch` (`AC-002`) exercises only `CREATE EXTENSION
  amcheck` + the install probe, so S1+S2 satisfy its *server-side* requirement;
  promoting `AC-002` to `port` still needs S5 (the TAP port itself) plus the SRFs
  to merely *exist* in `pg_proc`.

## Deferral

S1+S2 landed (above) in worktree `m0110-0003-amcheck-sql-surface`; S3–S5 remain.
Merging the worktree to the active branch still waits for a clean tree (the
main-tree foreign gen-column WIP). Resume point is **Slice S3** (`verify_heapam`
SRF). See `.ralph/deferral_ledger.md`.
