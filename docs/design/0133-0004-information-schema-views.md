# M0133-S4 — `information_schema` views (65, tranche 1 = 29 of 33)

**Status:** accepted — tranche 1 landed 2026-08-13.
**Milestone:** M0133 (`information_schema` on disk), slice S4.
**Supersedes:** S9.4d in `0131-0009-system-view-corpus-widening.md` §"Successor decomposition".

## What landed

The first tranche of the 65 `information_schema` views — **29 of the 33
catalog-direct leaves** — seeded on disk so a hosted PG 18.3 cold-started on a
goopg `$PGDATA` resolves and **evaluates** `SELECT * FROM
information_schema.<view>` for each. The slice reuses the M0131-S9
capture/pin/regen loop unchanged (Option-A identity pinning), with one new
generator and one forced btree fix (below).

Four of the 33 are **withheld** on incomplete goopg catalog descriptors
(ledgered): `character_sets` / `collations` / `collation_character_set_applicability`
read `pg_collation` (3456), which goopg seeds with 8 columns where PG18 has 12
(and `collcollate` as `name` where PG18 has `text`); `triggers` reads `pg_trigger`
(2620), which goopg seeds with 8 columns where PG18 has 19. Both are a
**catalog-descriptor-completion** slice, not a view-capture problem — the
ev_action blobs are verbatim PG 18.3.

## Why 33 and not 65

