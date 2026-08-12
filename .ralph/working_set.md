(idle — nothing in flight)

M0131-S9.2b landed and committed. The on-disk system-view corpus is 35 views;
S9 stays unchecked (S9.2 has 7 more under-ceiling heads, S9.3/S9.4 untouched).

Landed: `pg_roles` (12000/12003) and `pg_stat_activity` (12226/12229) pinned in
`internal/initdb/system_view_oid_pins.go`, captured by ONE
`scripts/capture-ev-action.sh <35 views>` + ONE
`go run cmd/gen-nailed-view-tables/main.go > internal/initdb/nailed_view_seed_data.go`
(the generator writes to STDOUT — redirect it). Probe set updated in
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`; guard #2 needed NO
re-point this time (its subject `pg_indexes` is blocked for a measured reason).

Worth carrying:
- **The TOAST ceiling is a class, not an exception.** Sized on a throwaway PG
  before pinning: `pg_seclabels` stores **35379 B** (raw 203378 B) — 3.9× the
  `pg_indexes` 9002 B breach. Design `DECLARE_TOAST(pg_rewrite, 2838, 2839)`
  for multi-chunk external storage, not one overflow chunk.
- **Adoptable NOW without TOAST** (all measured, stored bytes): `pg_user` 1356,
  `pg_group` 1428, `pg_shadow` 2015, `pg_stat_database` 2721, `pg_rules` 3774,
  `pg_policies` 5439, `pg_stat_all_tables` 5473. Caveat: `pg_stat_all_tables`
  is the base of view-on-view edges, so it may belong to S9.3.
- Cheap pre-pin sizing query (stored size, NOT `length(::text)` — they differ
  ~8x): on a throwaway PG 18.3, `SELECT c.relname, pg_column_size(r.ev_action)
  FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid AND
  r.rulename='_RETURN' WHERE c.relname IN (…)`.
- **F10, the sharpest dual-definition finding yet:** goopg's virtual
  `pg_stat_activity` lives at OID **16403**, inside the `FirstUserOID = 16384`
  band — a system view minted by the runtime USER-relation allocator, so its
  OID is unstable per-cluster and can collide with a user relation. Virtual
  `pg_roles` is 4 cols under 1259102 vs 13 under 12000. Both ledgered.
- Still ZERO in-band `:relid` in every blob, so S9.3 remains the first slice
  that actually needs view-on-view ordering.

Gates: `internal/initdb` PASS (93 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
`^TestE2E_` family PASS (90 s), `--verify` PASS (35/35 byte-identical),
`go build ./...` + `go vet` clean, UNITS PASS, pgbench smoke via the hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): continue S9.2 with the seven
under-ceiling views above (start with the small `pg_user`/`pg_group`/`pg_shadow`
authid family — same shared-catalog shape S9.2b just proved works). Then S21
(re-probe its real remaining gap first) and S24.

In-flight: none.
