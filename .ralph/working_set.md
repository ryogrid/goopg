(idle — nothing in flight)

M0131-S9.2c landed and committed. The on-disk system-view corpus is 37 views;
S9 stays unchecked (S9.2 has 4 more under-ceiling heads, S9.3/S9.4 untouched).

Landed: `pg_shadow` (12005/12008) and `pg_user` (12014/12017) pinned in
`internal/initdb/system_view_oid_pins.go`, captured by ONE
`scripts/capture-ev-action.sh <37 views>` + ONE
`go run cmd/gen-nailed-view-tables/main.go > internal/initdb/nailed_view_seed_data.go`
(the generator has a `//go:build ignore` tag — invoke it by FILE PATH,
`go run ./cmd/...` fails with "build constraints exclude all Go files").
Probe set updated in `internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`.

Worth carrying:
- **The first view-on-view edge works, so S9.3 is scale, not feasibility.**
  `pg_user`'s blob carries `:relid 12005` — the corpus's first in-band relid
  after 35 views with none. Option-A identity pinning means it needs no
  rewriting, and a hosted PG chains two of goopg's own pg_rewrite rows to
  evaluate it. Capture guard #4 enforces base-before-dependent ordering.
- **F11 / ceiling #3: `pg_type.typarray` is seeded as a literal 0 for EVERY
  row** (`internal/initdb/pg_type_bootstrap.go:306`), though `_oid` (1028)
  exists and the column is in the tupledesc. That blocks `pg_group` (12010,
  1428 B, under every size ceiling) with "could not find array type for data
  type oid" via `get_array_type`. Ledgered; resume = add `Typarray`/`Typelem`
  to `pgTypeEntry` (`pg_type_bootstrap.go:61-79`) and populate from
  `pg_type.dat`. All THREE ceilings so far (pg_amop, typarray, pg_rewrite
  TOAST) are initdb-BOOTSTRAP gaps — the capture tooling has never been the
  limit.
- Remaining under-ceiling S9.2 heads (measured stored bytes): `pg_stat_database`
  2721, `pg_rules` 3774, `pg_policies` 5439, `pg_stat_all_tables` 5473 (the
  last is itself a view-on-view base, so likely S9.3).
- Editing hazard hit this loop: an Edit whose old_string spanned a comment
  block AND the pin lines silently dropped the pins; the capture then ran on
  35 views and looked "successful". Verify the pin count the script parses.

Gates: `internal/initdb` PASS (87 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
`^TestE2E_` family PASS (92 s), `--verify` PASS (37/37 byte-identical),
`go build ./...` + `go vet` clean, UNITS PASS, pgbench smoke via the hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): continue S9.2 with the four
under-ceiling views above. `pg_type.typarray` population is a well-scoped
alternative that unblocks `pg_group` and likely other ARRAY-shaped views.

In-flight: none.
