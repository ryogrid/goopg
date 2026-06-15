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

## Resume point

Promote `AC-002` to `port` (and stop the skip) once the three SQL gaps above are
implemented — `002_nonesuch` then passes with no further amcheck work, since it
never calls `verify_heapam()`. **(DONE — AC-002 is now `port`; the three gaps
plus #6/#7a/#7b/#7c landed.)**

For `AC-003`: the page-structural tier of `004_verify_heapam.pl` is ported (above).
Remaining: `003_check.pl` (whole-database heap+btree orchestration; needs the
system-catalog heap pages to verify cleanly), and `005_opclass_damage.pl` (needs
`CREATE OPERATOR CLASS` + `pg_amproc` catalog parity to inject the breaking
sort-order via `UPDATE pg_amproc`). AC-003 stays `defer` until 003 and 005 land.
