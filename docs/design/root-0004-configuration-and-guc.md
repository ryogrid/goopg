# 0004 — Configuration and GUC System

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 4 in `.ralph/fix_plan.md` is the configuration story:

1. A `postgresql.conf`-style parser (key=value, comments, includes).
2. A GUC registry typed by upstream's `guc_tables.c` shape.
3. `SHOW`, `SET`, `SET LOCAL`, `pg_settings`, `current_setting()`,
   `set_config()` integration.

Two existing pieces of code already need a real GUC registry:

- `internal/server.parameterStatusBlock` hardcodes 13 reported settings
  (`server_version`, `server_encoding`, `client_encoding`, `DateStyle`,
  `TimeZone`, `integer_datetimes`, `standard_conforming_strings`, etc.).
  Once the registry exists, that list becomes "every variable flagged
  `Report`", not a typed-out string-pair list.
- `cmd/goopg start` carries one ad-hoc string flag for the listen
  address. The right surface is one `--config postgresql.conf` flag
  whose contents seed the registry, with `--listen` etc. overriding.

This doc records the design that takes us from "the server has zero
configuration knobs" to "every later milestone can declare its own
GUCs without touching the parser or the SHOW/SET wiring".

References into upstream:

- `postgres/src/backend/utils/misc/guc.c` — main GUC machinery.
- `postgres/src/backend/utils/misc/guc_tables.c` — the canonical GUC
  table; entries we copy values from cite this file's line numbers.
- `postgres/src/include/utils/guc.h` — `GucContext`, `GucSource`,
  `GUC_UNIT_*`, `GUC_REPORT` constants.
- `postgres/src/backend/utils/misc/guc-file.l` — the lexer for
  `postgresql.conf`. We hand-roll a Go equivalent rather than porting
  the flex grammar.
- `postgres/src/backend/utils/misc/postgresql.conf.sample` — the file
  format reference.

## Decision

### Scope of this loop

Land the parser, registry, server integration, and the SQL surface
that the existing simple-query path can reach (SHOW, SET, SET LOCAL,
RESET, current_setting, set_config). `pg_settings` as a real catalog
view is deferred to milestone 5 (catalog work) — until then,
`SHOW ALL` returns the registry contents and that is the operator's
inspection tool.

Do **not** port every upstream GUC. Seed the registry with only the
variables we already use or report. New milestones add their own GUCs
when they need them. The `Register` API is the only contract that
should stay stable.

### Postgresql.conf parser

`internal/config.ParseConfigFile(path)` returns a `[]ConfigEntry` —
ordered name/value pairs with source-file context. Syntax:

- `name = value` (the `=` is optional, matching upstream).
- `name 'value'` and `name "value"` for quoted values; quoting is
  required for values containing whitespace, `#`, or `=`.
- `'value'` may contain doubled-quote escapes (`''` → `'`).
- `#` introduces a comment to end of line, except inside quotes.
- `include FILE`, `include_if_exists FILE`, `include_dir DIR` (the
  same surface as `pg_hba.conf`; we share the include-cycle detection
  pattern from `internal/auth/parser.go`).
- Unit suffixes are honored at *registry-set* time, not parse time.
  The parser keeps the raw string; the registry interprets it
  according to the variable's declared unit.

Whitespace handling matches upstream: trim leading/trailing whitespace
on the name and the value, but preserve whitespace inside quoted
values verbatim.

### GUC type model

Mirrors `guc.c`'s five flavours, restricted to what we'll actually
use this milestone:

```go
type Type int

const (
    TypeBool Type = iota
    TypeInt
    TypeReal
    TypeString
    TypeEnum
)
```

Plus a `Unit` enum covering `KB`, `MB`, `GB`, `TB`, `Bytes`, `Ms`,
`S`, `Min`, `H`, `D` — the union of upstream's `GUC_UNIT_*` flags
(`postgres/src/include/utils/guc.h:228`).

A `Variable` carries:

- `Name`, `Type`, `Unit`.
- `BootVal` (the compiled-in default; immutable string form).
- `MinVal`, `MaxVal`, `EnumOptions` (when applicable).
- `Context`: where the variable can be set
  (`Internal`, `Postmaster`, `SigHup`, `SuBackend`, `Backend`,
  `Suset`, `Userset`). Mirrors `GucContext` in `guc.h:71-80`.
