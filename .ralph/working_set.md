(idle — nothing in flight)

M0131-S9.3c landed and committed. The on-disk system-view corpus is 60 views;
S9 stays unchecked (three ceilings left, all initdb-bootstrap gaps).

Landed: `pg_type.typsubscript`/`typelem`/`typarray` populated for ALL 193
bootstrapped rows — `cmd/gen-pg-type-data` now derives the triple from
`pg_type.dat` (genbki rules) and emits `pgTypeGeneratedElemArraySubscript` into
`internal/initdb/pg_type_seed_data.go`; `pgTypeRow` reads it via
`pgTypeElemArraySubscriptForOID` with a 1-entry hand overlay
(`pgTypeElemArrayOverlay`, `_pg_statistic` 10028). That discharged ceilings #3
and #5, so `pg_group` (12010/12013) and `pg_publication_tables` (12068/12071)
are pinned + captured (ONE 60-view `scripts/capture-ev-action.sh` + ONE
`cmd/gen-nailed-view-tables` run; the other 58 blobs byte-identical).

Worth carrying:
- **F16 — populate a PG catalog column group ATOMICALLY.** typelem+typarray
  without typsubscript passed every unit test, `--verify` and the encoded-byte
  pins, and BROKE the S4 E2E's `IN ('public','s4app')` guard: `typarray = 0`
  makes `transformAExprIn` fall back to an OR-chain (always works); the moment
  typarray resolves, `IsTrueArrayType` also demands
  `typsubscript == array_subscript_handler` (6179; raw_ variant is 6180).
- **`pg_rewrite` TOAST (2838/2839) is still the critical path** for S9 — gates
  `pg_indexes` (9002 B), the `pg_statio_*_tables` triple (base 12174, 10475 B)
  and every future base capture (F15: only bases approach the 8000 B budget).
  Other two ceilings: `pg_policy` not an on-disk relation (`pg_policies`), and
  the `pg_amop` text_lt row (`pg_timezone_abbrevs`).
- Oracle recipe used this loop: throwaway `initdb` (real PG) on a unix socket
  under the datadir to read canonical catalog values; and `goopg init -D` +
  `pg_ctl` hosting to measure a real PG on goopg's dir. Port 5601 free.
  Careful: a direct `SELECT … FROM pg_type` on a hosted assert-enabled PG
  TRAPs (initsplan.c:301 attno assert) — probe via `format_type()`/view reads
  instead.
- Editing hazard (still live): the pin parser in `capture-ev-action.sh` anchors
  on `},$`, so a TRAILING COMMENT on a pin line silently drops that pin.

Gates: `internal/initdb` PASS (104 s), `^TestE2E_` family PASS (96 s, incl.
`TestE2E_PGColdStartOnGoopgDataDir`), `--verify` PASS (60/60 byte-identical),
hosted-PG probe PASS, `go build ./...` + `go vet` clean, UNITS PASS, pgbench
smoke via the hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): the `pg_rewrite` TOAST slice
(unscoped — needs a design doc), or one of the two smaller ceilings.

In-flight: none.
