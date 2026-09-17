Task: M0143-0002c — re-identify the "six `deleteCatalogRowsForOID` sites"
M0143-0002's original text named. DONE this loop as investigation-only (no
code changed) — closed with a much bigger finding than expected; the real
fix is filed as M0143-0002d (unchecked, concrete resume point).

Files: .ralph/fix_plan.md (M0143-0002c marked [x] with a long Done note;
new M0143-0002d task filed, Parent: M0143-0002c), .ralph/deferral_ledger.md
(1 new row). No source files changed — two scratch test files
(internal/testport/m0143_0002c_composite_type_nondefault_db_test.go,
internal/testport/zzz_probe_composite_test.go) were written, run to gather
evidence, and DELETED before finishing (git status confirms clean — verify
with `git status --porcelain -- internal/testport/` before trusting this).

Key symbols: `writeTypeHeapRowWithIndexes` (operators_ddl.go:18447-18458,
pg_type writer shared by every CREATE TYPE/DOMAIN of any kind) and
`syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel`
(:18539-18544/:18555-18561) — both hardcode
`storage.RelFileNode{DBOid: catalog.DefaultDBOid, ...}` unconditionally.
The six delete-side sites M0143-0002c was asked to find: `execAlterType`'s
4 single-subcommand branches (:25242/:25282/:25325/:25372),
`execAlterTypeAttrCmds` (:25617), `execDropType`'s composite branch
(:25669) — same hardcode, `deleteCatalogRowsForOID`/`deleteTypeFromCatalogHeap`.
Contrast (the ALREADY-correct pattern to mirror): `pgConstraintTableRel`
(sys_pg_constraint.go:133, routes via `tableCatalogHeapDBOid(ctx)`) and
`insertCanonicalSysBtreeLeaf` (sys_catalog_index_insert.go:423-428, same
routing — already used for these same types' OWN index entries, so the
per-DB index files already exist and work).

Findings: the six sites are real but NOT independently fixable — they're
consistent with, not separate from, a much larger pre-existing gap:
EVERY user-defined type's (enum/domain/composite/range, all sharing
`writeTypeHeapRowWithIndexes`) physical pg_type/pg_class/pg_attribute row
always lands in the shared DEFAULT database's heap files, regardless of
which database's session ran the CREATE/ALTER/DROP TYPE. Live-confirmed on
a throwaway 55xx cluster (test wrote + ran + deleted, not committed):
`CREATE TYPE samename AS (a int)` in default db, then inside
`CREATE DATABASE r; \c r`, `CREATE TYPE samename AS (x text, y text)`
succeeds with NO name-collision error (correct — they're genuinely
different per-DB types in the in-memory registry, `compositeKey` folds in
dbOid), but `SELECT a.attname FROM pg_attribute a JOIN pg_type t ON
t.typrelid=a.attrelid WHERE t.typname='samename'` run from EITHER database
returns the UNION `[a, x, y]` instead of each database's own `[a]` /
`[x, y]` — genuine cross-database catalog corruption reachable by plain
SQL (pg_dump, `\d`, information_schema all ride this same serving path).
Ruled out: this is NOT the M0143-0002/-0002b hardcode shape (that was
`c.ns(DefaultDBOid)` inside `catalog.InMemory`'s own table/index
registries, already fixed both times) — this one is a
`storage.RelFileNode{DBOid: ...}` literal choosing which database's
PHYSICAL HEAP FILE to write/read, a different layer entirely. Also ruled
out via a live probe (test written and deleted): pg_attribute rows for a
composite type in a LIVE session show blank `ctid`, so a naive "does ALTER
duplicate a live-queryable row" test (my first attempt) always passes
regardless of the bug — it doesn't prove anything, because per the union
finding above the SAME shared file is scanned by both connections'
queries, not two, so there's no dbOid-scoped visibility to differentially
observe from a single-DB vantage point; only a cross-database query
comparison (the samename repro) actually demonstrates the defect. Also
ruled out: read-side is NOT served from the in-memory
`cat.compositeTypeFields` registry (which IS correctly per-DB-keyed) — if
it were, the cross-DB union would be impossible; the read path for
`pg_type`/`pg_attribute` genuinely re-derives from the shared heap file,
not yet located precisely (M0143-0002d's open item).

Next step: `## Current Priority` banner still gates on P0-E7 (needs P0-E6,
owner-run, `[!]`) for everything from item 1 onward. Sub-order unchanged
while P0-E6 waits: M0143 tasks whose gates don't need TPC-H data first.
Good next picks, none gated on TPC-H data: M0143-0002d (this loop's
follow-up — has a full write-side site list and a design-doc requirement,
but the read-side resolver for pg_type/pg_class/pg_attribute SeqScans
still needs locating before any fix can land — start there, e.g. grep how
`SysScan`/`SeqScan` resolves a system catalog table's `RelFileNode` in
general, compare against `pgConstraintTableRel`'s special-cased override,
to find whether pg_type/pg_class/pg_attribute have an equivalent override
that's ALSO hardcoded, or fall through to some other generic default),
M0143-0001 (has a concrete `internal/postmaster`-level chain-test resume
point from 2 loops ago, not yet started), M0143-0003 (pg_constraint 0-rows
after restart), M0143-0004 (PhysicalTypeIsVarlena IsArray), M0143-0005
(ParamRef LIMIT+DISTINCT), M0143-0006 (parser's 60 failing tests, still
unowned — same count/shape seen 4 loops running now, consider actually
triaging it next since "unowned" is explicitly called out as not an end
state). Re-check the banner fresh next loop per the Precedence rule.

Gates run: no source files changed this loop, so no build/test gate was
required by AGENT.md's risk-based policy (investigation-only, mirrors
M0143-0001's precedent). `go vet ./internal/testport/...` was run while the
two scratch test files existed (clean) and again after deleting them
(clean, confirms no orphaned references). `make ralph-state-guard` — found
a PRE-EXISTING inconsistency from a prior loop's stale progress marker
(status="running" vs progress="completed"), auto-repaired by the guard
itself to progress="in_progress"; not something this loop's edits caused
(this loop touched only fix_plan.md/deferral_ledger.md). Consistent after
repair.

In-flight: none. No servers or background processes running (the deleted
scratch tests used `t.Cleanup`-managed throwaway clusters via
`cluster.New`, already torn down when `go test` exited).
