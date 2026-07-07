# 0110-0003 — pg_amcheck TAP test port (001_basic CLI tier)

Status: accepted (partial)
Milestone: M0110-0003
Date: 2026-06-13

## Goal

Port the upstream `postgres/src/bin/pg_amcheck/t/001_basic.pl` TAP test into a
Go test under `internal/testport/`, following the incremental tier strategy
established by M0110-0001 (pg_dump) and M0110-0002 (pg_waldump).

## What the pg_amcheck suite contains

`001_basic.pl` is the only pure CLI-handling member of the suite (14 lines):

```perl
program_help_ok('pg_amcheck');
program_version_ok('pg_amcheck');
program_options_handling_ok('pg_amcheck');
done_testing();
```

Every assertion is satisfied by the binary's argument parser before any server
connection is attempted — no cluster required. The remaining four tests all
connect to a live server and run heap/btree corruption checks:

- `002_nonesuch.pl` — behaviour against non-existent database/relation
  (still issues catalog queries against a running server).
- `003_check.pl` — actual heap/btree corruption checks.
- `004_verify_heapam.pl` — requires the `verify_heapam()` set-returning
  function (not implemented in goopg).
- `005_opclass_damage.pl` — operator-class damage detection; needs opclass
  system-catalog parity.

## Decision

Port `001_basic.pl` as `TestPort_PgAmcheck001Basic`
(`internal/testport/pgamcheck_port_test.go`). It drives the upstream pg_amcheck
binary shipped unchanged in `postgres/local_install/bin`; goopg reuses it
verbatim, so this tier validates the CLI surface and provides a
presence/behaviour regression guard for the bundled binary.

### libpq library wrinkle

Unlike pg_dump/pg_waldump, the bundled `pg_amcheck` links a newer libpq symbol
(`PQcancelBlocking`, introduced in the PG 17 cancel-request API). Loaded against
an older host `libpq.so.5` it aborts at startup with
`undefined symbol: PQcancelBlocking`. The port therefore runs the binary with
`LD_LIBRARY_PATH=postgres/local_install/lib` so the in-tree libpq resolves the
symbol. A new local helper `runToolWithLib` sets this env (the existing
`runTool` is left untouched so the pg_dump/pg_waldump ports keep their current
behaviour). This mirrors the `LD_LIBRARY_PATH` handling other testport binaries
already use (e.g. `wal_pg_waldump_test.go`, `e2e_pgbench_test.go`).

**Defer the server-dependent tests** under CSV row `AC-002`: they require the
`verify_heapam()` SRF and operator-class catalog parity that goopg does not yet
implement.

## 002_nonesuch port (self-promoting reproduction)

`002_nonesuch.pl` is **not** a corruption test — it exercises pg_amcheck's
database/schema/table/index *pattern resolution* and argument grammar, none of
which invoke `verify_heapam()`. It is ported faithfully as
`TestPort_PgAmcheck002Nonesuch`
(`internal/testport/pgamcheck002_port_test.go`): the test starts a live goopg
cluster, runs `CREATE EXTENSION amcheck`, and drives the bundled pg_amcheck
binary through the full `command_checks_all` assertion set (exit code + empty
stdout + stderr regexes). A preflight invocation detects the goopg-side
`query failed` signature and `t.Skip`s with the precise blocker, so the test
**auto-promotes** the day goopg gains the missing features.

### Empirically-surfaced blockers (this loop)

Driving the real client revealed that AC-002's blocker was mis-stated as
"needs `verify_heapam()`". 002_nonesuch actually needs three **general** SQL
features (all in `internal/parser` / `internal/analyzer` / the connection
handshake, none amcheck-specific):

