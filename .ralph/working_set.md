(idle — nothing in flight)

M0131-S9.3g landed: `pg_stats_ext_exprs` (12063) is on disk and evaluable on a
hosted PG. **Corpus 79 → 80 of upstream's 80. NO pg_catalog ceilings remain.**

Landed: `pg_type` row for 10029 (pg_statistic's composite rowtype), the new
`pgTypeRelidOverlay`, `nailedLocalRels{2619}.RelType` 83 → 10029, the pin +
whole-80 re-capture + `nailed_view_seed_data.go` regen; **plus F30**, which was
the slice's real content. Design `0131-0009` §S9.3g. 2 ledger rows. New guard
`TestPgTypeCompositeRowsCarryTyprelid`.

Worth carrying:
- **`typtype='c'` is a PROMISE about `typrelid`, not a label.**
  `insert_rel_type_cache_if_needed` asserts `OidIsValid(typentry->typrelid)`
  (typcache.c:3082) — a composite row with typrelid 0 KILLS the backend, it
  does not error. goopg had FIVE: `_pg_statistic` (10028) typed `'c'` when
  upstream types an array-of-composite `'b'`, plus BKI_ROWTYPE_OID rows
  71/75/81/83 seeded with typrelid 0 since M0106 (latent — nothing had yet
  type-cached a catalog rowtype as a VALUE).
- The 10028 defect was guarded by a COMMENT scoping its own correctness to one
  code path ("carries no special meaning for the standby's TupleDescInitEntry
  path"). Twin of F27: a field is read by every consumer, not the one in mind.
- Coupling two catalog halves by construction beats coupling by comment:
  `pgTypeBootstrapEntryMap()` derives part of its OID set from
  `nailedRel.RelType`, so reverting pg_class deletes the pg_type row too
  (proven — break 2 reproduced break 1's error).
- Whole-corpus re-capture is ~6 s and doubles as `--verify`: all 79 incumbent
  blobs byte-identical. `scripts/capture-ev-action.sh $(view list)` then
  `go run cmd/gen-nailed-view-tables/main.go > …seed_data.go` (note: `//go:build
  ignore`, so `go run <path>` not `go run ./cmd/...`).
- Three expectation guards move with every capture: the toasted-rule set
  (`pg_rewrite_toast_writer_test.go`), `base/{1,5}/2838` page count, and the
  hosted-PG chunk list in the S4 E2E.
- Oracle recipe when no PG is up: `initdb` a temp dir, `pg_ctl -o "-p <port>
  -k $D -h ''"` (bare `-k` alone still binds TCP and collides).

Gates: `internal/initdb` PASS (226 s), `^TestE2E_` family PASS (106 s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS + 2 scripted break directions, UNITS
PASS, `go build ./...` + `go vet` clean, pgbench smoke via the commit hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): S9.4 —
`information_schema` (65 views), expected to DEFER with a ledger row. Its
first step is the complement query S9.3d used (F19); its fail-when-fixed
tripwire (`information_schema.tables`) is already installed by F29. Needs the
namespace + its domains (`sql_identifier`, `cardinal_number`, …) + helper
functions on disk first. Off that path: the S9.3g ledger row on the nine
header-declared `BKI_ROWTYPE_OID` rowtypes goopg never seeds.

In-flight: none.
