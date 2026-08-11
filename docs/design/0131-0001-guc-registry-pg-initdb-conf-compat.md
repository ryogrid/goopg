# GUC registry accepts a PG-18-initdb `postgresql.conf` — unblocks the reverse cold start before it reaches a catalog page

**Status:** accepted — landed 2026-08-11
**Date:** 2026-08-11
**Milestone:** M0131 (S1)

## Problem

`goopg start -D <dir>` auto-discovers `<dir>/postgresql.conf` when no explicit
`-config` is given (`cmd/goopg/main.go:360-365`, mirroring pg_ctl), parses it,
and hands the entries to `Registry.ApplyConfigEntries`
(`internal/config/guc.go:563-579`). That function aggregates every unrecognised
parameter into a single error, and `main.go:406-415` turns any error into
`return 1` — a hard exit before `initdb.Open` allocates a buffer pool, let alone
reads `base/5/1259`.

A stock PG 18.3 initdb writes eight settings goopg has never registered. Verified
against the reference cluster's conf
(`bench/tpch/runtime/pgdata/postgresql.conf`, written by real PG 18.3 initdb):

```
155: dynamic_shared_memory_type = posix
645: log_timezone = 'Asia/Tokyo'
687: autovacuum_worker_slots = 16
797: lc_messages = C
799: lc_monetary = C
800: lc_numeric = C
801: lc_time = C
807: default_text_search_config = 'pg_catalog.english'
```

All eight are absent from `internal/config/defaults.go` and from
`internal/config/postgresql.conf.sample` (grepped 2026-08-11, zero hits for each
name anywhere under `internal/config/`). So the very first thing that fails in
M0131 Theme A is not a catalog-format problem at all: goopg refuses to start.
`autovacuum_worker_slots` is PG-18-new (`guc_tables.c:3598`), so this is not a
gap that would have shown up on PG 17.

## Design

### Per-GUC disposition

PG 18 `BootVal`s below are the boot values in
`postgres/src/backend/utils/misc/guc_tables.c`, cross-checked against
`postgres/src/backend/utils/misc/postgresql.conf.sample`. Per the repo's GUC
discipline, the registered `BootVal` is **PG's default and never a goopg-tuned
value** — a tuned constant belongs in the call site, not in `BootVal`.

| GUC | PG 18 BootVal | Disposition | Rationale |
|---|---|---|---|
| `dynamic_shared_memory_type` | `posix` (`DEFAULT_DYNAMIC_SHARED_MEMORY_TYPE`, `postgres/src/include/storage/dsm_impl.h:32`) | accepted stub | goopg is a single process with no DSM segments; the value can only be recorded. |
| `log_timezone` | `GMT` (`guc_tables.c:4321-4327`) | accepted stub | goopg has a `TimeZone` GUC (`defaults.go:61`) but no separate log-side zone and no zone-aware log formatter; wiring it is a real feature, not a start-up unblock. |
| `autovacuum_worker_slots` | `16` (`guc_tables.c:3598-3604`, `PGC_POSTMASTER`) | accepted stub | goopg has no autovacuum worker pool to size. PG18-new. |
| `lc_messages` | `""` (`guc_tables.c:4435-4441`, `PGC_SUSET`) | accepted stub | goopg emits English messages only; no message catalogs exist to select. |
| `lc_monetary` | `C` (`guc_tables.c:4445-4451`) | accepted stub | `money` formatting is not locale-driven in goopg. |
| `lc_numeric` | `C` (`guc_tables.c:4455-4461`) | accepted stub | numeric output is C-locale by construction (`PGFloatOut` and the numeric codec are locale-free). |
| `lc_time` | `C` (`guc_tables.c:4465-4471`) | accepted stub | `internal/pgdatetime` formats C-locale month/day names unconditionally. |
| `default_text_search_config` | `pg_catalog.simple` (`guc_tables.c:4811-4817`) | accepted stub | goopg has no text-search configurations on disk; a non-default value must be recorded and ignored, not rejected. |

All eight are stubs. None changes engine behaviour; each is a *parsed, stored,
not acted on* variable. Stubbing a GUC is a behavioural no-op that must be
ledgered, exactly as the pg_dump GUCs were.

### The existing stub pattern

The mechanism is plain registration — there is no dedicated "stub" flag. The
pg_dump/pg_restore object-creation GUCs at `internal/config/defaults.go:117-140`
are the template: a block comment naming the client that emits the SET, the
upstream `guc_tables.c` group the names come from, an explicit statement that a
SET to the default is a true no-op and a non-default value is accepted and
ignored, and a ledger reference (M0122-0007); then an ordinary
`r.MustRegister(NewVariable(Variable{…}))` per name. Nothing marks them inert
except the comment, so the comment is load-bearing.

