# Stats Persistence Through the Catalog Snapshot (Milestone 0006)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0006 — Planner-Grade Statistics                        |
| Refines    | [root-0017-data-directory.md](root-0017-data-directory.md), [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) |
| Supersedes | —                                                      |

## Problem

`0006-0001` lands MCV lists and equi-depth histograms into
`catalog.TableStats` / `ColumnStats`, but they live entirely in memory.
`SaveCatalog` writes `<DataDir>/global/pg_catalog.json` on shutdown and
`loadCatalogSnapshot` reads it on startup; today the encoder serialises
`TableEntry` (oid, schema, name, columns) but skips every Table's
`Stats` pointer.

Operators currently have to re-run `ANALYZE` after every clean stop /
start, which is unacceptable for any non-trivial dataset (TPC-H SF1's
`lineitem` ANALYZE is already noticeable; SF10+ would be punishing).

## Decision

Embed the existing `TableStats` and `ColumnStats` structs directly in
the JSON snapshot:

```go
type TableEntry struct {
    OID     uint32      `json:"oid"`
    Schema  string      `json:"schema,omitempty"`
    Name    string      `json:"name"`
    Columns []Column    `json:"columns"`
    Stats   *TableStats `json:"stats,omitempty"`
}
```

The serialisation reuses the in-memory types; no parallel `Entry`
shapes. JSON tags (`row_count`, `pages`, `avg_width`, `columns`,
`ndistinct`, `null_frac`, `mcv`, `histogram`, `value`, `frequency`)
freeze the wire format so future Go-side renames don't silently break
the file format.

### Forward-compat with snapshots that omit stats

`Stats` is `*TableStats` with `omitempty`, so:

- Snapshots saved before this commit (no `stats` key) round-trip
  cleanly through `json.Unmarshal` into `Stats == nil`. `Restore`
  installs the table with `t.Stats == nil` — exactly the
  pre-`ANALYZE` state.
- Tables with `Stats != nil` that have all-zero values still serialise
  (the field is non-nil; `omitempty` only checks the pointer, not the
  pointee).

The milestone's DoD #2 — "old snapshots without stats still load and
simply present unanalysed relations" — is satisfied by construction;
no per-version gating needed.

### Determinism

`Snapshot()` already returns Tables in OID-sorted order, and JSON
encoders walk struct fields in declaration order. Adding `Stats` at
the end of `TableEntry` and freezing it via JSON tags keeps the
"two `Snapshot()` calls of the same catalog produce byte-identical
encodings" property the existing
`TestSnapshotIsDeterministic` pins.

The `Stats` payload itself is deterministic when the underlying
`ANALYZE` is — `0006-0001`'s `Context.AnalyzeRandSeed` is the seam
that makes that contract testable.

### `SetTableStats` mutation under the catalog lock

The in-memory mutation `Catalog.SetTableStats` already holds the
catalog write lock. `Snapshot()` holds the read lock for the duration
of its copy. The persistence path is therefore consistent without any
new synchronization.

### Out of scope

- **Stats-only incremental persistence.** Today the entire snapshot is
  re-written on shutdown. `ANALYZE` does not trigger a snapshot write
  on its own; that's a separate "auto-persist" feature out of scope
  for M0006.
- **Binary on-disk format.** Upstream's `pg_statistic` is a binary
  catalog table indexed by `(starelid, staattnum)`. v0 stays on the
  JSON snapshot until `0017-data-directory`'s migration gate fires.
- **Per-column sample-rows or stamped-`ANALYZE`-time metadata.**
  Useful for staleness detection later; deferred.

## Verification

`internal/catalog/persist_test.go` grows
`TestSnapshotPreservesTableStats`: build a catalog, attach a
fully-populated `TableStats` (RowCount, Pages, AvgWidth, two
`ColumnStats` slots — one with MCV + histogram, one bare with just
NDistinct / NullFrac), snapshot → JSON-encode → JSON-decode → restore,
assert deep-equal. Plus `TestSnapshotOmitsStatsWhenNil` to pin the
forward-compat property: no `Stats` field key when the table hasn't
been analysed.

End-to-end the path is exercised by the existing
`internal/initdb/open.go` `Runtime.SaveCatalog` / `loadCatalogSnapshot`
seam — no production wiring change needed; the snapshot file simply
grows the new field on the next clean stop / start.

## Cross-references

- `0006-0001-sampling-and-mcv-histograms.md` — defines the in-memory
  shape this loop now persists.
- `root-0017-data-directory.md` — the broader catalog-snapshot story
  and migration gate for switching to a binary `pg_class` /
  `pg_statistic` layout.
- `0003-0010-analyze-statistics.md` — predecessor, lists "catalog
  persistence" as deferred; this loop closes that gap.
