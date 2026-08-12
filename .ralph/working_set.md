(idle — nothing in flight)

M0131-S9.3f landed: `pg_seclabels` (12099) is on disk and evaluable on a hosted
PG. **Corpus 78 → 79 of upstream's 80.** The last instance of ceiling #4 is
closed; ONE view and ONE ceiling remain.

Landed: empty nailed catalogs `pg_seclabel` (3596, 5 cols, NO oid system
column) and `pg_largeobject_metadata` (2995, 3 cols) + 2995 in
`mappedLocalCatalogPlaceholderOIDs`; the pin; whole-list re-capture +
`nailed_view_seed_data.go` regen; **plus two catalog-DESCRIPTION repairs the
view forced** — pg_type 14 → 32 columns and pg_language 7 → 9. Design
`0131-0009` §S9.3f + F27/F28/F29. 2 ledger rows.

Worth carrying:
- **A view that JOINS a catalog audits that catalog's WHOLE description.**
  `expandRTE` → `get_rte_attribute_is_dropped` → `SearchSysCache2(ATTNUM)`
  (parse_relation.c:3414) resolves every column of every join input, not the
  selected ones. That is how on-disk pg_type was caught at 14 columns and
  MIS-NUMBERED past attnum 12 (typelem 13 / typarray 14 vs PG18's
  typsubscript / typelem / typarray at 13/14/15) — while `pgTypeRow` had been
  writing all 32 in upstream's order since M0106. Descriptor-vs-heap, not
  data.
- Widening a descriptor is only safe where the HEAP already holds the columns.
  pg_type did (fix = descriptor only); pg_language did NOT (fix = heap writer
  + descriptor in one commit, lanvalidator 2246/2247/2248 + NULL lanacl).
- The audit was VIEW-DRIVEN, not exhaustive — `pg_namespace` still describes 5
  columns where PG18 has 4 (ledgered, with a generated-guard resume point).
- Adding a base catalog is still TWO edits (`nailedLocalRels` row +
  `mappedLocalCatalogPlaceholderOIDs`); both break directions proven again.
- Re-capturing the whole 79-view list to add one view is ~60 s and doubles as
  `--verify`: all 78 other blobs came back byte-identical.
- Generator: `go run cmd/gen-nailed-view-tables/main.go > …seed_data.go`.
- Three expectation guards move with a capture: pg_type column count
  (initdb_test.go), `base/{1,5}/2838` page count, the toasted-rule set
  (pg_rewrite_toast_writer_test.go).

Gates: `internal/initdb` PASS (214 s), `^TestE2E_` family PASS (102 s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS + 2 scripted break directions, UNITS
PASS, `go build ./...` + `go vet` clean, pgbench smoke via the commit hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): ONE corpus view left.
`pg_stats_ext_exprs` (12063) is ceiling #6 — seed pg_type 10029 (pg_statistic's
composite rowtype) and make `pg_class(2619).reltype` carry it; note its current
failure trips `Assert("OidIsValid(typentry->typrelid)")` (typcache.c:3082) and
kills the backend, so probe it LAST in any run. Then S9.4
(`information_schema`, 65 views, expected to defer) — whose absence probe is
already in place.

In-flight: none.