- `Scope`: `Server | Database | Role | Session | Transaction`. Where
  a value can live; `SET` sets the session scope, `SET LOCAL` the
  transaction scope. Server/database/role scopes are stored in the
  registry but not yet writable from SQL — that needs catalog work.
- `Flags`: `Report` (auto-emit ParameterStatus on change),
  `NotInSample`, `DisallowInFile`, `Custom` (for user-defined GUCs).
- `Source`: where the *current* value came from (`Default`,
  `ConfigFile`, `EnvVar`, `Override`, `Session`, `Transaction`).

`Variable.Set(value, source)` is the gate every value goes through.
It validates the value against the type and bounds, normalises the
representation (e.g. `"on"` / `"true"` / `"yes"` / `"1"` → `true`),
and records the source.

### Registry

`Registry` is the per-server table. It is not goroutine-safe by
itself; concurrent access happens through per-connection
`SessionRegistry`s (see below). The startup sequence:

1. `BuildDefaultRegistry()` creates a `Registry` populated with the
   variables goopg currently advertises (`server_version`,
   `server_encoding`, `client_encoding`, `DateStyle`, `IntervalStyle`,
   `TimeZone`, `integer_datetimes`, `standard_conforming_strings`,
   `is_superuser`, `session_authorization`, `in_hot_standby`,
   `default_transaction_read_only`, `application_name`,
   `default_transaction_isolation`, `listen_addresses`, `port`,
   `max_connections`, `scram_iterations`, `shared_buffers`,
   `checkpoint_timeout`, `checkpoint_completion_target`,
   `max_wal_size`, `min_wal_size`, `full_page_writes`), plus seven
   compatibility GUCs that JDBC / pgAdmin / DBeaver / HammerDB /
   psql / pgbench issue with `SET` before running their
   workloads:
   `max_parallel_workers_per_gather`, `client_min_messages`,
   `statement_timeout`, `work_mem`, `random_page_cost`,
   `effective_cache_size`, `search_path`, `transaction_isolation`,
   `lock_timeout`, `idle_in_transaction_session_timeout`,
   `log_statement`, `log_min_duration_statement`,
   `default_statistics_target`, plus the eleven planner toggles
   `enable_seqscan` / `enable_indexscan` / `enable_indexonlyscan` /
   `enable_bitmapscan` / `enable_hashjoin` / `enable_mergejoin` /
   `enable_nestloop` / `enable_sort` / `enable_hashagg` /
   `enable_material` / `enable_partition_pruning`. v0 doesn't
   honour any of these compatibility GUCs semantically — the
   planner / executor ignore the values — but registering them
   as `ContextUserset` (or `ContextSuset` for log_*) lets the
   SET succeed instead of failing with
   `unrecognized configuration parameter`. Names, units,
   ranges, and defaults mirror upstream's
   `postgres/src/backend/utils/misc/guc_tables.c` entries.
2. If a postgresql.conf path was supplied, `ApplyConfigFile(path)`
   walks the parsed entries and calls `Set(name, value,
   SourceConfigFile)` on each. Unknown names emit a warning and are
   stored in a "custom" bucket so a future milestone reload can
   re-resolve them when the package that owns them registers.
3. Any CLI overrides (`--listen`, etc.) call `Set(name, value,
   SourceOverride)` last, taking precedence over the file.

`Registry` exposes `Get(name) (*Variable, bool)`,
`Set(name, value, source)`, `Reset(name)`, and `All()` (sorted by
name, used by `SHOW ALL`).

### SessionRegistry

Each connection gets a `SessionRegistry` that wraps the global
`Registry`:

- Reads fall through to the global registry by default.
- `SET name = value` writes to the session layer; reads see the
  session value.
- `SET LOCAL name = value` writes to the transaction layer; reads
  see the transaction value first, then session, then global. The
  transaction layer is dropped on `COMMIT` / `ROLLBACK`.
- `RESET name` drops the session value (and the transaction value if
  in one).
- `SHOW name` returns the layered value; `current_setting(name)` is
  the SQL-callable equivalent.
- `set_config(name, value, is_local)` is the SQL-callable mutator;
  `is_local=true` is `SET LOCAL`, `false` is `SET`.

