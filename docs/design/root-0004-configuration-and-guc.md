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
   compatibility GUCs HammerDB / psql / pgbench issue with `SET`
   before running their workloads:
   `max_parallel_workers_per_gather`, `client_min_messages`,
   `statement_timeout`, `work_mem`, `random_page_cost`,
   `effective_cache_size`, and `search_path`. v0 doesn't honour
   any of these compatibility GUCs semantically — the planner
   and executor ignore the values — but registering them as
   `ContextUserset` lets the SET succeed instead of failing
   with `unrecognized configuration parameter`. Names, units,
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
- Hot reload (`SIGHUP` / `pg_ctl reload`). The reload command lands
  with the control socket in milestone 7. The registry already
  separates `Source` from `Value` so a future reload can replace
  ConfigFile-sourced values without touching Session-sourced ones.

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
