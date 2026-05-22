# Milestone 0108 — `postgresql.conf.sample` Template + Registry-Sync Rule

**Status:** accepted
**Filed:** 2026-05-20
**Accepted:** 2026-05-21
**Depends on:** none (uses existing `internal/config` GUC registry and `internal/initdb` file-writer)
**Reference plan:** `.ralph/fix_plan.md` (M0108 section)

## Problem

PostgreSQL's `src/backend/utils/misc/postgresql.conf.sample` (842 lines)
is the operator's primary reference for what GUCs exist, what units they
take, what restart class each belongs to, and what the default values
are. PG's `initdb` copies that file to `<datadir>/postgresql.conf` so a
fresh cluster ships with a self-documenting, ready-to-customize config
that lists every supported knob with everything commented out.

goopg has no equivalent. Today `internal/initdb/initdb.go` (the
`defaultPostgresqlConf` function around `initdb.go:5656`) emits a ~20-line
embedded string covering 6 GUCs. The other 70 GUCs registered by
`internal/config/defaults.go::BuildDefaultRegistry` (76 total) are
invisible unless an operator reads goopg's Go source. There is also no
enforced sync between the registry and the on-disk template, so any new
GUC silently fails to appear in fresh clusters' `postgresql.conf`.

## Goal

1. Land `internal/config/postgresql.conf.sample` — a hand-maintained
   PG-style template documenting every file-settable GUC in
   `BuildDefaultRegistry`, all commented out, with PG-compatible section
   structure and inline unit / range / restart-class / enum-option hints.
2. Wire `goopg init` to write this template as `<datadir>/postgresql.conf`
   (replacing the current minimal embedded string).
3. Enforce the registry↔template sync with a unit test that fails CI
   when a registered GUC is missing from the template or vice versa.
4. Land a written rule in `.ralph/AGENT.md` requiring every commit that
   adds or removes a GUC to update `internal/config/postgresql.conf.sample`
   in the same commit. **This AGENT.md rule is landed in the milestone-filing
   commit so it is binding before any subsequent GUC is registered.**

## Operational policy

- The template is **hand-maintained**. Per-GUC prose comments and section
  grouping are operator-facing documentation, not derivable from registry
  metadata. (This mirrors how PG itself maintains `postgresql.conf.sample`
  — it is not generated from `guc.c`.)
- The template is the **single source of truth** for fresh-cluster
  `postgresql.conf` content. Once M0108-0002 lands, `defaultPostgresqlConf`
  in `initdb.go` is deleted and `SampleFiles()` calls `config.SampleConfig()`
  instead.
- GUC names in the template **must match PG's names exactly**. This is a
  hard usability invariant: an operator must be able to lift a tuned
  `postgresql.conf` from a PG installation and use it against goopg
  unchanged (modulo unsupported settings, which are no-ops in goopg
  today and ignored by the registry).
- The sync test is the mechanical enforcement; the AGENT.md rule is the
  human-facing reminder. Both are required.

## Scope

### In Scope

1. New file `internal/config/postgresql.conf.sample` (hand-maintained,
   PG-style sections, all entries commented out, defaults match
   `BuildDefaultRegistry` BootVals).
2. New file `internal/config/sample.go` exporting `SampleConfig() []byte`
   via `//go:embed`.
3. Updated `internal/initdb/initdb.go::SampleFiles()` — `postgresql.conf`
   entry switches to `config.SampleConfig()`; `defaultPostgresqlConf`
   deleted.
4. New test `internal/config/sample_test.go::TestSampleConfigCoversRegistry`
   enforcing template↔registry sync (every file-settable Variable has a
   commented entry; every entry resolves to a known Variable; default
   values match BootVal).
5. AGENT.md rule landed with this milestone-filing commit.

### Out of Scope

- Automatic generation of the template from the registry. Hand-maintained
  prose is intentional (matches PG).
- Backfilling `pg_settings` view from the registry (handled separately
  if a future milestone opens for it).
- Translating PG's 842-line `postgresql.conf.sample` verbatim. Only
  goopg-supported GUCs are listed.
- Adding new GUCs or new GUC functionality. This milestone is scaffolding;
  the registry's 76 entries are not modified.

## Definition of Done

For each M0108-NNNN sub-milestone:

1. Design doc `docs/design/0108-0001-postgresql-conf-sample-template.md`
   is referenced and remains `accepted`.
2. Sub-milestone-specific implementation merged on `for-performance-optimize`
   (or whichever branch is current).
3. `go test ./internal/config/...` PASS.
4. `go test ./internal/initdb/...` PASS — including all M0105/M0106
   byte-layout regression tests (the change is to file content, not to
   on-disk byte formats, but the gate must remain green).
5. `gofmt -l .` empty; `go vet ./...` clean; `make ralph-state-guard` PASS.

For the milestone overall:

1. All 3 sub-milestones (M0108-0001 / 0002 / 0003) have DoD satisfied.
2. `internal/config/postgresql.conf.sample` exists and contains a
   commented-out entry for every file-settable GUC in
   `BuildDefaultRegistry`. The sync test is green.
3. `goopg init <dir>` writes `<dir>/postgresql.conf` whose bytes equal
   the embedded sample.
4. `.ralph/AGENT.md` contains the GUC-sync rule and references the
   sync test as its enforcement gate.

## Sub-milestones

| ID | Title | Primary deliverable |
|---|---|---|
| M0108-0001 | Initial template body + `config.SampleConfig()` accessor | `internal/config/postgresql.conf.sample`; `internal/config/sample.go` |
| M0108-0002 | initdb wiring + retire `defaultPostgresqlConf` | `SampleFiles()` updated; embedded string deleted; regression test pins `<datadir>/postgresql.conf` bytes match `SampleConfig()` |
| M0108-0003 | Registry↔template sync test | `internal/config/sample_test.go::TestSampleConfigCoversRegistry` |

Ordering: 0001 → 0002 → 0003. (0001 lands the file; 0002 makes it
operationally meaningful; 0003 makes it enforced. Each is independently
revertible.)

## Required Design Docs

Already in the repo (filed with this milestone):

- [`docs/design/0108-0001-postgresql-conf-sample-template.md`](../design/0108-0001-postgresql-conf-sample-template.md)
  — Location decision, file format / commenting style, initdb wiring,
  sync-test design, AGENT.md rule text, out-of-scope items.

Per-sub-milestone follow-up design docs are not required — the single
0108-0001 doc covers all three implementation steps. New 0108-NNNN docs
may be added by implementing loops if a sub-milestone discovers a
non-trivial deviation from this design (e.g., section structure changes).

## Verification commands

```bash
# Focused per-sub-milestone:
go test ./internal/config/...
go test ./internal/initdb/...

# State guard (every Ralph loop):
make ralph-state-guard

# Manual sanity:
go run ./cmd/goopg init /tmp/sanity-data
head -40 /tmp/sanity-data/postgresql.conf
# Expect: PG-style FILE LOCATIONS / CONNECTIONS AND AUTHENTICATION
# section banners + commented-out #listen_addresses, #port, etc.
```

## Rollback rules

1. If `TestSampleConfigCoversRegistry` fails: do not merge; fix the
   template or fix the registry change in the same commit.
2. If `goopg init` regression test fails: do not merge; the embedded
   bytes from `SampleConfig()` must match what is written to disk byte-
   for-byte (modulo trailing newline if any).
3. If a PG-style section name needs to differ from PG's, document the
   deviation in `docs/design/0108-0001-postgresql-conf-sample-template.md`
   §"Upstream reference policy" before landing.
