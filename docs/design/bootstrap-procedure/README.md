# Bootstrap & Standby-Startup Procedure for goopg

**Status:** accepted
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility) and follow-ups
**Audience:** Claude Code (coding agent) implementing the remaining
goopg `internal/initdb/` and `internal/{catalog,executor,wal,
checkpointer,replication}/` work.

---

## Why this doc set exists

`.ralph/fix_plan.md` milestone M0106-0010 has accumulated **116+ "step
3\*" sub-steps** (3a → 3dm) under a purely reactive model: run the E2E
`TestE2E_FailoverGoopgToPG/async`, observe the next vanilla PG18
standby `FATAL` / `PANIC` / `SIGSEGV`, seed exactly one missing catalog
tuple or index, re-run, repeat. The pattern has not converged.

This document set replaces that loop with a **batched specification**.
It enumerates, with upstream-source citations, both:

1. **Initdb-time output** — every directory, flat file, control file,
   WAL segment, catalog heap page, system index, view rewrite rule, and
   relcache init file that goopg's `initdb` must produce so the cluster
   matches a fresh PG18 cluster byte-for-byte (where bit-identity is
   feasible) or semantically (where pointers / OIDs differ); and
2. **Continuous maintenance** — the rules goopg must follow at runtime
   to keep every PG-read file in a PG-compatible state as the cluster
   evolves (DDL, INSERT/UPDATE/DELETE on system tables, checkpoint,
   vacuum, reindex, slot lifecycle, promotion). A vanilla PG18 backend
   may attach as standby at **any time** during goopg's lifetime — not
   only immediately after initdb — so every artefact PG reads on attach
   must remain current.

When the spec is implemented end-to-end, `TestE2E_FailoverGoopgToPG/async`
must pass without further reactive step-3\* iterations.

---

## Reading order