A variable flagged `Report` calls back to the connection writer with
a `ParameterStatus` message whenever its effective value changes —
this is the contract clients like libpq rely on for auto-tracked
settings such as `client_encoding` and `application_name`.

### Server integration

`server.Config` grows `Registry *config.Registry`. The default
(nil) constructs `BuildDefaultRegistry()` at `Server` construction
time so existing tests don't need updating. `cmd/goopg start --config
<path>` calls `config.ParseConfigFile` and applies the result.

`parameterStatusBlock` is replaced by a single call:

```go
for _, v := range sess.ReportableVariables() {
    w.WriteParameterStatus(v.Name, v.Display())
}
```

`server.handleQuery` learns three new statement shapes alongside
`SELECT 1`:

- `SET name [TO|=] value` (and `SET LOCAL ...`)
- `RESET name` / `RESET ALL`
- `SHOW name` / `SHOW ALL`

These are recognised by a small case-insensitive prefix match (the
same temporary-shim style as the existing `SELECT 1` matcher) and
delegated to the session registry. They produce real
RowDescription/DataRow output for `SHOW`, and an empty `SET` /
`RESET` / `SHOW` command tag for the mutators.

The full SQL grammar arrives in milestone 6; this loop does the
minimum that lets a libpq client run `SHOW server_version` and
`SET application_name = 'myapp'` and see the corresponding
ParameterStatus message, which is what `psql \conninfo` and
similar code paths exercise.

### What this doc does NOT cover

- `pg_settings` as a real catalog view. `SHOW ALL` covers the
  inspection use case until milestone 5 lands the catalog.
- The full upstream GUC table. Variables are added on demand. The
  registry's `Custom` flag exists so packages can register their own
  GUCs at init time without changing this doc.
- `ALTER SYSTEM`, `ALTER DATABASE ... SET`, `ALTER ROLE ... SET`.
  Those need the catalog. Recorded as milestone 6+ work.
- Per-database / per-role scope persistence. Variables can carry the
  scope but the values aren't loaded yet (no catalog).
- Hot reload landed 2026-07-08 (M0122-0007) — see the "Hot reload
  (2026-07-08)" section below.

## Alternatives Considered

- **Use a third-party config library (`pelletier/go-toml`,
  `koanf`, `viper`).** Rejected: the file format is `postgresql.conf`,
  not TOML/YAML. We'd be writing a translator anyway, and inheriting
  someone else's parser quirks.
- **Skip the GUC machinery; let each subsystem own its own settings.**
  Rejected: the wire protocol *requires* a registry — clients call
  `SHOW`, `SET`, `current_setting()`, and inspect `pg_settings`. A
  pile of subsystem-local globals would have to be glued together
  through these APIs anyway.
- **Generate the registry from upstream's `guc_tables.c` like we did
  for `errcodes.txt`.** Rejected for now: the table mixes data with
  C type references and per-variable hooks. Adding the variables we
  actually need by hand keeps the seam between goopg's defaults and
  upstream's defaults explicit. Revisit if the hand-written list ever
  exceeds ~100 variables.

## Consequences

- The hardcoded ParameterStatus block in `internal/server` becomes a
  registry walk, so adding a new reportable GUC is a one-line change
  rather than two coordinated edits.
- Future milestones (storage, WAL, executor) declare their tunables
  by registering them at package-init or wired through
  `BuildDefaultRegistry`. They don't need to touch the parser, the
  SHOW/SET wiring, or the ParameterStatus emission.
- The `Source` provenance on each variable will be load-bearing later
  for reload-from-SIGHUP semantics: we replace ConfigFile-sourced
  values, leave Session/Override/EnvVar values alone — exactly
  upstream's policy.

## Hot reload (2026-07-08)

M0122-0007's "SIGHUP config reload" item: `startControlPlane`'s
`OnReload` handler (`internal/server/server.go`) was a "v0 no-op" that
only logged a line — `goopg reload` and a literal `kill -HUP
<postmaster-pid>` had no effect on a running server. Both are now
real, and agree with each other, since both call the same
`Server.reloadConfig`:

- The control-socket path is unchanged in shape — `cl.OnReload` still
  answers `RELOAD` — but now calls `reloadConfig` instead of just
  logging.
- A new goroutine in `startControlPlane` calls `signal.Notify` on
  `syscall.SIGHUP` and calls the same `reloadConfig` on receipt,
  matching upstream's two reload triggers (`pg_ctl reload` itself just
  sends SIGHUP to the postmaster; goopg's control socket is a second,
  parallel path since goopg has no postmaster/backend process split).

`Server.reloadConfig` re-reads `cfg.ConfigPath` (the file
`cfg.Registry` was originally loaded from at boot — plumbed through
the new `Config.ConfigPath` field, set by `cmd/goopg` next to
`cfg.Registry`) via the existing `config.ParseConfigFile`, then calls
the new `Registry.ApplyReloadEntries` (`internal/config/guc.go`) —
deliberately a separate entry point from boot's `ApplyConfigEntries`,
because a *running* server's reload must split by `Context` the way
upstream's `ProcessConfigFile` (`guc-file.l`) does, where boot may
not:

- `ContextPostmaster` / `ContextInternal` entries are left untouched
  and reported as a warning (`"... cannot be changed without
  restarting the server"`) — applying them live would silently lie
  about the server's actual behavior (e.g. `shared_buffers`, already
  sized into a fixed-length buffer pool at boot).
