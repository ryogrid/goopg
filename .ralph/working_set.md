(idle — nothing in flight)

M0131-S9.3d landed and committed. The on-disk system-view corpus is **71 of
upstream's 80 `pg_catalog` views**; S9 stays unchecked (two ceilings left, both
initdb-bootstrap gaps, and they account for exactly the nine missing views).

Landed: eleven views pinned + captured in ONE 71-view
`scripts/capture-ev-action.sh` run + ONE `cmd/gen-nailed-view-tables` run
(other 60 blobs byte-identical): `pg_available_extensions` 12081,
`pg_available_extension_versions` 12085, `pg_timezone_abbrevs` 12122,
`pg_stat_user_functions` 12279, `pg_stat_xact_user_functions` 12284,
`pg_stat_progress_{analyze,vacuum,cluster,create_index,copy}`
12309/12314/12319/12324/12333, `pg_stat_subscription_stats` 12347.

Worth carrying:
- **The selection method is the finding (F19).** Do NOT pick the next tranche
  by subject family. Ask the oracle for every `pg_catalog` view with a
  `_RETURN` rule (oid, rule oid, reltype, relnatts,
  `pg_column_size(ev_action)`, in-band `:relid` via
  `regexp_matches(ev_action::text, ':relid (1[2-9][0-9]{3})','g')`) and diff
  against `systemViewOIDPins()`. That one query found eleven views blocked by
  nothing at all.
- **F18 — re-measure a recorded ceiling before believing it.**
  `pg_timezone_abbrevs` sat on the blocked list through five tranches after
  M0131-S12 had already fixed it (2653/2654 bulk-load). No guard re-probes a
  ceiling; only `pg_indexes` has a fail-when-fixed assertion.
- Remaining nine, a closed list: **eight** behind `DECLARE_TOAST(pg_rewrite,
  2838, 2839)` — `pg_indexes` 9002 B, `pg_stats` 9316 B, `pg_stats_ext`
  12196 B, `pg_stats_ext_exprs` 11481 B, `pg_seclabels` 35379 B (multi-chunk),
  `pg_statio_all_tables` 10475 B + `pg_statio_{sys,user}_tables` under guard
  #4 — and `pg_policies` behind an on-disk `pg_policy` (3256).
- Non-vacuity recipe used this loop: add un-seeded `pg_indexes` (12043) to
  `nailedSystemViewProbeSet` → `TestE2E_PGColdStartOnGoopgDataDir` FAILS in
  ~2 s. That test is genuinely ~2.5 s; short runtime is not a skip.
- Editing hazard (still live): the pin parser in `capture-ev-action.sh` anchors
  on `},$`, so a TRAILING COMMENT on a pin line silently drops that pin.

Gates: `internal/initdb` PASS (111 s), `^TestE2E_` family PASS (96 s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS + deliberate fail-when-broken run,
`--verify` PASS (71/71 byte-identical), `go build ./...` + `go vet` clean,
UNITS PASS, pgbench smoke via the hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): the `pg_rewrite` TOAST slice
(unscoped — needs a design doc; must be multi-chunk), or `pg_policy` as an
on-disk relation.

In-flight: none.