goopg change:
- `internal/config/defaults.go`: one commented block registering the eight names
  in `BuildDefaultRegistry`, contexts mirroring upstream —
  `dynamic_shared_memory_type` and `autovacuum_worker_slots` `ContextPostmaster`;
  `log_timezone` `ContextSigHup`; `lc_messages` `ContextSuset`; the remaining four
  `ContextUserset` (`internal/config/guc.go:31-38`). `dynamic_shared_memory_type`
  is `TypeEnum` upstream; registering it as `TypeString` would accept values PG
  rejects — use `TypeEnum` with upstream's `dynamic_shared_memory_options`.

### Sample-template coupling (mandatory)

`TestSampleConfigCoversRegistry` (`internal/config/sample_test.go:56-85`)
enforces three invariants: (1) every registered variable whose
`Flags&FlagDisallowInFile == 0` has an entry in
`internal/config/postgresql.conf.sample`; (2) no template entry names an
unregistered GUC; (3) the template's value equals the registry `BootVal`, so
uncommenting the shipped file is functionally a no-op. `FlagDisallowInFile`
(`internal/config/guc.go:107`) is the only exemption, and it is reserved for
`ContextInternal` reportables (`defaults.go:21-98`) — none of the eight qualify.

goopg change:
- `internal/config/postgresql.conf.sample`: eight commented entries whose values
  are the PG 18 `BootVal`s above, placed in the sections upstream uses —
  `RESOURCE USAGE (except WAL)` → `- Memory -` for `dynamic_shared_memory_type`;
  `REPORTING AND LOGGING` → `- What To Log -` for `log_timezone`; `AUTOVACUUM`
  for `autovacuum_worker_slots`; `CLIENT CONNECTION DEFAULTS` →
  `- Locale and Formatting -` for the four `lc_*` and
  `default_text_search_config`. Quote the string values (`''`, `'C'`,
  `'GMT'`, `'pg_catalog.simple'`) — `parseSampleEntries` strips one enclosing
  pair before comparing to `BootVal` (`sample_test.go:36-38`).

### S1.4 — the self-inflicted sibling, wider than filed

goopg's own `initdb` seeds `postgresql.conf` through
`seedPostgresqlConf` (`internal/initdb/config_seed.go:32-83`), whose
`replaceGUCValue` (`:99`) rewrites a matching commented line **or appends a new
one** when the template has no such line. Because none of these names is in the
template, every one of them is appended. Four distinct `goopg init` flag paths
therefore write a parameter that `goopg start` then rejects:

- `--locale` / `--lc-messages` / `--lc-monetary` / `--lc-numeric` / `--lc-time`:
  `localeGUCSettings` (`internal/initdb/locale.go:235-245`) emits all four `lc_*`
  names whenever any locale option was specified (`ls.specified`, `locale.go:223`).
- `-T` / `--text-search-config`: `config_seed.go:56` writes
  `default_text_search_config = 'pg_catalog.<cfg>'`.
- `--pwfile` with an md5/scram auth method: `config_seed.go:63` writes
  `password_encryption` — **also unregistered** (zero hits under
  `internal/config/`).
- `-g` / `--allow-group-access`: `config_seed.go:71` writes `log_file_mode` —
  **also unregistered**.

The last two extend the plan's S1.4 finding by two names and are the correction
this doc carries: the failure class is "initdb writes a GUC the registry does not
know", and it has six members, not four. Confirm each with a throwaway
`goopg init` + `goopg start` before fixing; if any path does not reproduce, say
so rather than assuming. `password_encryption` and `log_file_mode` are in scope
for the same registration block (both are real PG GUCs with PG BootVals
`scram-sha-256` and `0640` respectively — verify against `guc_tables.c` before
registering), or they are ledgered.

Adjacent observation, **not** in scope: goopg's `TimeZone` BootVal is `UTC`
(`defaults.go:61`, sample `:319`) where PG 18's is `GMT` (`guc_tables.c:4629`).
That is a GUC-discipline divergence predating this slice; ledger it, do not fix
it here.

## Guards

1. `internal/config` unit tests pass, including `TestSampleConfigCoversRegistry`
   with the eight (or ten) new registrations.
2. A new test parses a captured real-PG-18.3 `postgresql.conf` (the eight lines
   above are sufficient as a fixture) through `ParseConfigFile` +
   `ApplyConfigEntries` and asserts **zero** errors. It must fail before the
   registration lands.
3. Round-trip: `goopg init --lc-messages=C -T english -g -D <tmp>` followed by
   `goopg start -D <tmp>` starts (currently expected to exit 1).
4. Each stubbed GUC's `SHOW <name>` returns the value the conf file set, proving
   "parsed and stored", not "silently dropped".
5. No engine behaviour changes: SPOT and DS05 are unaffected by construction
   (no code reads the new variables).
6. UNITS + SMOKE green.

## References