- Every other context (`ContextSigHup` primarily, but also
  `ContextSuBackend`/`ContextBackend`/`ContextSuset`/`ContextUserset`
  values not already overridden by a session `SET`) is applied with
  `SourceConfigFile`, same as boot.
- Unlike boot, each applied change also fires `Registry`'s `OnChange`
  bridge (`invokeOnChange`) — boot's `setFromFile` bypasses it because
  nothing has read the registry yet at that point, but a reload
  changes a *live* value, so process-global toggles wired via
  `Registry.OnChange` (e.g. `enable_nestloop_index` →
  `planner.SetNLIEnabled`) must observe the new value immediately, the
  same as a `SET` would.
- A reload never aborts partway or crashes the server on a bad
  entry — unknown parameters and canonicalization failures are
  reported as warnings too, matching `ProcessConfigFile`'s "log and
  keep the old values" behavior instead of boot's hard-fail-on-error
  contract (`cmd/goopg start` exits 1 on a malformed file; a running
  server must not exit on a malformed reload).

Verified live against the real `cmd/goopg` binary: started with
`checkpoint_timeout = 600` in `postgresql.conf`, `SHOW
checkpoint_timeout` confirmed `10min`; edited the file to `900` +
added `max_connections = 5`, ran `goopg reload -D <datadir>` — log
showed the `max_connections` restart-required warning and
`changed=[checkpoint_timeout]`; `SHOW checkpoint_timeout` → `15min`,
`SHOW max_connections` → unchanged `100`. Repeated with a literal
`kill -HUP <pid>` instead of `goopg reload` — same result. Tests:
`TestApplyReloadEntriesAppliesSigHupSkipsPostmaster`,
`TestApplyReloadEntriesFiresOnChange` (`internal/config/guc_test.go`),
`TestReloadConfigAppliesSigHupSkipsPostmaster`,
`TestReloadConfigNoPathIsNoop` (`internal/server/reload_test.go`).

**Still open:** a true restart-the-listener reload for
`ContextPostmaster` GUCs (e.g. re-binding on a changed `port`) is not
attempted — matching upstream, those still require a full process
restart; goopg's reload only reports them.

## Completing the `jit_*` GUC stub family (2026-07-08, M0122-0007)

