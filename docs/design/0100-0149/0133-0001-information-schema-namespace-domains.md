# M0133-S1 — `information_schema` namespace + domains, atomically

**Status:** accepted — landed 2026-08-13.
**Milestone:** M0133 (`information_schema` on disk), slice S1.
**Supersedes:** the S9.4a successor step in `0131-0009-system-view-corpus-widening.md`
§"Successor decomposition".

## What landed

The `information_schema` **namespace** (13273) plus its **five domains, their five
array peers, and the two domain CHECK constraints** — in one bootstrap step, so a
hosted PG never observes a namespace that exists but has no types. A real PG 18.3
cold-started on a goopg `$PGDATA` now resolves `information_schema.sql_identifier`,
rejects `-1::cardinal_number` and `'MAYBE'::yes_or_no` with the constraint named,
and `format_type(13287, NULL)` answers `information_schema.cardinal_number`
(schema-qualified — `information_schema` is not in the default search_path).

Everything below is **measured against a fresh PG 18.3**, not read from a `.dat`
file: `information_schema` is created by initdb running `information_schema.sql`
*after* bootstrap, so its OIDs come from the post-bootstrap counter at
`FirstUnpinnedObjectId = 12000` — the same counter M0131-S9 pinned for the 80
`system_views.sql` views (12000..12355) and the 894 `pg_description` rows
(12356..13272). The domain band starts at 13286.

## Measured object graph

| name | OID | kind | base / element | typmod | collation |
|---|---|---|---|---|---|
| `_cardinal_number` | 13286 | array | elem 13287 | -1 | 0 |
| `cardinal_number` | 13287 | domain | base 23 (int4) | -1 | 0 |
| `cardinal_number_domain_check` | 13288 | constraint | contypid 13287 | — | — |
| `_character_data` | 13289 | array | elem 13290 | -1 | 950 (C) |
| `character_data` | 13290 | domain | base 1043 (varchar) | -1 | 950 |
| `_sql_identifier` | 13291 | array | elem 13292 | -1 | 950 |
| `sql_identifier` | 13292 | domain | base 19 (name) | -1 | 950 |
| `_time_stamp` | 13297 | array | elem 13298 | -1 | 0 |
| `time_stamp` | 13298 | domain | base 1184 (timestamptz) | 2 | 0 |
| `_yes_or_no` | 13299 | array | elem 13300 | -1 | 950 |
| `yes_or_no` | 13300 | domain | base 1043 (varchar) | 7 (VARHDRSZ+3) | 950 |
| `yes_or_no_check` | 13301 | constraint | contypid 13300 | — | — |

The gap between `sql_identifier` (13292) and `_time_stamp` (13297) is
`information_schema_catalog_name`, the first **view** created by the file (line
212), which sits between `sql_identifier` and `time_stamp` in
`information_schema.sql` order — the domains and their constraints share one OID
counter with the views' `pg_class`/`pg_type`/`pg_rewrite` objects, so the domain
OIDs are NOT contiguous.

## Design

### 1. Namespace