| # | File | Purpose |
|---|------|---------|
| 0 | `README.md` (this file) | Index, glossary, conventions |
| 1 | [`01-data-directory-layout.md`](01-data-directory-layout.md) | Every directory and flat file under `$PGDATA` |
| 2 | [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md) | `global/pg_control` layout, CRC, checkpoint record |
| 3 | [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md) | First WAL segment headers and shutdown record |
| 4 | [`04-shared-catalog-bootstrap.md`](04-shared-catalog-bootstrap.md) | `global/` catalogs and the 6 critical shared indexes |
| 5 | [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md) | `base/N/` nailed catalogs and the 7 critical local indexes |
| 6 | [`06-bki-derived-catalog-seeds.md`](06-bki-derived-catalog-seeds.md) | Non-nailed catalogs seeded from `pg_*.dat` |
| 7 | [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md) | `system_views.sql` views, `pg_stat_wal_receiver`, `pg_rewrite._RETURN` |
| 8 | [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md) | `pg_internal.init`, `PG_VERSION` |
| 9 | [`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md) | Signal files, timeline history, slot files, basebackup |
| 10 | [`10-implementation-roadmap.md`](10-implementation-roadmap.md) | Ordered batched task list for `internal/` changes |
| 11 | [`11-continuous-maintenance.md`](11-continuous-maintenance.md) | Cross-cutting matrix: per-operation → affected artefacts |

Recommended reading: 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 11 → 10.

Cross-reference graph:

- `02 → 03` — ControlFile references checkpoint LSN inside the first
  WAL segment.
- `04, 05 → 08` — relcache init file enumerates these relations.
- `06 → 05` — opclass/amop indexes are critical local indexes.
- `07 → 04, 05` — pg_subscription is shared (04); pg_rewrite is local
  (05).
- `09 → 02, 01` — `CheckRequiredParameterValues` reads ControlFile;
  basebackup exclusions live under `$PGDATA`.
- `11 → 01..09` — every operation row links to the per-artefact doc.
- `10 → 01..11` — task ordering derives from the spec; tasks 1–7
  inherit from `02` (ControlFile) and `03` (WAL), tasks 8–32 from
  `04`–`07`, tasks 28–35 from `09` (replication).

---

## Per-file section structure

Each per-artefact file (01–10) follows this layout:

1. **Scope** — what the file does and does not cover; relation to
   adjacent files in this set.
2. **Upstream references** — the small set of `postgres/src/...` files
   that ground every claim, listed once at the top so individual
   citations later can be short (`path:LINE`).
3. **Initdb-time output** — the deterministic state the file's artefact
   should hold immediately after `initdb` finishes. Tables, OIDs,
   column lists, struct layouts.
4. **Continuous maintenance** — the runtime mutation rules: which goopg
   operations touch the artefact, which WAL records must be emitted,
   which invalidation messages must fire, which on-disk format
   invariants are preserved.
5. **What goopg must produce** — diff between (3) + (4) and the
   current state of `internal/` files. Marked `done` / `partial` /
   `missing`. Names existing or new files under `internal/`.
6. **Verification** — concrete commands or assertions that prove the
   artefact is correct (e.g. byte-diff vs a fresh `pg_initdb`, `psql`
   queries, dedicated unit tests).

File `11-continuous-maintenance.md` is structured as one section per
goopg operation rather than per artefact, and points back to the
per-artefact rules.

---

## Glossary

- **BKI** — bootstrap input language. Files like
  `postgres/src/include/catalog/postgres.bki` describe initial catalog
  rows; `initdb`'s bootstrap backend (`bootstrap.c`) consumes them
  before any SQL phase runs.
- **Nailed relation** — a system catalog whose `RelationData` is
  constructed at startup *without* reading `pg_class`, because it is
  needed before the relcache is loadable. PG calls `formrdesc()` for
  these. The four local nailed catalogs are pg_class (1259),
  pg_attribute (1249), pg_proc (1255), pg_type (1247); the five shared
  nailed catalogs are pg_database (1262), pg_authid (1260),
  pg_auth_members (1261), pg_shseclabel (3592), pg_subscription
  (6100). Defined in
  `postgres/src/backend/utils/cache/relcache.c::formrdesc`.
- **Critical index** — a system index loaded via `load_critical_index()`
  before the relcache init file path is exercised; the catalog cache
  cannot function without these. Listed in `relcache.c`.
- **Relcache init file** — `global/pg_internal.init` (shared catalogs)
  and `base/<dboid>/pg_internal.init` (per-database). Binary blob of
  `RelationData`/`Form_pg_class`/`Form_pg_attribute` for every nailed
  rel + critical index, read by
  `load_relcache_init_file()`. Magic `RELCACHE_INIT_FILEMAGIC`. Defined
  in `relcache.c`.
- **ControlFile** — `global/pg_control`. Binary `ControlFileData`
  struct + CRC32C, sized to `PG_CONTROL_FILE_SIZE` (8192 bytes).
  Holds `system_identifier`, `pg_control_version`,
  `catalog_version_no`, checkpoint pointers, GUC echoes, sizing
  constants. Defined in `postgres/src/include/catalog/pg_control.h` and
  written by `postgres/src/backend/access/transam/xlog.c::WriteControlFile`.
- **`formrdesc()`** — relcache fall-back path: when the init file is
  unreadable, PG builds nailed-rel descriptors from C-source structs.
  Covers only the 9 nailed rels above; *not* their indexes — index
  descriptors come from `load_critical_index()` which scans
  `pg_class`, which is why goopg must populate pg_class properly even
  if `pg_internal.init` is present.
- **WAL receiver** — backend process started on a standby after
  `standby.signal` is observed; pulls WAL from the primary over the
  streaming replication protocol. Implemented in
  `postgres/src/backend/replication/walreceiver.c`.
- **Hot standby** — recovery mode in which the standby accepts
  read-only client connections while replaying WAL. Requires
  `wal_level≥replica` on the primary and standby GUCs ≥ values stored
  in the primary's ControlFile.

---

## Status legend (used in every "What goopg must produce" section)

| Marker | Meaning |
|--------|---------|
| `done` | goopg already produces / maintains this artefact correctly. |
| `partial` | goopg produces some fields/rows but is missing others; specific gaps named. |
| `missing` | goopg does not produce / maintain this artefact at all. |
| `superseded` | An older approach (typically a 0106-0010-step3\* doc) has been replaced by the rule documented here. |

---

## Source citation conventions

- Upstream paths are written relative to `postgres/` so they are
  resolvable from the repo root (e.g. `src/backend/utils/cache/relcache.c:6204`).
- Line numbers reference the version of PostgreSQL 18.3 vendored in
  `postgres/`. If a future submodule bump invalidates line numbers,
  the surrounding function name should still be unique.
- Catalog OIDs are taken from `src/include/catalog/pg_*_d.h`
  (autogenerated) and `src/include/catalog/pg_*.h`.
- `pg_*.dat` files in `src/include/catalog/` are the source of truth
  for non-nailed catalog rows.
- The `genbki.pl` build script in `src/backend/catalog/` is the only
  way to derive the canonical `postgres.bki` file goopg must
  reproduce. Reading the script is sometimes necessary to understand
  which `.dat` entries become which on-disk tuples.

To navigate the upstream tree efficiently the project relies on the GNU
GLOBAL index pre-built under `/home/ryo/work/goopg/goopg/postgres/`:

```bash
cd /home/ryo/work/goopg/goopg/postgres
global -x SymbolName            # definitions with file:line
global -rx SymbolName           # references
global -f path/to/file.c        # symbols in a file
```

---

## Pointer back to the milestone tracker

The authoritative open-work list is `.ralph/fix_plan.md` milestone
**M0106**. Once this doc set lands, the M0106 section header should
link here so future Ralph loops read the batched plan instead of the
next step-3\* error.

See [`10-implementation-roadmap.md`](10-implementation-roadmap.md) for
the ordered list of `internal/` changes derived from this spec, and
[`11-continuous-maintenance.md`](11-continuous-maintenance.md) for the
per-operation affected-artefact matrix.
