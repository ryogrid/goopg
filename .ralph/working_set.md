Task: M0143-0002d — user-defined type catalog-heap storage is a single
un-partitioned store shared by every database (read-side half). Still `[ ]`
(unchecked) — this loop is investigation-only, no code changed, same shape as
the M0143-0002c precedent.

Files: .ralph/fix_plan.md only (M0143-0002d's read-side paragraph rewritten
with the confirmed root cause; fixed an edit-boundary duplication artifact in
the same edit). No source files touched, nothing to build.

Key symbols (read-side root cause, now fully located — this was the loop's
deliverable): `loadSystemCatalogsIfPresent` (internal/initdb/open.go:2931-2972,
registers pg_type/pg_attribute exactly ONCE at startup, `DBOid` field unset →
defaults to DefaultDBOid's namespace only, no per-database call exists
anywhere), `catalog.InMemory.RelFileNode` (internal/catalog/catalog.go:22354-
22366, falls back to the single process-wide `c.dbOid` field whenever
`table.DBOid` is 0/DefaultDBOid — true for both these Table structs always),
`c.dbOid` (stamped once by `SetDBOID` at startup, catalog.go:4779-4792,
doc comment at :22301-22320 confirms "process-wide"). Contrast confirmed:
`pgConstraintTableRel` (sys_pg_constraint.go:133) computes
`tableCatalogHeapDBOid(ctx)` FRESH per call from `ctx.CurrentDatabaseOid`;
pg_type/pg_attribute's resolution is baked into a registration-time struct
and never revisited per-connection — a different kind of bug, not just
"missing the same override".

Findings: every SeqScan/write against pg_type or pg_attribute, from ANY
connected database, resolves to the exact same physical heap file — confirmed
this is the mechanism behind M0143-0002c's live cross-database UNION repro.
Fix shape (recorded in fix_plan.md, not yet designed in detail): real PG has
pg_type/pg_class/pg_attribute as non-shared catalogs (relisshared=false) —
each database needs its own physical `base/<dbOid>/1247`/`1249` file plus its
own per-DB Table registration (`t.DBOid` set), most likely provisioned at
CREATE DATABASE time (mirror `syncCopiedTableCatalogHeap`'s per-table pattern,
internal/postmaster/database_ddl.go:1291-1335) plus a startup-reload pass
covering every already-registered database, not just the default one. This is
materially bigger than the already-scoped write-side hardcode list (8 sites in
operators_ddl.go) — genuinely needs a design doc before code lands (AGENT.md
D3), so deliberately NOT attempted in this loop or squeezed into a rushed
partial fix.

Next step: `## Current Priority` banner (fix_plan.md) still gates on P0-E7
(needs P0-E6, owner-run, `[!]`) for everything from item 1 onward; while
P0-E6 waits, select M0143 tasks whose gates don't need TPC-H data. Good picks,
none gated on TPC-H data: M0143-0002d itself (the read-side root cause is now
fully located — next loop should either (a) write the required design doc
sketching the provisioning + registration + write-side fix as one coherent
plan, since a design doc is a safe, code-free, reversible next step that
directly unblocks implementation, or (b) if design bandwidth is thin, pick a
smaller independent M0143 task instead and leave 0002d for a loop with more
room), M0143-0001 (in-process cross-database test, concrete resume point from
prior loops, not yet started), M0143-0003 (pg_constraint 0 rows after
restart), M0143-0004 (PhysicalTypeIsVarlena IsArray), M0143-0005 (ParamRef
LIMIT+DISTINCT), M0143-0006 (parser's 60 failing tests, still unowned — same
count/shape seen 5 loops running now; strongly consider triaging it next
since "unowned" is explicitly called out as not an end state and this has now
recurred across many loops without anyone picking it up). Re-check the banner
fresh next loop per the Precedence rule.

Gates run: no source files changed this loop (fix_plan.md only), so no
build/test gate was required by AGENT.md's risk-based policy — mirrors the
M0143-0001/M0143-0002c precedent for investigation-only loops. `make
ralph-state-guard` found a PRE-EXISTING inconsistency carried from a prior
loop's stale progress marker (status="running" vs progress="completed",
loop_count=7 timestamp 2026-09-17T11:43:39Z), auto-repaired by the guard
itself to progress="in_progress"; not caused by this loop's edits (fix_plan.md
only). Consistent after repair, re-verified clean.

In-flight: none. No servers or background processes running this loop.
