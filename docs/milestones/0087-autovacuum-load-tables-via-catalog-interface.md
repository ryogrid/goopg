# Milestone 0087 — autovacuum loadTables via Catalog interface

**Status:** planned
**Depends on:** M0019 (autovacuum launcher)
**Drives:** decouple the autovacuum launcher from the
`*catalog.InMemory` concrete type so non-in-memory catalog
implementations (and test doubles) are vacuumable.

## Context

`internal/autovacuum/launcher.go::loadTables` is currently:

```go
func (l *Launcher) loadTables() []*catalog.Table {
    if c, ok := l.Cat.(*catalog.InMemory); ok {
        return c.AllTables()
    }
    return nil
}
```

This type-assertion is the only thing keeping autovacuum
from running against any future catalog implementation
(e.g. a disk-backed catalog, a sharded test double). When
`l.Cat` is anything other than `*catalog.InMemory`, the
launcher silently no-ops on every tick — the launcher
goroutine stays alive but does no work, and there is no
log line indicating the type-assertion failed.

## Required design docs

- `docs/design/0087-0001-catalog-iteration-interface.md`
  (extend `catalog.Catalog` with a table-iteration method —
  or a streaming variant for catalogs too large to
  materialise — and the migration story for existing
  callers).

## Tasks

Tasks will be detailed when this milestone is picked up. See
the fix_plan.md note about the milestone-only format.

## Definition of Done (sketch)

- `catalog.Catalog` exposes a table-iteration entry point
  (signature determined by the design doc — slice, iterator,
  or visitor).
- `autovacuum.Launcher.loadTables` uses the interface, not
  a type assertion against `*catalog.InMemory`.
- Existing in-memory catalog tests continue to pass.
- A test-double catalog (in `internal/autovacuum` test code
  or `internal/catalog/catalogtest`) demonstrates the
  launcher vacuuming through the interface.
- Any other type-assertion fallbacks against `*catalog.InMemory`
  in non-test code are noted (out of scope for this
  milestone but tracked).
