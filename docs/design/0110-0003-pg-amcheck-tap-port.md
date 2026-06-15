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