M0097-0073 registered three JIT-adjacent compatibility stubs (`jit`,
`jit_above_cost`, `compute_query_id`, `plan_cache_mode`) so scripts
written against real PostgreSQL don't fail with "unrecognized
configuration parameter", but upstream's `guc_tables.c` defines nine
`jit_*` GUCs total — six were still missing:
`jit_optimize_above_cost`/`jit_inline_above_cost` (real, alongside the
existing `jit_above_cost`) and `jit_debugging_support`/
`jit_dump_bitcode`/`jit_expressions`/`jit_profiling_support`/
`jit_tuple_deforming`/`jit_provider`. All eight now registered in
`internal/config/defaults.go`, with boot values, `Type`, and
`Context` copied directly from `guc_tables.c` (`jit_debugging_support`/
`jit_profiling_support` are `PGC_SU_BACKEND` → `ContextSuBackend`;
`jit_dump_bitcode` is `PGC_SUSET` → `ContextSuset`; `jit_provider` is
`PGC_POSTMASTER` → `ContextPostmaster`; the rest are `PGC_USERSET` →
`ContextUserset`). goopg has no JIT compiler at all — not even a
consulted-but-inert code path — so, like their three siblings, these
remain pure enumeration/SET-acceptance stubs with no runtime effect.
Choosing `ContextSuBackend` for the two `SU_BACKEND` GUCs is
functionally meaningful despite the "just a stub" framing:
`SessionRegistry.Set` already rejects any `Context < ContextSuset`
with `"parameter ... cannot be changed now"`, so `SET
jit_debugging_support = on` correctly fails the same way it does
against real PostgreSQL (`SU_BACKEND` GUCs are only settable at
backend start, never via `SET`), without any new enforcement code.
`postgresql.conf.sample` gained matching commented-out entries (this
codebase's `TestSampleConfigCoversRegistry` requires every non-
`FlagDisallowInFile` registry GUC to appear there, unlike upstream's
own sample file which omits `GUC_NOT_IN_SAMPLE` entries — a
deliberate goopg-local stricter convention, not a bug in the test).
Tests: `TestJitGUCFamilyStubs` (table-driven over all 8, asserting
boot value/type/context and `SET`-acceptance matching real PG's
context rules), `TestJitCostGUCsAcceptNegativeOneSentinel` (the two
new cost GUCs accept upstream's `-1`-disables sentinel and reject
values below it), both `internal/config/jit_guc_stubs_test.go`
(confirmed non-vacuous via `git stash` on `defaults.go` +
`postgresql.conf.sample`). Gates: `go build ./...`/`go vet ./...`
clean; `go test ./internal/config/...` PASS;
`go test ./internal/executor/... ./internal/initdb/...` PASS (minus
the pre-existing, unrelated `TestSeqScanFiresPrefetchesAcrossBlocks`
hang tracked in the deferral ledger); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed txns, all 3
workloads).

## Follow-up: GEQO + remaining planner-tuning GUC stubs (2026-07-12, M0122-0007)

Registered the last commonly-issued planner-tuning GUCs that were still
missing from the registry, so a script written against real PostgreSQL
never trips `unrecognized configuration parameter` on them:

- **GEQO family (`QUERY_TUNING_GEQO`)** — `geqo`, `geqo_threshold`,
  `geqo_effort`, `geqo_pool_size`, `geqo_generations`,
  `geqo_selection_bias`, `geqo_seed`.
- **`QUERY_TUNING_OTHER`** — `constraint_exclusion` (enum
  `{partition,on,off}`, default `partition`), `cursor_tuple_fraction`
  (real, 0.1), `recursive_worktable_factor` (real, 10).

All ten are `PGC_USERSET` / `GUC_EXPLAIN` in upstream. Names, boot
values, types, and numeric bounds mirror
`postgres/src/backend/utils/misc/guc_tables.c` (GEQO bounds from
`optimizer/geqo.h`, `cursor_tuple_fraction` from `planmain.h`,
`recursive_worktable_factor` from `cost.h`). Boot values use the form
real-PG `SHOW` returns (`geqo_selection_bias` → `2`,
`recursive_worktable_factor` → `10`, `geqo_seed` → `0`), which the
matching `postgresql.conf.sample` entries also use so
`TestSampleConfigCoversRegistry` (sample default must equal registry
`BootVal`) is satisfied.

