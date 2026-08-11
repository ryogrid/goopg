(idle — nothing in flight)

Loop #120 landed **M0131-S9.1b**: the on-disk system-view corpus is **30 views**
(`pg_stat_bgwriter` 12293, `pg_stat_checkpointer` 12297 added), both evaluate on
a real PG 18.3 hosted on a goopg `$PGDATA`, and design 0131-0009's guard #2 now
has BOTH halves. One capture run + one generator run, zero hand-edits.

Carry-forward for the next loop:

- **Next per the banner: M0131-S12** — bulk-load `pg_opclass_am_name_nsp_index`
  (2686) at initdb. It is the same failure shape as the `pg_amop` 2653 blocker
  that keeps `pg_timezone_abbrevs` off disk and blocks EVERY future system view
  with an `ORDER BY`, so landing it before S9.2 pays twice. Subtasks S12.1–S12.5
  are spelled out in fix_plan; note S12.5 INVERTS
  `assertEmptyOpclassIndexStillBlocksSorts`.
- **`assertNonCorpusSystemViewIsStillAbsent` is fail-when-fixed on
  `pg_catalog.pg_tables`** — the moment S9.2 adopts pg_tables that assertion
  goes red on purpose; re-point it at the next un-adopted view, don't delete it.
- **F4 correction worth remembering:** `RTE_RESULT` is a PLANNER construct and
  never appears in `pg_rewrite.ev_action`. A FROM-less view serialises an empty
  `:rtable`/`:fromlist`. The still-unmeasured shape is `LATERAL`
  (`pg_statio_all_tables`, `pg_stats_ext`).
- **F6, ledgered not fixed:** goopg's virtual `pg_stat_checkpointer`
  (`internal/initdb/open.go:2625-2646`) omits `num_done`, adds `total_time`, and
  types all 11 columns `text`. Same COUNT as upstream, different SET — a
  count-only audit passes. Resume point is in the ledger row; a table-driven
  `nailedViewSeedAttrs(name)` vs virtual-`Columns` test would audit the whole
  corpus at once.

Procedure reminder for any corpus widening: add pins to
`internal/initdb/system_view_oid_pins.go`, then
`scripts/capture-ev-action.sh $(sed -n 's/^\t\t{"\([a-z_]*\)".*/\1/p'
internal/initdb/system_view_oid_pins.go)` (the script REWRITES the whole
manifest — always pass the FULL list), then
`go run cmd/gen-nailed-view-tables/main.go > internal/initdb/nailed_view_seed_data.go`,
then add the view to `nailedSystemViewProbeSet()` in
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`.

Gates run this loop: `internal/initdb` PASS (64 s), `^TestE2E_` family PASS
(100 s), `capture-ev-action.sh --verify` PASS (30/30 byte-identical), UNITS
PASS, pgbench smoke via the commit hook, `make ralph-state-guard` OK.

In-flight: none.
