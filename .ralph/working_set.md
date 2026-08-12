(idle — nothing in flight)

M0131-S9.3e landed: `pg_policy` (3256) bootstrapped as an EMPTY on-disk nailed
catalog, closing corpus ceiling #4. `pg_policies` (12018) pinned and evaluable
on a hosted PG. **Corpus 77 → 78 of upstream's 80.**

Landed: `pgPolicyAttrs()` (8 cols, from `postgres/src/include/catalog/pg_policy.h`)
+ `nailedLocalRels` row in `internal/initdb/relcache_init.go`; `3256` in
`mappedLocalCatalogPlaceholderOIDs` (`initdb.go`) for the empty 8 KiB heap in
base/{1,5}; the pin in `system_view_oid_pins.go`; whole-list re-capture +
`nailed_view_seed_data.go` regen; guards in
`internal/initdb/pg_policy_nailed_test.go`;
`assertNonCorpusSystemViewIsStillAbsent` re-pointed to `pg_seclabels`.
Design `0131-0009` §S9.3e + F25/F26. 2 ledger rows.

Worth carrying:
- **Adding a base catalog is TWO edits, both load-bearing**: the `nailedLocalRels`
  row (pg_class + pg_attribute) AND `mappedLocalCatalogPlaceholderOIDs` (the
  physical file). Each break direction was proven by scripted revert.
- Re-capturing the WHOLE pinned list to add one view is cheap (~60 s) and
  doubles as `--verify`: all 77 other blobs came back byte-identical.
- An "is it still absent?" probe must fail QUIETLY — `pg_stats_ext_exprs`
  aborts the backend (typcache.c:3082 Assert), so `pg_seclabels` is the only
  safe target left (F26).
- Generator is `go run cmd/gen-nailed-view-tables/main.go > …seed_data.go`
  (the package has `//go:build ignore`, so `go run ./cmd/...` fails).
- pg_class payload offsets used by the on-disk guards: relkind 119,
  relnatts 120, relchecks 122, relhasrules 124.

Gates: `internal/initdb` PASS (187 s), `^TestE2E_` family PASS (99 s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS (-count=1), UNITS PASS, `go build
./...` + `go vet` clean, pgbench smoke via the commit hook, 2 break directions
proven fail-when-broken.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): TWO corpus views left.
`pg_seclabels` (12099) is mechanically S9.3e twice over — bootstrap pg_seclabel
(3596) and pg_largeobject_metadata (2995) as empty nailed catalogs, then pin;
its 35379 B blob already toasts. `pg_stats_ext_exprs` (12063) is ceiling #6:
seed pg_type 10029 (pg_statistic's composite rowtype) and make
pg_class(2619).reltype carry it.

In-flight: none.