- `cmd/goopg/main.go:360-365` — conf auto-discovery; `:406-415` — hard exit
- `internal/config/guc.go:563-579` — `ApplyConfigEntries`; `:107` — `FlagDisallowInFile`; `:31-38` — `Context` enum
- `internal/config/defaults.go:117-140` — pg_dump accepted-stub pattern (M0122-0007); `:61` — `TimeZone`
- `internal/config/sample_test.go:56-85` — `TestSampleConfigCoversRegistry`
- `internal/config/postgresql.conf.sample` — sections at `:26`, `:45`, `:240`, `:275`, `:284`, `:314`
- `internal/initdb/config_seed.go:32-83` — `seedPostgresqlConf`; `:99` — `replaceGUCValue`
- `internal/initdb/locale.go:229-245` — `localeGUCSettings`
- `postgres/src/backend/utils/misc/guc_tables.c:3598`, `:4321`, `:4435`, `:4445`, `:4455`, `:4465`, `:4629`, `:4811`, `:5247` — upstream entries
- `postgres/src/backend/utils/misc/postgresql.conf.sample:155`, `:645`, `:687`, `:797-801`, `:807` — upstream placement
- `postgres/src/include/storage/dsm_impl.h:32` — `DEFAULT_DYNAMIC_SHARED_MEMORY_TYPE`
- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S1
- memory: `guc_defaults_must_match_pg` — BootVal is PG's default, never goopg's tuned value

## Outcome (2026-08-11)

Landed as designed, with **ten** registrations, not eight: the design's S1.4
correction (`password_encryption`, `log_file_mode`) was confirmed by
inspection — both are appended by `seedPostgresqlConf` and neither was
registered — so both are in the same block. All ten sit in
`BuildDefaultRegistry` (`internal/config/defaults.go`) with a load-bearing
block comment, and all ten have commented entries in
`internal/config/postgresql.conf.sample` (`TestSampleConfigCoversRegistry`
green).

Corrections to the design's table, all verified against
`postgres/src/backend/utils/misc/guc_tables.c`:

- `log_file_mode` BootVal is **`0600`**, not `0640` (`guc_tables.c:2554`);
  `0640` is what *initdb* writes under `--allow-group-access`, not the GUC
  default. Context is `PGC_SIGHUP`.
- `dynamic_shared_memory_type`'s enum options are the Linux build's set —
  `posix | sysv | mmap` (`dsm_impl.c:95-109`, the `windows` entry is
  `#ifdef USE_DSM_WINDOWS`).
- `password_encryption` is `PGC_USERSET`, enum `md5 | scram-sha-256`, BootVal
  `scram-sha-256` (`guc_tables.c:5362-5367`).

`log_file_mode` is registered as `TypeString` with a `checkFileMode`
`CheckFn` rather than `TypeInt`. Upstream parses it with `strtol(…, 0)` —
base 0, so a leading `0` is octal — and *re-renders* it in octal through
`show_log_file_mode`. goopg's `parseIntWithUnit` is base-10 only and has no
octal display hook, so a `TypeInt` registration would answer `SHOW
log_file_mode` with `640` where PG says `0640`. Keeping the literal text and
validating it with the same base-0 parse plus upstream's `0000`-`0777` range
reproduces PG's accept/reject set exactly and PG's *display* for every
octal-spelled value — the only spelling initdb or the shipped template ever
emits. The residual divergence (a decimal-spelled input is echoed rather than
re-rendered as octal) is ledgered.

### Verification

- `TestApplyPGInitdbConfEntries` (`internal/config/pg_initdb_conf_test.go`) —
  the eight real-PG-18.3 initdb lines, captured verbatim from
  `bench/tpch/runtime/pgdata/postgresql.conf`, apply with zero errors.
- `TestApplyGoopgInitdbConfEntries` — the seven names `goopg init` appends
  apply with zero errors.
- `TestInitdbGUCsAreStoredNotDropped` — Guard 4: each value survives into
  `Registry.Get`, i.e. `SHOW` reports what the conf set.
- `TestInitdbGUCsRejectBadValues` — the stubs still enforce upstream's enum
  and range; they did not degrade into accept-anything strings.
- `TestLogFileModeKeepsOctalSpelling` — pins the divergence above.
- Guard 3 end-to-end: `goopg init --lc-messages=C -T english -g -D /tmp/s1data`
  then `goopg start` **starts** (it exited 1 before this slice), and `SHOW`
  returns `lc_time=C`, `log_file_mode=0640`,
  `default_text_search_config=pg_catalog.english`, `lc_messages=C`.
  Note the sample entries now exist, so `replaceGUCValue` rewrites the
  commented template lines *in place* instead of appending — the same shape
  upstream `initdb` produces.

### Discovery for the rest of M0131 (ledgered, not fixed here)

Applying the **whole** reference-cluster conf still fails on three names —
`unix_socket_directories`, `maintenance_work_mem`, `wal_compression`
(`bench/tpch/runtime/pgdata/postgresql.conf:891`, `:895`, `:899`). All three
live under that file's `CUSTOMIZED OPTIONS` header, i.e. they were appended
by `bench/tpch/setup_pg.sh`, **not** written by `initdb` — so S1's scope
(the initdb-authored set) is genuinely discharged. But it shows the failure
class is broader than initdb output: any hand-tuned or tool-tuned PG cluster
carries ordinary PG GUCs goopg has never registered, and each one is still a
hard exit 1. M0131-S3's E2E uses a pristine `initdb` directory and will not
see this; a real-world directory would. Resume point in the ledger.