These are **accepted-and-ignored** stubs: goopg's planner is
rule/cost-based and reads none of them, so `SET`/`SHOW` succeed but the
chosen plan is unchanged — identical to the pre-existing `enable_*`
toggles. The behavioral no-op (real PG's planner *does* honour these) is
recorded in the deferral ledger as a deliberate scope boundary.

### `pg_settings` surfacing (2026-07-12, M0122-0007 follow-up)

The initial GEQO landing deferred one half: `pg_catalog.pg_settings` is a
*separately* hand-curated literal list in
`internal/catalog/catalog.go` (`pgSettings.VirtualRows`), **not** derived
from the config registry, so `SHOW geqo_threshold` worked while
`SELECT * FROM pg_settings WHERE name = 'geqo_threshold'` returned nothing.
A real-PG-authored monitoring/ORM query reading `pg_settings` would have
missed every one. This follow-up adds all ten rows (seven
`QUERY_TUNING_GEQO` + three `QUERY_TUNING_OTHER`) to that list, with
`name`/`setting`/`category`/`vartype`/`min_val`/`max_val`/`enumvals`/
`boot_val` byte-for-byte from
`postgres/src/backend/utils/misc/guc_tables.c` (category strings
`Query Tuning / Genetic Query Optimizer` and
`Query Tuning / Other Planner Options`, `short_desc`/`extra_desc` from the
same `gettext_noop` literals; `constraint_exclusion`'s enumvals render
`{partition,on,off}`). The list is re-sorted by name after append, so the
existing sysviews name-sort contract holds. Remaining deferral: the
*behavioral* no-op (the planner still ignores every value) is unchanged
and the `pg_settings` list is still not registry-derived (only the GUCs a
regress/tooling query needs are hand-added).

Tests: `TestGeqoAndPlannerTuningGUCStubs` (boot value/type/USERSET
context/`SET`-acceptance for all ten) and `TestGeqoTuningGUCBoundsEnforced`
(out-of-range/invalid-enum `SET` rejected, in-range accepted), both
`internal/config/geqo_guc_stubs_test.go`; plus
`TestPgSettingsPlannerTuningGUCs`
(`internal/catalog/catalog_test.go`) pinning the ten new `pg_settings`
rows' `setting`/`category`/`vartype`/`boot_val`/`source`/enumvals. Gates:
`go build ./...`/`go vet ./internal/config/... ./internal/catalog/...`
clean; `go test ./internal/config/... ./internal/catalog/...` PASS.

## Follow-up: object-creation default GUC stubs (2026-07-12, M0122-0007)

`pg_dump`/`pg_restore` emit a fixed SET preamble before every `CREATE TABLE`
section:

```
SET default_tablespace = '';
SET default_table_access_method = heap;
```

plus `SET default_toast_compression = 'pglz';` when a column carries a
non-default compression method. None of these three GUCs
(`CLIENT_CONN_STATEMENT`, all `PGC_USERSET` in
`postgres/src/backend/utils/misc/guc_tables.c`) were registered in goopg, so
replaying a real-PG dump aborted at the first such line with
`unrecognized configuration parameter`. Registered them as accepted stubs in
`internal/config/defaults.go` and surfaced them in the hand-curated
`pg_settings` list (`internal/catalog/catalog.go`), plus
`internal/config/postgresql.conf.sample` (the `TestSampleConfigCoversRegistry`
invariant requires every registered GUC there):

- **`default_table_access_method`** — string, boot `heap`
  (`DEFAULT_TABLE_ACCESS_METHOD`, `access/tableam.h`).
- **`default_tablespace`** — string, boot `''`.
- **`default_toast_compression`** — enum `{pglz,lz4}`, boot `pglz`
  (`TOAST_PGLZ_COMPRESSION`, `access/toast_compression.h`); the `lz4` option
  matches the reference PG 18.3 `--with-lz4` build and goopg's existing
  column-level `COMPRESSION lz4` support.

**Behavioral scope (deferred, ledgered):** these are compatibility stubs.
goopg only implements the `heap` access method, has no real tablespaces, and
chooses a column's TOAST compression from its own built-in default rather than
consulting `default_toast_compression`. So a `SET` to the boot value is a true
no-op and a non-default value is accepted and ignored — the same
accepted-and-ignored contract as the `enable_*`/GEQO planner stubs. The enum
*domain* is still enforced (`SET default_toast_compression = 'zstd'` errors),
matching upstream. Verified end-to-end against the real `cmd/goopg` binary over
the wire: the three pg_dump-preamble `SET`s all succeed, `SHOW` and
`pg_settings` report them, `SET ... = 'lz4'` moves the value, and the invalid
`'zstd'` is rejected.

Tests: `TestObjectDefaultGUCStubs` + `TestObjectDefaultGUCValuesAccepted`
(`internal/config/object_default_guc_stubs_test.go`);
`TestPgSettingsObjectDefaultGUCs` (`internal/catalog/catalog_test.go`);
`TestSampleConfigCoversRegistry` (unchanged, now covers the three new names).
Gates: `go build ./...` clean; `go test ./internal/config/...
./internal/catalog/...` PASS; live server SET/SHOW/pg_settings smoke.