`pgNamespaceInitialEntries()` gains a fourth row `{13273, "information_schema", 10}`.
It flows through the existing `pgNamespaceRow` → `bootstrapPgNamespaceTuples` →
`bootstrapPgNamespaceNspnameIndex`/`OidIndex` path with no new code. The
`catalog/bootstrap_namespace_oid_test.go` pin already covered 13273 (F31's fix);
it now also exercises the `SchemaNameForOID(13273)` reverse direction.

### 2. Domain + array-peer `pg_type` rows

`pgTypeCanonical()` gains ten cases (the domain rows use `domain_in` 2597 /
`domain_recv` 2598 as typinput/typreceive and the base's typoutput/typsend, since
`getTypeOutputInfo` reads the DOMAIN row and renders through its base). Four new
overlay maps, keyed by OID like the existing `pgTypeElemArrayOverlay` /
`pgTypeRelidOverlay`, carry what `pgTypeRow` still hardcodes for every other row:

- `pgTypeNamespaceOverlay` — typnamespace 13273 (default 11).
- `pgTypeBaseTypeOverlay` — typbasetype (default 0).
- `pgTypeTypModOverlay` — typtypmod (default -1; `time_stamp`=2, `yes_or_no`=7).
- `pgTypeElemArrayOverlay` (extended) — the array-peer/domain `typelem`/`typarray`/
  `typsubscript` edges (peer→domain, `typsubscript` 6179 for arrays; domain→peer).

`pgTypeCollationForOID()` gains the six collated rows (character_data,
sql_identifier, yes_or_no and their array peers → 950). `pgTypeBootstrapEntryMap()`
pulls the ten OIDs in via `pgTypeInformationSchemaDomainOIDs()` — none is referenced
by any nailed attr, so the existing loops would miss them.

### 3. Domain CHECK constraints (`pg_constraint`)

Two things, both required — a hosted PG reads constraints through the index AND
casts the tuple to the full `FormData_pg_constraint`, so a short descriptor is as
fatal as a missing row.

- **The descriptor was wrong, not merely short.** `pgConstraintAttrs()` described
  **11** columns where PG18 has **28** (20 fixed + 8 varlen), and past column 6 the
  numbering diverged: goopg put `convalidated` at 7 where PG18 has `conenforced`.
  Same class as M0131-S9.3f F27 (pg_type 14→32). `pgConstraintAttrs()` now carries
  the full 28 and `nailedLocalRels[2606]` says `relnatts = 28`. The heap already
  used the 28-column layout at runtime (`executor.PGConstraintColumnsPG18()`,
  `buildPGConstraintRowForDomainCheck`), so only the *description* moved — the
  `catalog_heap_reload` path was already consistent.
- **The two rows** are written by `bootstrapPgConstraintTuples`
  (`internal/initdb/pg_constraint_bootstrap.go`), replacing the empty placeholder
  (2606 removed from `mappedLocalCatalogPlaceholderOIDs`). `conbin` is the
  **verbatim nodeToString** captured from PG 18.3 — not the runtime's raw-text
  "adbin convention" (`buildPGConstraintRowForDomainCheck`), which a standby cannot
  parse — so a hosted PG actually *enforces* the checks.
- **Three indexes** are populated (2665/2666/2667). 2666 (`contypid`) is what
  `GetDomainConstraints` scans — without it the checks are invisible. The composite
  2665 needed a new `pgBuildIndexTupleOidOidNameKey` builder (80-byte
  `(oid,oid,name)` tuple).

## Findings

**F1 — `pg_type_typname_nsp_index` (2704) hardcoded `typnamespace = 11`.**
`bootstrapPgTypeTypnameNspIndex` wrote every tuple with a literal `11` and its
comment asserted "all bootstrap types share typnamespace=11", so a hosted PG's
`LookupTypeName('information_schema.sql_identifier')` found nothing and failed
42P01. The fix reads `pgTypeNamespaceOverlay` per OID and sorts by
`(typname, typnamespace)`. This is the twin of the `pgTypeRow` namespace change —
the sibling-path agreement rule (`pattern_sibling_paths_must_agree`).

**F2 — `format_type` is schema-qualified for non-search-path schemas.**
`format_type(13287, NULL)` returns `information_schema.cardinal_number`, not
`cardinal_number`, because `information_schema` is not in the default search_path.
The E2E probe asserts the qualified form — the unqualified form would have been a
*weaker* (wrong) assertion, and the qualified form is also what proves the domain
landed in 13273 rather than pg_catalog.

**F3 — the atomicity constraint is one commit, not one function.**
The namespace, the ten `pg_type` rows and the two `pg_constraint` rows land in the
same commit. A namespace without its domains would be the half-filled-catalog
anti-pattern F16 warned about (a partial `pg_type` is worse than an absent one);
here the failure is the other direction — a hosted PG resolving the namespace but
finding no `sql_identifier` type.

## Guards

- `TestE2E_PGColdStartOnGoopgDataDir` → `assertInformationSchemaDomainsResolvable`:
  three domain casts resolve, `format_type` names the domain, `typtype='d'`, and the
  two CHECKs reject their violating values **by name**.
- `assertNonCorpusSystemViewIsStillAbsent` stays green: it probes the **view**
  `information_schema.tables` (M0133-S4's work), which is still absent — only the
  namespace and domains landed. Its comment was updated to say so.
- `TestBootstrapPgTypeTypnameNspIndexWritesPopulatedBtree` now asserts the ten
  information_schema types carry `nsp=13273` and the rest carry `11`.
- `TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs` forced a `pgTypeCanonical` case
  for `_int2` (1005), the `conkey`/`confkey`/`confdelsetcols` type.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages` moved 2606 from the
  "must have an empty placeholder" set to the "must be omitted" set.

Gates: `internal/initdb` PASS (224 s), `internal/catalog` PASS,
`TestE2E_PGColdStartOnGoopgDataDir` PASS, `go build ./...` + `go vet` clean,
UNITS (`RALPH_PRECOMMIT_SCOPE=units`) PASS, pgbench smoke via the commit hook.