All three blockers that could have gated S4 were cleared before this slice: S2's
11 helper functions (the `:funcid` surface), S3's data tables (the bulk-heap-load
mechanism), and the F4 fix (`typispreferred` for the eight PG18 preferred types,
so the views' `character_data = 'x'` / `||` WHERE-clause operators resolve). With
those on disk, the 65 views split cleanly into four tranches by what their
`ev_action` references — measured against a fresh PG 18.3, not assumed:

| tranche | criterion | count |
|---|---|---|
| **1 (this slice)** | catalog-direct, no in-band `:relid`, no in-band `:funcid`, stored ≤ 8000 B | 29 (+ 4 withheld on descriptor gaps) |
| 2 | TOAST (stored > 8000 B — the 11 F33 values) | 11 |
| 3 | helper-function (`:funcid` 13274..13285) | 4 (`key_column_usage`, `parameters`, `sequences`, `triggered_update_columns`) |
| 4 | view-on-view (`:relid` in 13293..13621) + `element_types`/`data_type_privileges` | 17 |

## Measured object graph

The `information_schema` band shares the single post-bootstrap counter with the
pg_catalog corpus, and is **not dense** — objects interleave by
`information_schema.sql` creation order:

```
13273          the information_schema namespace                     [S1]
13274..13285   the 11 helper functions (13280 is a hole)           [S2]
13286..13300   the 5 domains + 5 array peers                       [S1]
13293..13621   the 65 views (interleaved with the domains)
13456..13475   the 4 data tables (5 OIDs each)                     [S3]
```

Every view's reltype is `oid+2` (composite rowtype) and its `_RETURN` rule is
`oid+3`, verified for all 65 (`reltype = oid+2 AND rule = oid+3` holds across the
whole band). goopg keeps the M0131-S6.5 divergence: `RelType 2249` (RECORDOID),
not the composite.

### The 16 view-on-view edges (tranche 4, already measured)

`applicable_roles → administrable_role_authorizations`;
`enabled_roles → role_{column,routine,table,udt,usage}_grants`;
`column_privileges → role_column_grants`, `routine_privileges → role_routine_grants`,
`table_privileges → role_table_grants`, `udt_privileges → role_udt_grants`,
`usage_privileges → role_usage_grants`;
`{attributes,columns,domains,parameters,routines} → data_type_privileges →
element_types`;
`_pg_foreign_table_columns → column_options`;
`_pg_foreign_data_wrappers → foreign_data_wrapper_options, foreign_data_wrappers`;
`_pg_foreign_servers → foreign_server_options, foreign_servers, _pg_user_mappings`;
`_pg_foreign_tables → foreign_table_options, foreign_tables`;
`_pg_user_mappings → user_mapping_options, user_mappings`.

All 16 are edges among the information_schema views themselves — F32 already
established that **no** information_schema rule references a pg_catalog *view*
(they reference pg_catalog *catalogs*, which are sub-12000 bootstrap constants),
so M0133 is independent of the M0131-S9 view-on-view work.

## Wiring

The 65 (33 today) views must reach the on-disk catalogs **without** entering
`pg_internal.init` — upstream never nails information_schema relations. They ride
a **third list**, `informationSchemaViewSeedRels()`, exactly like the data tables'
`informationSchemaDataTableRels()`, wired at five sites plus the pg_rewrite heap:

1. `bootstrapPgClassTuples` — pg_class rows (relkind `'v'`, relhasrules true via
   `pgClassRow`'s existing view arm).
2. `bootstrapPgAttributeTuples` — pg_attribute descriptors (the domain-typed
   columns: `atttypid` 13292/13290/13300/… from the capture, `attlen` 64/-1).
3. `pgRewriteInitialEntries` — the `_RETURN` rules, appended to
   `nailedViewRewriteEntries()` so the 2692/2693 indexes cover them.
4. `bootstrapPgClassRelnameNspIndex` — the (relname, relnamespace) index, keyed
   under 13273 via `pgClassRelnamespaceFor` (extended to the view OIDs).
5. `pgClassRelnamespaceFor` — routes the 33 view OIDs to `infoSchemaNamespaceOID`
   (13273), the same path the data tables use.

The pg_type bootstrap needs no change: the views carry `RelType 2249`, already in
the entry map, so no composite rowtype is minted (the M0131-S6.5 divergence).

New artefacts:

- `internal/initdb/information_schema_view_oid_pins.go` — the Option-A pin table
  (`informationSchemaViewOIDPins()`, reusing `systemViewOIDPin`), captured from
  the same PG 18.3 oracle.
- `cmd/gen-information-schema-views/main.go` — renders
  `information_schema_view_manifest.tsv` into
  `internal/initdb/information_schema_view_seed_data.go`
  (`informationSchemaViewSeedRels()` + `informationSchemaViewRewriteEntries()`).
  A separate generator for the same reason `gen-information-schema-procs` is
  separate: disjoint namespace + list, one stdout stream each.
- `scripts/capture-ev-action.sh --information-schema` — captures
  `information_schema.<view>::regclass` into
  `information_schema_view_manifest.tsv` + `<view>_ev_action.dat`, parsing the
  new pin file. The `pg_catalog.${view}` literal became `${NSP_NAME}.${view}`.

## The forced fix: pg_rewrite_rel_rulename_index goes multi-page

The 2693 index (`pg_rewrite_rel_rulename_index`, `(ev_class, rulename)`) was
bulk-loaded with `pgBuildBtreeLeafRootPage` — a **single** leaf-root page. 80
pg_catalog rules fit (80×80 B ≈ 6400 B < 8152 B); adding 33 information_schema
rules pushed it to 113 tuples and overflowed the page at tuple 97. Switched to
`pgBuildBtreeBulkLoadSized(tuples, 80, 2)`, the same multi-page path
`pg_class_relname_nsp_index` already uses (metapage + N leaves + internal root).
The oid index 2692 (16-byte tuples) still fits one leaf-root (113 < 407) and is
unchanged.

## Guards

- `information_schema_view_oid_pins_test.go` — pin↔seed-row OID/RelNatts
  agreement, rule-OID agreement, band disjointness against `systemViewOIDPins`,
  and the RelType 2249 divergence (mirrors the S8a guards).
- `pinnedSystemViewOIDs()` extended to the information_schema pins, so the blob
  invariant guard (`TestEvActionBlobsCarryNoUnmappedInBandRelid`) covers the new
  corpus.
- `TestNailedViewEvActionBlobSetMatchesSeededViews` and
  `TestPgRewriteRuleOIDsMatchUpstreamPins` widened to the second pin table.
- `TestPgClassHeapBootstrapCoverage` / `TestBootstrappedViewsCarryRelhasrules` /
  `TestPgRewriteInitialEntriesContainsPgStatWalReceiverReturn` /
  `TestBootstrapPgRewriteLeafIndicesWriteBothFiles` — count/coverage checks
  updated for the +33 rows.
- E2E `assertInformationSchemaViewsEvaluable` — a hosted PG resolves each of the
  33 to its pinned OID and evaluates `SELECT * FROM … LIMIT 0`. The absence probe
  `assertNonCorpusSystemViewIsStillAbsent` is re-pointed from `tables` (now
  adopted) to `columns` (the largest remaining view, tranche 2/3).

## Remaining (later tranches)

Tranches 2–4 above. Tranche 2 (TOAST) needs no new mechanism — the 11 over-budget
values externalise through the existing `DECLARE_TOAST(pg_rewrite, 2838, 2839)`
chunk writer (`pgRewriteRowToasted` already collects chunks). Tranche 3 needs the
`_pg_*` funcids to resolve (S2's pinned helpers). Tranche 4 is ordered
base-before-dependent by capture guard #4; its base views (`attributes`,
`columns`, `domains`, `enabled_roles`, …) land in tranches 2–3, so each is pinned
before its dependents are captured.