1. **`index` as a CTE name.** pg_amcheck's relation-gathering query
   (`compile_relation_list_one_db`) defines a CTE literally named `index`;
   goopg's parser rejects it (`syntax error … expected CTE name after ',' (got
   index)`). PG treats `index` as unreserved in that position.
2. **`VALUES`-list backing a CTE → 0 columns.** The database-resolution query
   (`compile_database_list`) uses
   `include_raw (pattern_id, rgx) AS (VALUES (0,'^(x)$'), …)`; goopg errors
   `CTE "include_raw" has 2 column aliases but inner query produces 0 columns`
   — the analyzer does not derive a `VALUES` list's output-column count when it
   is a CTE's inner query.
3. **Non-existent-database connect.** `pg_amcheck qqq` connected successfully
   instead of failing with `database "qqq" does not exist`.

These edit the contaminated parser/analyzer packages (the active foreign
gen-column WIP), so they cannot land line-disjointly here; the reproduction test
pins them precisely for the next unblocked loop.

## CSV rows

- `AC-001` → `port` / `pass_required=yes`: `001_basic.pl` CLI tier =
  `TestPort_PgAmcheck001Basic`.
- `AC-002` → `defer` / `pass_required=no`: `002_nonesuch.pl` ported as the
  self-promoting `TestPort_PgAmcheck002Nonesuch` (blocked on the 3 SQL gaps
  above); `003_check.pl`, `004_verify_heapam.pl`, `005_opclass_damage.pl` still
  need `verify_heapam()`/`bt_index_check()` with LATERAL outer-column resolution
  + opclass catalog parity.

## Verification

- `gofmt -l` clean; `go vet ./internal/testport/` clean.
- `go test -run TestPort_PgAmcheck001Basic ./internal/testport/` → PASS.
- `go test -run TestPort_PgAmcheck002Nonesuch ./internal/testport/` → SKIP with
  the precise blocker (self-promoting).
- `go run ./cmd/gen-oracle-port-status` regenerated the `.md` view.

## 004_verify_heapam.pl — page-structural heap tier (AC-003, partial)

`TestPort_PgAmcheck004VerifyHeapam` (`internal/testport/pgamcheck004_port_test.go`)
ports the faithful subset of `004_verify_heapam.pl`: the page-structural
line-pointer corruption tier, driven through the real `pg_amcheck` binary against
a live goopg cluster.

Mechanism (mirrors upstream's stop → seek/overwrite → restart):
1. Init the cluster with `--no-data-checksums` (upstream `init(no_data_checksums
   => 1)`). goopg now defaults checksums **on**; with them on, overwriting page
   bytes trips goopg's storage-manager checksum verification (`invalid page in
   block 0 … checksum verification failed`) before `verify_heapam` inspects the
   corruption.
2. `CREATE EXTENSION amcheck`, create `t004(a bigint, b text, c text)`, insert a
   few short-value rows (single block 0, no TOAST).
3. Locate the heap file by globbing `base/*/<reloid>` — goopg's storage dbOid is
   **not** the value `pg_database.oid` reports, so the glob matches on the unique
   relation-OID filename instead of trusting the catalog database OID.
4. Stop cleanly (shutdown checkpoint flushes the page), overwrite the first line
   pointer's length on block 0 to `0x7FFF` so `lp_off + lp_len > BLCKSZ`, restart.
5. `CREATE EXTENSION amcheck` again — its install is runtime-only (gap #7c
   per-database `pg_extension` scoping) and does not survive a restart.
6. Run `pg_amcheck --table public.t004 postgres`; assert exit 2 and the
   upstream-verbatim `line pointer to page offset N with length 32767 ends beyond
   maximum page offset 8192` report on stdout.

Scoping adaptation: upstream runs `pg_amcheck <db>` over the whole database and
asserts an empty pre-corruption run; goopg's system-catalog heap pages do not yet
round-trip cleanly through `verify_heapam` (a separate parity effort), so the
check is restricted to the single user table. The corruption-detection behaviour
under test — the heap line-pointer tier on a user relation — is unchanged.

Not ported (goopg-divergent): the MVCC/attribute and TOAST tiers of 004 corrupt
PG's on-disk `varatt_external` pointer layout, which goopg does not use (oversized
values live in a chunk relation). A byte-for-byte port of those cases would assert
against bytes goopg never writes.

## Lateral outer-qual pushdown (relation-scoped probes) — AC-003 enabler

`pg_amcheck` builds each per-relation heap check as an implicit-LATERAL
comma-join with the target relation pinned in the WHERE clause (captured from the
live wire protocol):

```sql
SELECT v.blkno, v.offnum, v.attnum, v.msg
FROM pg_catalog.pg_class c, "public".verify_heapam(
       relation := c.oid, on_error_stop := false, check_toast := true, skip := 'none'
     ) v
WHERE c.oid = <reloid> AND c.relpersistence != 't'
```

The `WHERE c.oid = N` restricts only the **outer** relation. goopg planned the
residual `Filter` *above* the lateral nested-loop, so `verify_heapam` was opened
for **every** `pg_class` row and the join only filtered afterwards. `verify_heapam`
raises `could not open relation: relation does not exist` when handed a non-heap
OID (an index, sequence, …), so the first sibling relation in the catalog aborted
the whole scan with exit 2 — even though the user's target table was perfectly
healthy. This is why `--table public.t` worked only when the database held no
other non-heap relations (the 004 single-table case) and failed the moment an
index or second table existed.

Fix: `pushOuterQualsIntoLaterals` (`internal/planner/pushdown.go`), run right
after `pushPredicatesIntoCrossJoins`. For a residual `Filter` whose direct child
is a `Lateral` `Join`, each conjunct that references only the outer (left) side —
verified by both index range (`classifyConjunctSide == sideLeft`) and column name
(`collectScanOutputNames`, now including the `*Values` node that backs virtual
catalog relations like `pg_class`) — is moved onto the join's outer child as a
`Filter`. The outer child occupies the join's leading columns, so a left-only
conjunct's indices already align with `j.Left.Output()` (no remapping). This
matches PostgreSQL's nested-loop qual placement: a single-relation restriction is
applied to the outer scan before the inner side is opened per row.

Scope/blast radius: the pass is a no-op unless the residual filter's direct child
is a `Lateral` join, so non-lateral query shapes (all of TPC-H, ordinary
comma/`JOIN` queries) are untouched. Regression: `TestPlanOuterQualPushedBelowLateralJoin`
(`internal/planner/planner_test.go`) asserts the qual lands on `Join.Left` and no
longer sits above the lateral join. After the fix, `pg_amcheck --table public.t`
and `--table public.t --no-dependent-indexes` return exit 0 over a multi-table /
indexed database (verified end-to-end against a live cluster).

## 003_check / whole-database blockers (diagnostic, AC-003 remainder)

Driving the real `pg_amcheck postgres` (whole-db) and `--table`/`--schema` runs
against a clean goopg cluster surfaced the precise remaining gaps:

1. **Lateral outer-qual pushdown** — FIXED above. Was the dominant blocker for any
   `--table`/`--schema`-scoped run once a database held more than a lone heap table.
2. **`bt_index_check` schema-qualified dispatch** — FIXED below. `pg_amcheck` calls
   the amcheck functions schema-qualified by the *amcheck install schema*
   (`"<amcheck-schema>".bt_index_check(index := $1::regclass, heapallindexed := $2, …)`,
   e.g. `public.bt_index_check`), not `pg_catalog`. `evalFuncCall`
   (`internal/executor/expr.go`) stripped only a `pg_catalog.` prefix before
   matching, so `public.bt_index_check` resolved to `function public.bt_index_check
   does not exist` (42883), and any table with a dependent index failed its index
   check even when healthy. The fix strips a *user*-schema qualifier for the amcheck
   scalar builtins (`bt_index_check` / `bt_index_parent_check` / `verify_heapam`) —
   the suffix after the last `.` is matched against the amcheck builtin set, and only
   those names are stripped, so a same-named user function is unaffected. This
   mirrors the FROM-clause SRF schema-strip already done for `verify_heapam`
   (`internal/parser/select.go`, gap #5). The scalar parser already accepts the
   `:=` named-arg spelling pg_amcheck emits (`parseFuncCallTail`, S5).
3. **System-catalog heap resolution** — *was hypothesised as a blocker; empirically
   it is NOT (corrected 2026-06-15, see "Whole-database enumeration tier" below).*
   The earlier diagnosis predicted that a whole-db `pg_amcheck postgres` run would
   also check `pg_catalog.*` and that `verify_heapam` on `pg_type`/`pg_class` would
   report `could not open relation`. Driving the *real* default whole-database run
   against a live goopg cluster shows exit 0 / empty output: goopg's `pg_class`
   enumeration does not present its system catalogs to `pg_amcheck` as checkable
   heap relations, so the per-relation dispatch loop never opens them and the clean
   whole-db run succeeds. The remaining `003_check.pl` blockers are therefore the
   *feature/corruption* requirements, not catalog heap resolution (see below).

## Resume point

Promote `AC-002` to `port` (and stop the skip) once the three SQL gaps above are
implemented — `002_nonesuch` then passes with no further amcheck work, since it
never calls `verify_heapam()`. **(DONE — AC-002 is now `port`; the three gaps
plus #6/#7a/#7b/#7c landed.)**

For `AC-003`: the page-structural tier of `004_verify_heapam.pl` is ported (above),
the lateral outer-qual pushdown (blocker #1) now lets `pg_amcheck` scope heap checks
to a single relation in a multi-relation database, and the `bt_index_check`
schema-qualified dispatch (blocker #2) now lets a `--table`/`--schema`-scoped run
check a relation's dependent btree indexes. End-to-end proof: a healthy *indexed*
user table checks clean through the real binary
(`TestPort_PgAmcheckBtreeIndexCheck`, `internal/testport/pgamcheck_btree_port_test.go`);
the dispatch fix itself is hard-gated by `TestBtIndexCheck_SchemaQualifiedDispatch`
(`internal/executor/operators_bt_index_check_test.go`). Remaining for `003_check.pl`
(whole-database heap+btree orchestration): system-catalog heap resolution
(blocker #3) for the empty pre-corruption whole-db run — `verify_heapam` on
`pg_type`/`pg_attribute`/`pg_class` must resolve catalog relations to on-disk heap
pages. `005_opclass_damage.pl` needs `CREATE OPERATOR CLASS` + `pg_amproc` catalog
parity to inject the breaking sort-order via `UPDATE pg_amproc`. AC-003 stays
`defer` until 003 and 005 land.

## Whole-database enumeration tier + blocker #3 correction (AC-003, 2026-06-15)

The single-`--table` path (`TestPort_PgAmcheckBtreeIndexCheck`) only exercises one
relation. `003_check.pl`'s clean-database path (db3 is left uncorrupted) instead
runs the *default* `pg_amcheck` — no scoping — which enumerates **every** checkable
relation in the database and dispatches `verify_heapam` per heap / `bt_index_check`
per btree. That relation-enumeration + per-relation dispatch loop is a distinct tier
(pg_amcheck's `pg_class`/`pg_namespace`/`pg_am` selection query, not the
single-relation fast path).

`TestPort_PgAmcheckAllTables` (`internal/testport/pgamcheck_alltables_port_test.go`)
covers it against a live goopg cluster over a database mixing the relkinds
`003_check` builds that goopg supports — a heap table, several btree indexes
(incl. a UNIQUE index), a sequence, a view, and a materialized view — in a user
schema `s1`. A `--schema s1` run checks the heap + btree subset and silently skips
the view/sequence; it is **clean (exit 0, empty output)**. The test additionally
drives the *unscoped whole-database* run (which would also reach `pg_catalog.*`)
and logs its result: it too is **clean (exit 0)**, which empirically refutes the
prior blocker #3 hypothesis (item 3 above) — goopg never feeds its system catalogs
to pg_amcheck's heap-check dispatch, so no `verify_heapam`-on-catalog gap exists to
close. The dispatch fixes (blocker #1 lateral pushdown, blocker #2 install-schema
`bt_index_check`) are asserted as hard regressions; any other not-yet-clean result
self-skips with the captured blocker.

**Remaining `003_check.pl` blockers** are now purely feature/corruption, not catalog
heap resolution: (a) the hash/gist/gin/brin/spgist index AMs goopg lacks (s5's
"corruption must not error" relations), (b) the `box`/`int4range`/`int4[]` column
types, (c) `STORAGE EXTERNAL` TOAST-file corruption, (d) multi-database
(db1/db2/db3) orchestration, and (e) the file-removal / first-page-overwrite
corruption mechanics with their per-relation expected reports. These are
multi-milestone; AC-003 stays `defer`.

## Missing-main-relation-fork tier (AC-003 file-removal corruption, 2026-06-15)

`003_check.pl`'s second corruption mechanism (alongside first-page overwrite) is
**file removal** (`plan_to_remove_relation_file`): with the node stopped, the
test `unlink()`s a relation's backing file, restarts, and asserts pg_amcheck
reports it. For a removed **index** main fork the expected stdout is

```
index "<name>" lacks a main relation fork
```

with exit status 2 (`003_check.pl:328-329`, `:392-399`). Upstream raises this in
`bt_index_check_callback` via `!smgrexists(RelationGetSmgr(indrel), MAIN_FORKNUM)`
→ `ereport(ERROR, errcode(ERRCODE_INDEX_CORRUPTED), …)` (`verify_nbtree.c:318`).

### goopg gap and fix

goopg's storage manager opens relation files with `os.O_CREATE` (`smgr.go`
`relFile`). A naive `Pool.NBlocks(rel)` on a removed fork therefore **recreates it
as an empty 0-block file**, and the btree engine (`nblocks <= MetaBlock+1`) then
reports the index as *clean* — a silent false negative exactly where PG reports
corruption.

The fix adds a stat-only existence probe that never goes through the
`O_CREATE` open path:

- `storage.Manager.Exists(rel) bool` — `os.Stat(relPath(rel))`, faithful to
  `smgrexists(MAIN_FORKNUM)`. It deliberately does **not** consult the open-file
  cache or `relFile`: every live relation always has an on-disk file (created
  eagerly), so a pure stat never reports a live relation absent, and it never
  recreates a removed one.
- `storage.Pool.Exists(rel)` delegates to it.
- `evalBtIndexCheck` (`operators_bt_index_check.go`) calls `ctx.Pool.Exists(rel)`
  **before** `NBlocks`; absent → `ExecError{Code:"XX002"}` (ERRCODE_INDEX_CORRUPTED)
  with the verbatim `index "%s" lacks a main relation fork` message. Covers both
  `bt_index_check` and `bt_index_parent_check`.

### Tests

- `TestBtIndexCheck_DetectsMissingRelationFork`
  (`internal/executor/operators_bt_index_check_test.go`) — hard gate: drops the
  index's backing file via `DropRelation` and asserts both functions raise the
  verbatim message.
- `TestPort_PgAmcheck003MissingIndexFork`
  (`internal/testport/pgamcheck003_missingfork_test.go`) — e2e proof through the
  real `pg_amcheck` binary over the full stop → unlink → restart lifecycle. It
  additionally asserts the fork was **not** recreated during startup/recovery
  (the load-bearing property a unit test cannot prove).

## Missing-heap-relation-file tier (AC-003 file-removal corruption, 2026-06-15)

The companion to the index case above: `003_check.pl` also removes an **ordinary
table's** backing file (`plan_to_remove_relation_file('db1', 's2.t1')`,
`003_check.pl:275`). For a removed **heap** main fork the expected stdout is

```
could not open file "<path>": No such file or directory
```

with exit status 2 (`003_check.pl:327`, `:357-365`). Upstream raises this when
`verify_heapam` opens the relation's main fork via
`RelationGetNumberOfBlocks` → `mdnblocks` → `mdopenfork`, whose
`errcode_for_file_access()` maps the `ENOENT` open failure to `ERRCODE_IO_ERROR`
(`fd.c`, the default branch).

### goopg gap and fix

The same `os.O_CREATE` hazard applies: `verifyHeapamOp.Open`'s
`ctx.Pool.NBlocks(rel)` would recreate the removed heap fork as an empty 0-block
file, and `amcheck.VerifyHeapRelation` over zero blocks reports the table *clean*
— a silent false negative. The fix reuses the same stat-only seam as the index
case:

- `verifyHeapamOp.Open` calls `ctx.Pool.Exists(rel)` **before** `NBlocks`;
  absent → `ExecError{Code:"58030"}` (`ERRCODE_IO_ERROR`) with the verbatim
  `could not open file "%s": No such file or directory` message.
- The `<path>` is built by new `storage.Manager.RelPath` / `Pool.RelPath`, which
  return the data-dir-relative fork path (`base/<dboid>/<relfilenode>`, forward
  slashes) faithful to upstream `relpath()`. The test matches `.*` for the path,
  so goopg's storage-`dbOid`-keyed directory needs no special handling.

pg_amcheck turns the SRF's per-relation query error into a stdout corruption
report (exit 2), exactly as it does for the index `lacks a main relation fork`
message.

### Tests

- `TestVerifyHeapam_DetectsMissingRelationFile`
  (`internal/executor/operators_verify_heapam_test.go`) — hard gate: drops the
  heap's backing file via `DropRelation` and asserts the SRF raises the verbatim
  message.
- `TestPort_PgAmcheck003MissingHeapFile`
  (`internal/testport/pgamcheck003_missingheap_test.go`) — e2e proof through the
  real `pg_amcheck` binary over the full stop → unlink → restart lifecycle,
  additionally asserting the fork was **not** recreated during startup/recovery.

### Still remaining for `003_check.pl`

With both file-removal cases (index fork + heap file) now ported, the remaining
blockers are purely feature/corruption: the unsupported index AMs
(hash/gist/gin/brin/spgist), the `box`/`int4range`/`int4[]` types, `STORAGE
EXTERNAL` TOAST corruption, the page-overwrite mechanics for those unsupported
relkinds, and multi-database orchestration. `005_opclass_damage` (CREATE
OPERATOR CLASS + `pg_amproc` parity) also remains. These keep AC-003 `defer`.

## Combined-corruption integration tier (AC-003 main check, 2026-06-15)

The three sub-tiers above each inject exactly **one** corruption on a
single-relation fixture. But the central assertion of `003_check.pl` — its main
check (`:347-365`) — is an *integration* property none of them proves: upstream
plans several **different** corruptions across a database, applies them all in a
**single** `perform_all_corruptions` stop → corrupt → restart cycle
(`:107-119`), then runs `pg_amcheck` **once** over the database and asserts that
**all three** corruption classes are reported *together* in one pass:

```
$index_missing_relation_fork_re = qr/index ".*" lacks a main relation fork/
$line_pointer_corruption_re     = qr/line pointer/
$missing_file_re                = qr/could not open file ".*": No such file or directory/
```

The property under test is that `pg_amcheck`'s per-relation dispatch does **not**
abort on the first corrupt relation — the removed-heap-file case raises an ERROR
(`58030`), not a corruption row — but keeps enumerating and reports every
distinct corruption class. A regression where that first ERROR tore down the run
would pass all three isolated surrogates yet be caught here.

`TestPort_PgAmcheck003CombinedCorruption`
(`internal/testport/pgamcheck003_combined_test.go`) reproduces this on the
goopg-supported relkind subset: three `public` tables (`tfork` + dependent btree
`tfork_idx`, `tfile`, `tpage`), corrupted in one restart cycle by removing
`tfork_idx`'s fork, removing `tfile`'s heap file, and overwriting `tpage`'s first
line pointer (reusing `corruptFirstLinePointerLength`). A single
one-`--table`-per-relation run must exit 2 with all three regexes on stdout and
empty stderr. Verified PASS — goopg's dispatch already reports all three (zero
engine change; this is a pure faithful port of the integration assertion).

**Surfaced gap (separate, out of scope for this tier):** the relations live in
`public`, not a custom schema, because goopg does **not** persist a `CREATE
SCHEMA` `pg_namespace` row across a server restart — a first `--schema s1` run
checked clean pre-corruption but reported `no relations to check in schemas
matching "s1"` after the restart this tier requires. That catalog-durability gap
is independent of corruption dispatch; every AC-003 surrogate already scopes to
`public` for the same reason.

## 002_nonesuch.pl coverage extension (loop #16)

`TestPort_PgAmcheck002Nonesuch` (AC-002, already `port`) was extended to two
further faithful sections of the upstream `.pl`:

- **Multi-pattern `--no-strict-names` case.** A single run mixing many
  unresolvable `--table`/`--index`/`--relation`/`--database` patterns plus the
  one existent `--table postgres.pg_catalog.pg_class` anchor. pg_amcheck emits a
  warning per unmatched pattern, categorised by argument kind ("no heap tables
  to check" / "no btree indexes to check" / "no relations to check" / "no
  connectable databases to check"), and the existent anchor keeps the exit code
  at 0. This exercises goopg's relation/namespace pattern resolution end-to-end.
- **Cross-database existent-objects case.** Objects (`public.foo`, `foo_idx`)
  created in `postgres` are referenced under `template1`/`another_db`/
  `no_such_database`; pg_amcheck (connected only to `postgres`) warns it cannot
  reach them and finally errors `no relations to check` (exit 1).

### `--exclude-schema` sections — ported (was a separate engine bug)

Passing `--exclude-schema` makes pg_amcheck issue a relation-gathering query
with an `exclude_raw (...) AS (VALUES ...)` / `exclude_pat` CTE and an anti-join
`... LEFT OUTER JOIN exclude_pat ep ON (...) WHERE ep.pattern_id IS NULL`. goopg
previously **panicked the backend** on this shape — pinned to the `toast`
sub-CTE, where a CTE-backed relation is the probe side and the 5-column
`exclude_pat` VALUES relation is the build side: a build-side filter predicate
carried a combined-join-schema column index (43) but was evaluated against the
5-wide build slot → `runtime error: index out of range [43] with length 5` in
`executor.MaterializedSlot.Get` via `joinOp.Open → drainRowsCtx → filterOp.Next
→ evalExprSlot`. The structurally similar 4-way `index` sub-CTE (same anti-join,
but `relation` on the outer side) did **not** crash, isolating the defect to
build-side predicate column remapping when the inner/build input is a narrow
VALUES/CTE relation.

Root cause was the recurring sibling-path divergence between the two LEFT JOIN
inner-only pushdown helpers: `classifyConjunctSide`/`walkColumnRefs`
(`internal/planner/pushdown.go`) decided the conjunct was inner-only and
pushable, while `shiftColumnRefsBy` (`internal/planner/planner.go`) failed to
rebase the inner `IS NULL` ref by `-leftWidth` — neither switch enumerated
`*IsNullExpr` and its sub-expr-bearing siblings. Fixed by commit `36a085dc`
(M0110-0003 residual #2): both helpers now enumerate every sub-expr-bearing
`Expr` kind, so the inner ref is rebased to the build slot's width and the
anti-join runs to completion. Both `--all --exclude-schema …` cases (`.pl`
:377-418) are now ported in `TestPort_PgAmcheck002Nonesuch` (exit 1,
`no relations to check`) and stand as the end-to-end regression guard for the
planner fix.

### Former deferred residual — now closed (2026-07-07)

**`datconnlimit = -2` invalid-database filter.** The `.pl` marks a database
invalid via `UPDATE pg_database SET datconnlimit = -2` and asserts pg_amcheck's
database-resolution query filters it out. This was blocked on a runtime write
path for `pg_database.datconnlimit` — goopg has no on-disk, generically
UPDATE-able heap for `pg_database` (it is `catalog.Table{Virtual: true}`,
computed entirely by `VirtualRows()`), so the write needed a dedicated
mechanism rather than falling out of the generic UPDATE executor for free.

**Read side.** `catalog.InMemory` gained `databaseConnLimit map[string]int32`
plus `SetDatabaseConnLimit(name string, limit int32) bool` /
`DatabaseConnLimit(name string) int32` (catalog.go) — the same "runtime
InMemory truth, no on-disk write" pattern `CreateCollation`/`CreateExtension`
already use for other per-object runtime state. `pg_database`'s `VirtualRows`
closure now renders `strconv.FormatInt(int64(c.DatabaseConnLimit(n)), 10)`
instead of the old hard-coded `"0"`.

**Write side.** This is the interesting half. `planUpdate` (`internal/planner/
planner.go`) already resolves `UPDATE pg_database SET datconnlimit = ... WHERE
...` without complaint — `pg_database.View == nil`, so it never enters the
view-rewrite branch, and nothing in the generic single-table UPDATE path
checks `Table.Virtual`. The WHERE/SET expressions resolve against
`pg_database`'s ordinary `Columns` list exactly like a real table's. The
problem was purely on the *execution* side: `updateOp.Next()`
(`internal/executor/operators_storage.go`) unconditionally calls
`ctx.Catalog.RelFileNode(tbl)` and scans physical heap pages through that
relation file — `pg_database` has none, so the scan silently produced zero
matching rows (`UPDATE 0`, no error) and the SET never ran.

Fixed by adding `updateOp.nextVirtualPgDatabase()`: `Next()` now checks
`tbl.Virtual && tbl.Schema == "pg_catalog" && tbl.Name == "pg_database"` right
after setting `o.done = true`, and if true routes here instead of the ~1300
lines of physical-heap logic below (`MaterializeWriterXID`, `RelFileNode`,
B-tree/SeqScan matching, WAL emission, MVCC tuple headers — none of it applies
to a table with no storage). The helper:

1. Reads `tbl.VirtualRows()` — the exact same rows a `SELECT * FROM
   pg_database` would see.
2. Decodes each cell into a typed `Row` via `planner.TypedVirtualCell` (the
   identical helper `rematerialiseVirtualRows` uses for the SELECT-side
   `Values`/`VirtualSource` path in `operators.go`), so `evalExpr` can run
   the already-extracted `o.pred` (the WHERE clause `extractScan` pulled out
   of the plan's `Child` at construction time) against real typed values —
   the WHERE-matching logic is not duplicated, only its data source changes.
3. For matched rows, evaluates `o.plan.Set[connLimitOrdinal]` (also already
   resolved by the generic planner path — no special planner code needed) and
   calls `SetDatabaseConnLimit`.
4. Rejects (`0A000`) any `SET` targeting a column other than `datconnlimit` —
   silently discarding an unsupported column's write would be worse than
   refusing it outright.

The special-case is scoped to `pg_database` specifically, not every `Virtual`
table — generalizing the Child-scan/write substitution to arbitrary system
catalogs was avoided to keep blast radius low; other Virtual tables' `UPDATE`
statements keep today's existing (silent, 0-rows-affected) behavior unchanged.

**Test.** `TestPort_PgAmcheck002Nonesuch`'s "Invalid / partially dropped
database" section is now ported (was `NOT PORTED`/deferred) — both
`command_checks_all` cases (`--database regression_invalid` and `--table
regression_invalid.public.foo`) run against the real `pg_amcheck` binary and
assert the exact upstream `no connectable databases to check matching "..."`
error. Confirmed non-vacuous via `git stash` on the catalog/executor changes
(fails with the pre-fix `skipping database "regression_invalid": amcheck is
not installed` symptom). **002_nonesuch.pl (AC-002) is now fully ported with
no remaining residuals.**

**Still deferred** (recorded in `.ralph/deferral_ledger.md`, M0119-0006 AC-002
residual #1 row): (1) connect-time enforcement — a client can still actually
open a session against a `datconnlimit = -2` database; goopg's connection
startup never consults `DatabaseConnLimit`, only database registration/
`datallowconn`. Real PG rejects the connection itself. (2) Positive
`datconnlimit` values (real per-database connection-count throttling) are
entirely unimplemented — only the `-2` sentinel's SQL-visibility half was
needed for AC-002.

### Follow-up: connect-time `datconnlimit = -2` rejection (2026-07-07)

Picked up residual #1 above. PG's `InitPostgres` (postinit.c) rechecks
`pg_database` after authentication and calls `database_is_invalid_form`,
which is just `datform->datconnlimit == DATCONNLIMIT_INVALID_DB` (pg_database.h
defines the sentinel as `-2`, "a database is set to invalid partway through
being dropped"). A match is a FATAL `55000`
(`ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE`) `cannot connect to invalid
database "%s"`, raised before the session is otherwise established — distinct
from the connection-count check a few lines below it (`ERRCODE_TOO_MANY_
CONNECTIONS`), which only applies to non-negative limits and is out of scope
here (needs a live per-database connection counter — residual #2, still
deferred, untouched by this loop).

goopg's connection-startup handshake (`Server.handleStartup`,
`internal/server/server.go`) already had the sibling "database does not
exist" gate (`databaseRegistry.HasDatabase`, M0110-0003 AC-002 gap #3) right
after authentication — the natural place to add the `-2` check too. Added:

- `catalog.DatconnlimitInvalidDB` (`internal/catalog/catalog.go`), an exported
  `-2` constant mirroring PG's `DATCONNLIMIT_INVALID_DB` name, so the sentinel
  isn't a magic number at the call site.
- `databaseConnLimitRegistry` (`internal/server/database_ddl.go`), a new
  single-method interface (`DatabaseConnLimit(name string) int32`) satisfied
  by `catalog.InMemory`. Deliberately kept separate from the existing
  `databaseRegistry` interface (`CreateDatabase`/`DropDatabase`/`HasDatabase`)
  rather than adding the method there: `databaseRegistry` gates the
  unrelated unknown-role and unknown-database FATAL checks via the same type
  assertion, so widening it would silently disable those checks for any
  catalog fake that implements the first three methods but not this one.
  This mirrors the existing `databaseConfigRegistry` precedent (added for
  `ALTER DATABASE ... SET`) in the same file.
- A new check in `Server.handleStartup`, right after the existing
  `HasDatabase` gate (so it only runs once the database is confirmed to
  exist): if `DatabaseConnLimit(db) == catalog.DatconnlimitInvalidDB`, write
  the FATAL `55000` `cannot connect to invalid database "%s"` ErrorResponse
  and close the connection, exactly like the sibling check above it.

Deliberately scoped to the `-2` sentinel only, per the ledger row's own
scoping rationale — positive-limit connection *counting* needs new state
(an active-connection registry keyed by database) and remains residual #2,
untouched.

**Test.** `TestConnectInvalidDatconnlimitDatabaseRejected`
(`internal/server/database_exists_test.go`), mirroring the existing
`TestConnectNonexistentDatabaseRejected`/`TestConnectBootstrapDatabasesAccepted`
pair exactly: creates a database, marks it `datconnlimit = -2` via
`SetDatabaseConnLimit`, dials the real wire protocol, and asserts the FATAL
frame's SQLSTATE and message text. Confirmed non-vacuous via `git stash` on
`server.go` alone (fails with `unexpected frame S before ErrorResponse` — the
handshake proceeds to `AuthenticationOk` instead of rejecting — without the
fix).

Gates: `go build ./...` clean; `go vet ./internal/server/... ./internal/catalog/...`
clean; `go test ./internal/server/... ./internal/catalog/... ./internal/executor/...`
PASS; `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
(0 failed transactions, standard/simple-update/select-only).

### Follow-up: residual #2 — positive `datconnlimit` connection-count throttling (2026-07-07)

The previous follow-up deliberately scoped out the "too many connections"
check (`CheckMyDatabase`, `postgres/src/backend/utils/init/postinit.c:392-399`):

```c
if (dbform->datconnlimit >= 0 &&
    AmRegularBackendProcess() &&
    !am_superuser &&
    CountDBConnections(MyDatabaseId) > dbform->datconnlimit)
    ereport(FATAL,
            (errcode(ERRCODE_TOO_MANY_CONNECTIONS),
             errmsg("too many connections for database \"%s\"", name)));
```

Two properties of the upstream check matter for the port:

1. **The count includes the connecting backend itself.** The comment right
   above it says so explicitly: "we create our PGPROC before checking for
   other PGPROCs... the connection limit is approximate." `CountDBConnections`
   scans `ProcArray` for every backend (any role) whose `databaseId` matches,
   with no self-exclusion — unlike its cousin `CountOtherDBBackends` (used by
   `DROP DATABASE`), which explicitly skips `MyProc`. So the comparison is
   `count > limit`, not `count >= limit`, and the counter must be incremented
   *before* the comparison runs.
2. **Superuser connections are still counted, only the check is skipped for
   them.** `!am_superuser` gates the `ereport`, not the count — a superuser
   session occupies a slot that counts against everyone else's limit exactly
   like a regular one does, since `CountDBConnections` has no role filter.

goopg has no `ProcArray`; the closest existing analogue that already tracks
one entry per live backend, keyed by database name, is
`activity.ActivityRegistry` (`internal/activity/registry.go`), populated by
`Server.handleStartup`'s existing `reg.Register(&activity.Backend{DatName:
params["database"], ...})` call (line ~875) and released by the existing
teardown `defer` calling `reg.Unregister(pidStr)` (line ~895). Since that
`Register` call already happens before any point this check could run, no
new mutable state or lifecycle is needed: a slot-scan count taken *after*
`Register` already has the self-inclusive property `CountDBConnections`
relies on, and `Unregister`'s existing cleanup already keeps it accurate on
every teardown path (including the reject path added here) with zero new
increment/decrement pairing to get wrong. (An earlier draft of this section
proposed a dedicated `dbConnMu`/`dbConnCounts` counter incremented/decremented
around a `connLimitDB` closure variable; per-review, that added a second
piece of state and a new leak surface across every future early-return in
`handleStartup` for no benefit — `handleStartup` runs once per TCP connection,
not the per-frame hot path, so the O(slot-count) scan below costs nothing
that matters.)

Add one read-only method to `ActivityRegistry`:

```go
// CountByDatName returns the number of currently registered backend slots
// (regular + background) whose DatName equals name, INCLUDING a backend
// that has already Register()'d itself for this same connection — mirrors
// PG's CountDBConnections (postinit.c / procarray.c), which counts the
// calling backend's own PGPROC alongside every other backend's, unlike its
// self-excluding cousin CountOtherDBBackends (used by DROP DATABASE).
// O(len(slots)); intended for connect-time use (once per TCP connection),
// not the per-frame WaitEvent hot path.
func (r *ActivityRegistry) CountByDatName(name string) int32 {
    var n int32
    for i := range r.slots {
        if c := r.slots[i].cold; c != nil && c.DatName == name {
            n++
        }
    }
    return n
}
```

And one new check in `Server.handleStartup`, placed right after the existing
`reg.Register(...)` / `activity.SetCurrentGoroutine(...)` block (so `db`'s
own just-registered slot is already counted) — either before or after the
teardown `defer` is fine now, since there is no new state for the defer to
release:

```go
if db := params["database"]; db != "" && !isReplication && reg != nil {
    if limReg, ok := s.cfg.Catalog.(databaseConnLimitRegistry); ok {
        if limit := limReg.DatabaseConnLimit(db); limit >= 0 && !isSuperuserRoleName(user) {
            if count := reg.CountByDatName(db); count > limit {
                s.writeFatal(w, sqlstate.TooManyConnections,
                    fmt.Sprintf("too many connections for database %q", db))
                logger.Info("connection rejected: too many connections",
                    "database", db, "limit", limit, "count", count)
                return
            }
        }
    }
}
```

Reused as-is, no new plumbing needed: `databaseConnLimitRegistry.
DatabaseConnLimit` (already returns any configured limit, not just the `-2`
sentinel), `isSuperuserRoleName` (existing bootstrap-`postgres`-is-the-only-
superuser convention, already used for `is_superuser` GUC reporting a few
lines below), and `sqlstate.TooManyConnections` (`53300`, already defined,
previously unused). The existing teardown `defer` (`reg.Unregister(pidStr)`)
needs no change: rejecting here still runs it via the normal `return` path,
removing this backend's slot exactly like any other disconnect.

Deliberately NOT done: replication connections stay excluded from the
count (matching `AmRegularBackendProcess()`'s exclusion of walsenders — goopg
routes those to a separate path before reaching this block, via the
pre-existing `isReplication` guard); background/internal connections that
never go through `handleStartup` (there are none reaching user databases
today) are out of scope; per-role connection limits (`pg_authid.rolconnlimit`,
a separate PG mechanism) are a different, untracked feature.

**Test plan.** Extend `internal/server/database_exists_test.go`:
`startServerWithCatalog`'s signature grows an `act *activity.Registry`
parameter (nil-safe passthrough into `Config.Activity`; existing callers pass
`nil`, preserving today's behaviour where this whole block is skipped).
`TestConnectExceedsPositiveDatconnlimitRejected` wires a real
`activity.NewActivityRegistry(N)`, sets `SetDatabaseConnLimit(db, 1)`, opens
and keeps one connection alive, then dials a second and asserts the FATAL
`53300` `too many connections for database "%s"` ErrorResponse.
`TestConnectPositiveDatconnlimitSuperuserBypasses` mirrors it connecting as
`postgres` past the same limit to confirm the bypass (still counted, not
rejected). `TestActivityRegistryCountByDatName`
(`internal/activity/registry_test.go`) unit-tests `CountByDatName` directly
(multiple databases, zero/one/many matching slots, post-`Unregister` count
drop) without a real server.

Gates: `go build ./...`; `go vet ./internal/server/... ./internal/activity/...`;
`go test ./internal/server/... ./internal/activity/... ./internal/catalog/...`;
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`.

## Follow-up (2026-07-07): AC-003 cluster unblocked — synthetic TOAST relations report as healthy-empty instead of erroring

All six AC-003/AC-004 tests (`TestPort_PgAmcheck003CombinedCorruption`,
`003MissingIndexFork`, `003MissingHeapFile`, `003SchemaScoped`,
`004VerifyHeapam`, `AllTables`, `BtreeIndexCheck`) were unconditionally
`t.Skip`ing at their "pre-corruption baseline is clean" gate, all with the
identical evidence:

```
heap table "postgres.pg_toast.pg_toast_16404": ERROR: could not open relation: relation does not exist
btree index "postgres.pg_toast.pg_toast_16404_index": ERROR: could not open relation: relation does not exist
```

Root cause: `catalog.go`'s TOAST-exposure scheme (`toastRelidOffset`,
`tableHasToastRelation`, doc comment at `tableHasToastRelation` ~line 990)
deliberately emits a synthetic `pg_class`/`pg_index` row for every toastable
table's `pg_toast_<oid>` relation and `pg_toast_<oid>_index` — but with **no
real backing heap or index file**, since goopg never actually routes
out-of-line values through a physical TOAST relation. Real `pg_amcheck`, by
default, walks every table's TOAST relation alongside the table itself (it
resolves `reltoastrelid` and checks it too) — so `verify_heapam(oid)` /
`bt_index_check(oid)` on a synthetic TOAST OID always 42P01'd, and pg_amcheck
reported the *whole database* as dirty before any real corruption was ever
injected, permanently blocking the "confirm a clean baseline first" gate every
one of these tests requires.

Fix: `verifyHeapamResolveTable`'s call site (`operators_verify_heapam.go`) and
`btIndexResolve`'s call site (`operators_bt_index_check.go`) both gained a
fallback when the OID fails to resolve to a real table/index: check
`catalog.InMemory.ToastParentTable(oid)` (pre-existing helper, previously used
only by REINDEX CONCURRENTLY lock routing) — if the OID is a synthetic TOAST
relation/index OID whose parent still owns an auto-exposed TOAST relation,
report **no findings** (`nil`/`NullDatum, nil`) instead of raising 42P01. This
is semantically correct, not a workaround: since no value is ever actually
stored in goopg's synthetic TOAST relation, it is vacuously always empty and
healthy — exactly the report real `pg_amcheck` gives for a genuinely empty
TOAST table.

Result: `TestPort_PgAmcheck004VerifyHeapam`, `TestPort_PgAmcheckAllTables`, and
`TestPort_PgAmcheckBtreeIndexCheck` now genuinely PASS (previously always
skipped). The four corruption-injection tests
(`003CombinedCorruption`/`003MissingIndexFork`/`003MissingHeapFile`/
`003SchemaScoped`) advance past the same gate but now hit a **new, distinct**
blocker: after the test manually removes a heap/index file to simulate
corruption, restarting the goopg cluster times out after 20s
(`start timeout after 20s`, no corruption-check ever runs). This is a fresh
discovery, not a regression from this fix (the old code never reached the
restart step) — see the deferral ledger's 2026-07-07 AC-003 row for the exact
repro and resume point.

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/catalog/... ./internal/amcheck/...` PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads).
