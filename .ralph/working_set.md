Task: M0143-0002d (design-first recon, per its own filing instruction) —
COMPLETE this loop, committed. Landed a design doc plus decomposition into
two loop-sized child tasks; no production code touched.

Files:
- docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md (new)
- docs/design/README.md (new index row)
- .ralph/fix_plan.md (M0143-0002d ticked [x] with Done note; filed
  M0143-0002e/-0002f)

Key symbols: `tableCatalogHeapDBOid` (operators_ddl.go:18697), `RelFileNode`
(catalog.go:22354), `loadSystemCatalogsIfPresent` (open.go:2931),
`RegisterIndexDuringRecoveryForDB` (catalog.go:6584, the precedent to mirror),
`copyBootstrapCatalogImage` (initdb.go:426, proves provisioning already
exists).

Findings: M0143-0002d's own fear ("materially bigger... provisioning") is
REFUTED — every DB already gets its own base/<dbOid>/1247|1249 heap file at
CREATE DATABASE time. The remaining gap is in-memory registration/routing
only: pg_type/pg_attribute are singleton `catalog.Table`s with no DBOid, so
every connection's SeqScan resolves via the one process-wide `c.dbOid`
regardless of ctx.CurrentDatabaseOid. pg_class doesn't share the bug because
its SELECT output is virtual (per-DB filtered already), not a real heap
SeqScan. Fix mirrors the index precedent exactly (RegisterIndexDuringRecoveryForDB
+ its per-DB startup loop) and must land read-side (M0143-0002e) before
write-side (M0143-0002f), else a distinct-dbOid connection's own new type
would become invisible to itself.

Next step: select the next task per the `## Current Priority` banner
(re-read fresh — P0-E6 may have completed). While P0-E6 stays `[!]`: next
M0143 candidate whose gate doesn't need TPC-H data is M0143-0002e (now
filed, read-side registration, unit-test-only gate) — implement it, in order,
before M0143-0002f (which depends on it). If P0-E6 flipped `[x]`, the banner
promotes P0-E7 ahead of M0143 remainder — re-read the banner text itself,
not this note, since it is the sole ordering authority.

Gates run: `make ralph-state-guard` PASS (self-repaired a stale
running/completed marker mismatch, routine). No go build/test needed — no
.go files touched this loop (docs(...)/ralph(...) commit only, D5).

In-flight: none.
