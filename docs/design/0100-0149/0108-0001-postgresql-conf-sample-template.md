# 0108-0001 — `postgresql.conf.sample` template for goopg

**Status:** accepted
**Date:** 2026-05-20
**Supersedes:** none (new)
**Companion milestone:** [M0108 — postgresql.conf.sample template + sync rule](../../milestones/0108-postgresql-conf-sample-template.md)

## Problem

PostgreSQL ships `src/backend/utils/misc/postgresql.conf.sample` (842 lines as of PG 18.3) — a richly-commented template enumerating every supported GUC, grouped by category, with all settings commented out so the file is also a usable starter `postgresql.conf`. `initdb` copies this sample verbatim to `<datadir>/postgresql.conf` during cluster creation. Operators discover what knobs exist, what units apply, and what restart class each setting belongs to by reading the file directly.

goopg currently has no equivalent. `internal/initdb/initdb.go::defaultPostgresqlConf` emits a 20-line embedded string covering ~6 GUCs (`listen_addresses`, `port`, `server_encoding`, `client_encoding`, `DateStyle`, `TimeZone`); the other 70 supported GUCs registered by `internal/config/defaults.go::BuildDefaultRegistry` are invisible unless an operator reads the source. There is also no enforced link between the GUC registry and the on-disk template, so newly-added GUCs silently fail to appear in the cluster's `postgresql.conf`.

## Goal

Land a single hand-maintained `postgresql.conf.sample` file inside goopg's codebase that:

1. Documents every GUC registered by `BuildDefaultRegistry` (76 today; grows with the project).
2. Uses PostgreSQL 18's section structure (FILE LOCATIONS, CONNECTIONS AND AUTHENTICATION, RESOURCE USAGE, WRITE-AHEAD LOG, REPLICATION, QUERY TUNING, REPORTING AND LOGGING, STATISTICS, AUTOVACUUM, CLIENT CONNECTION DEFAULTS, LOCK MANAGEMENT, VERSION AND PLATFORM COMPATIBILITY, ERROR HANDLING, CONFIG FILE INCLUDES, CUSTOMIZED OPTIONS) so operators with PG experience can navigate it without re-learning.
3. Has every setting **commented out** so file-derived values are by definition equal to the registry's `BootVal` (i.e. a freshly-initted cluster is unaffected by the template's presence).
4. Carries inline unit / range / restart-class / enum-option hints per entry (matching PG's commenting style).
5. Is written into `<datadir>/postgresql.conf` by `goopg init` (replacing the current minimal embedded string).
6. Is kept in sync with the registry by a unit test that fails CI when a registered GUC is missing from the template or a template entry is unknown to the registry — backed by a written rule in `.ralph/AGENT.md` that mandates appending a commented-out entry whenever a new GUC is registered.

## Design

### Location

`internal/config/postgresql.conf.sample` — embedded via `//go:embed` from the package that owns `BuildDefaultRegistry`. Rationale:

- Co-located with the source of truth for GUC metadata (the registry).
- Embedded into the goopg binary — no separate `share/` install step needed (mirrors goopg's single-binary distribution model).
- Discoverable by anyone touching the GUC registry: the file sits next to `defaults.go` in the same directory.

The package exports a `SampleConfig() []byte` accessor that returns the embedded bytes verbatim. `initdb` calls this accessor when materialising `<datadir>/postgresql.conf`.

### File format

Mirror upstream PG 18.3's `postgresql.conf.sample` (842 lines) structurally but list only goopg-supported GUCs. Concrete shape per entry:

```
# - <Subsection> -

#<guc_name> = <BootVal>            # <unit-or-range-hint> (<restart-class>)
                                   # <one-line short description>
```

Examples (illustrative — concrete bytes are produced by the implementing loop):

```
# - Memory -

#shared_buffers = 128MB                  # min 128kB
                                         # (change requires restart)
#work_mem = 4MB                          # min 64kB
                                         # (sets the per-operation working
                                         # memory limit)
#maintenance_work_mem = 64MB             # min 1MB
                                         # (used by VACUUM, CREATE INDEX)

# - Asynchronous Behavior -

#effective_io_concurrency = 1            # 1-1000; 0 disables prefetching
#max_worker_processes = 8                # (change requires restart)
```

Style rules:

- One blank line between entries (matches PG).
- Section banners follow PG's `# <SECTION NAME>` + `#--------------------` style.
- Restart class annotated as `(change requires restart)` when `Context = ContextPostmaster`, `(SIGHUP / reload to apply)` when `ContextSighup`, no annotation for `ContextUserset` / `ContextSuset` (matching PG).
- Units use the registry's `Unit` field (B, kB, MB, GB, TB, ms, s, min, h, d).
- Enum-typed GUCs list valid values inline: `# <name> = <BootVal>  # <enumOpt1> | <enumOpt2> | …`.

### Initdb wiring

`internal/initdb/initdb.go::SampleFiles()` currently returns a `FileSpec{Path: "postgresql.conf", Build: defaultPostgresqlConf, Mode: 0o600}` entry. Change `Build` to a thin shim that calls `config.SampleConfig()`:

```go
{Path: "postgresql.conf", Build: func() []byte { return config.SampleConfig() }, Mode: 0o600},
```

Delete `defaultPostgresqlConf` and its 20-line embedded string from `initdb.go`. The new template is the single source of truth for what `<datadir>/postgresql.conf` looks like on a fresh cluster.

`pg_hba.conf`, `pg_ident.conf`, and `postgresql.auto.conf` are unaffected — they have separate concerns and stay on their current embedded strings.

### Registry ↔ template sync test

New test under `internal/config/`:

```go
func TestSampleConfigCoversRegistry(t *testing.T) {
    reg := BuildDefaultRegistry()
    sample := SampleConfig()
    sampleEntries := parseSampleConfigGUCNames(sample) // walks every "#<name> = …" line

    // Every registered GUC has a commented-out entry in the sample.
    for _, v := range reg.AllVariables() {
        if v.HasFlag(FlagDisallowInFile) {
            continue // internal-only GUCs (e.g., server_version) are not file-settable
        }
        if !sampleEntries[v.Name] {
            t.Errorf("postgresql.conf.sample missing GUC %q (registered in defaults.go)", v.Name)
        }
    }

    // Every sample entry resolves to a known GUC.
    for name := range sampleEntries {
        if _, ok := reg.Lookup(name); !ok {
            t.Errorf("postgresql.conf.sample references unknown GUC %q", name)
        }
    }

    // Default values in the sample match the registry's BootVal so the
    // file is functionally a no-op when uncommented as-is.
    enforceBootValMatch(t, reg, sample)
}
```

The parser is intentionally simple: regex over `^#?\s*(\w+)\s*=` lines, ignoring section banners and prose. `FlagDisallowInFile` (already on the `Variable` struct) lets internal-only GUCs opt out cleanly.

### AGENT.md rule (landed at filing time)

A new section in `.ralph/AGENT.md` between "Completion and Deferral Discipline" and "Key Learnings":

```
## GUC sample-file discipline

When you register a new GUC under `internal/config/defaults.go::BuildDefaultRegistry`
(or remove an existing one), you MUST update
`internal/config/postgresql.conf.sample` in the same commit:

- Add a commented-out entry under the appropriate PG-style section,
  matching the template's existing formatting (unit/range/enum hint inline,
  restart-class annotation when applicable, default value equal to the
  GUC's BootVal so the file remains a usable no-op).
- For removals, delete the corresponding line and re-flow surrounding
  whitespace.
- The unit test `TestSampleConfigCoversRegistry` in `internal/config/`
  is the enforcement gate; it MUST pass before the commit is opened.

The sample file is the operator-facing documentation of every supported
GUC. Letting it drift from the registry is a regression on usability
and on PG operator-mental-model compatibility.
```

This rule is added in the same commit that files M0108, so the rule is in effect before any subsequent GUC is registered. Loops implementing M0108-0001 (sample-file body) and M0108-0002 (initdb wiring) are then governed by the rule going forward.

### Upstream reference policy

This is documentation, not byte-compatibility — operators read the file as a guide, not as a byte-for-byte clone of PG's. We are free to:

- Drop sections that have no goopg-supported GUCs (e.g., entire SSL sub-section if goopg doesn't implement SSL GUCs yet).
- Add a `# CUSTOMIZED OPTIONS — goopg-specific` section at the end for any GUC that has no PG analogue (rare; goopg's current 76 GUCs are all PG-compatible names).
- Use slightly different comment formatting where it improves clarity (e.g., calling out goopg-specific defaults when they differ from PG's stock defaults).

We are **not** free to:

- Document a GUC that isn't actually registered. The sync test enforces this.
- Set a different default in the sample than what the registry produces. The sync test enforces this.
- Rename a GUC to a name PG doesn't use. Doing so would break operator muscle memory and prevent reusing a PG-tuned `postgresql.conf` against goopg.

## Sub-milestone breakdown

| ID | Title | Primary deliverable |
|---|---|---|
| M0108-0001 | Initial template body + `config.SampleConfig()` accessor | `internal/config/postgresql.conf.sample` (76 commented-out entries, PG-style sections); `internal/config/sample.go` exporting `SampleConfig() []byte` via `//go:embed` |
| M0108-0002 | initdb wiring | `SampleFiles()` in `internal/initdb/initdb.go` switches `postgresql.conf` to `config.SampleConfig()`; `defaultPostgresqlConf` deleted; a regression test verifies a fresh `goopg init` produces the embedded bytes verbatim |
| M0108-0003 | Registry↔sample sync test | `TestSampleConfigCoversRegistry` in `internal/config/sample_test.go` enforcing the rule's mechanical part |

The AGENT.md rule is landed at filing time (this commit), not as a separate sub-milestone, so the rule is binding from day one.

## Verification

For each sub-milestone:

1. `go test ./internal/config/...` PASS.
2. `go test ./internal/initdb/...` PASS (must keep all M0105/M0106 byte-layout regression tests green; the postgresql.conf change is a file-content change, not an on-disk-byte-format change).
3. `gofmt -l .` empty; `go vet ./...` clean; `make ralph-state-guard` PASS.
4. Manual sanity: `go run ./cmd/goopg init /tmp/sanity-data` produces a `postgresql.conf` whose first ~30 lines start with the FILE LOCATIONS section header and contain commented-out `#listen_addresses`, `#port` lines among others.

For the milestone overall: `internal/config/postgresql.conf.sample` exists, has ≥ N commented-out entries where N matches the count of file-settable GUCs in `BuildDefaultRegistry`, the sync test passes in CI, and the `.ralph/AGENT.md` rule is in place.

## Out of scope

- Regenerating the sample from the registry programmatically. The sample is hand-maintained because per-GUC prose comments are not derivable from registry metadata. (A future loop may add a `make gen-postgresql-conf-sample-diff` helper that emits a starter line for a newly-registered GUC, but it is not required by this milestone.)
- Backfilling `pg_settings` from the registry. That view is hand-coded today (`internal/catalog/catalog.go`); aligning it with the registry is tracked separately if the user opens a future milestone for it.
- Translating PG's `postgresql.conf.sample` verbatim. We list only goopg-supported GUCs; the upstream file is reference material, not a copy target.
