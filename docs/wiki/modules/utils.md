# Module: `internal/utils`

The **utility and infrastructure** packages — a collection of leaf packages that
provide foundational services used by every core module. No single package
structure; each sub-package is independent, with no imports from `internal/`
outside `utils/` itself.

## Sub-packages

### `internal/utils/misc` — GUC registry, session state, configuration

The **grand unified configuration** (GUC) engine and per-session state.

- `guc.go` (836) — `Registry`/`Variable`: the GUC registry with `Register`,
  `Get`, `Set`, `ApplyConfigEntries` (conf file), `ApplyReloadEntries` (SIGHUP).
  GUC types: `TypeBool/Int/Real/String/Enum`, contexts
  `ContextPostmaster/SigHup/Backend/Userset/…`, flags `FlagReport/DisallowInFile`.
- `defaults.go` (1,603) — `BuildDefaultRegistry`: registers every PG-18 GUC
  with its default value, context, and unit/hint. The `postgresql.conf.sample`
  template is kept in sync by `TestSampleConfigCoversRegistry`.
- `session.go` (542) — session state: `SET`/`SET LOCAL`/`RESET`, the undo
  journal for transaction rollback, `ParameterStatus` tracking.
- `parser.go` (318) — pg_hba.conf- and postgresql.conf-format parser.
- `datestyle.go` / `timestamptz_out.go` — date/time formatting/parsing helpers.
- `encoding_guc.go` — `client_encoding`/`server_encoding` GUCs + encoding-ID
  table.
- `version.go` — `server_version` / `server_version_num` GUCs.

### `internal/utils/adt/datetime` — date/time/interval formatting

The date/time/interval output formatting engine, mirroring PG's `datetime.c`,
`formatting.c`, and `pg_locale.c`.

- `pg_datetime_format.go` — `FormatDateTime` (a port of PG's `do_to_timestamp`
  / `datetime_format`): the `to_char`/`to_timestamp` template engine.
- `interval_format.go` / `interval_typmod.go` — `FormatInterval` (ISO 8601,
  PostgreSQL abbreviated, SQL standard), interval typmod decode.
- `normalize.go` — `NormalizeTimestamp`/`NormalizeInterval` (time-unit
  overflow/underflow carry).
- `timeofday.go` — `TimeOfDay` (the `now()`/`timeofday()` wall-clock).
- `era.go` / `monthname.go` — era name lookup, month/day name tables.

### `internal/utils/mb` — multi-byte encoding

Character-set encoding support: `conv.go` (encoding conversion, byte-order
mark), `wchar.go` (multi-byte character width), per-encoding files
(`euc_jp.go`, `euc_kr.go`, `latin1.go`, `latin2.go`). PG-18 encodings are
declared in `encoding_guc.go`; the `mb/` package provides the actual
conversion/width functions.

### `internal/utils/activity` — goroutine-level activity tracking

The `ActivityRegistry`: per-backend-goroutine registration, activity kind
(executing query, waiting on lock, idle in transaction), blocking-tag, and
`WaitEvent` tracking. The `pg_stat_activity` view and the lock manager's
deadlock detection both consume this. `internal/utils/activity/stats/counter.go`
provides concurrent-statistics counters.

### `internal/utils/errcodes` — SQLSTATE codes

The `codes.go` file defines every SQLSTATE code as a constant
(`SuccessFulCompletion`, `SerializationFailure`, `SyntaxError`, …) matching
`postgres/src/backend/utils/errcodes.txt`.

### `internal/utils/mmgr` — memory context

The `mctx` (memory context) allocator: `NewContext`/`NewChunk`/`Alloc`/`Free`.
A simple bump-alloc arena with a free-list fallback, used by the parser and
nodes packages for temporary allocations along the query path.

### `internal/utils/adt/similarto` — SIMILAR TO regex

The `similarto.go` package translates PG's `SIMILAR TO` pattern syntax into
a Go `regexp` for `LIKE`-style pattern matching.

### `internal/utils/adt/array` — array utility helpers

The `pgarray.go` file provides array element type name resolution / lookup
helpers used by the catalog and executor.

## Public API

There is no single entry point — each sub-package exposes its own surface:

```go
// misc (GUC registry)
func NewRegistry() *Registry
func (r *Registry) Register(name string, v *Variable) error
func (r *Registry) Get(name string) (*Variable, error)
func (r *Registry) ApplyConfigEntries(entries map[string]string) error
func BuildDefaultRegistry() *Registry

// misc (session)
func (s *Session) Set(name, value string, local bool) error
func (s *Session) Reset(name string) error

// adt/datetime
func FormatDateTime(t time.Time, format string) (string, error)
func FormatInterval(months, days, micros int64, style string) string

// activity
func (r *ActivityRegistry) Register() (*Slot, error)
func (r *ActivityRegistry) SetActivityKind(s *Slot, kind ActivityKind) error

// mmgr
func NewContext() *Context
func (c *Context) Alloc(size int) []byte
func (c *Context) Free() error
```

## Internal structure

- **`misc/guc.go`** — the `Registry` holds a map of `Variable`s, each carrying
  type, context, source, and change hooks; `ApplyConfigEntries` parses the conf
  file and rejects unknown parameters; `ApplyReloadEntries` is the SIGHUP path.
- **`misc/session.go`** — per-session GUC state with a `txPrior` undo journal so
  transaction ABORT reverts plain `SET` (not `SET LOCAL`).
- **`misc/defaults.go`** — the boot-time `BuildDefaultRegistry()` registers every
  PG-18 GUC (default value, context, unit, enum hint); the `postgresql.conf.sample`
  template mirrors it and is enforced by `TestSampleConfigCoversRegistry`.
- **`adt/datetime`** — the template engine (`pg_datetime_format.go`) is the
  largest piece; `normalize.go` carries time-unit overflow/underflow.
- **`activity`** — a registry of per-backend slots indexed by proc number; each
  slot tracks activity kind, blocking tag, and wait event for `pg_stat_activity`
  and deadlock detection.
- **`mmgr`** — a bump-alloc arena with a free-list fallback; contexts are
  cleared after each statement, reducing allocation overhead without replacing
  Go's GC.

## Dependencies

- **Used by** — every core package: `internal/executor`, `internal/optimizer`,
  `internal/parser`, `internal/storage`, `internal/access/*`, `internal/postmaster`,
  `internal/catalog`, `internal/replication`, `internal/initdb`.
- **Uses** — nothing inside `internal/` outside `utils/` itself (leaf layer
  rule); only the standard library and, for `mb`, the Go `unicode` tables.

## Notable patterns / gotchas

- **GUC registry singleton** — `BuildDefaultRegistry` is called once at startup;
  `Register` is used for custom GUCs. Do not import `internal/executor` or
  `internal/optimizer` from here (layer rule).
- **GUC undo journal** — `session.go` tracks plain `SET` (not `SET LOCAL`)
  values in a `txPrior` map so transaction ABORT reverts them; `SET LOCAL`
  is journalled separately via the `rollbackStack`.
- **`client_min_messages`** — the elevel ceiling (21 = ERROR) is enforced
  once at the single emitter, not at every call site; INFO always sends.
- **Encoding tables** — the encoding-ID table and the `encoding_guc.go` table
  must stay in sync with PG 18's `pg_enc.c`; a stale table means a
  `SET client_encoding` to a valid PG encoding silently fails.
- **`mmgr` is not GC-free** — the memory context is a simple bump-alloc arena;
  it does not replace Go's GC, it reduces allocation overhead for short-lived
  parse trees. Contexts are cleared after each statement.