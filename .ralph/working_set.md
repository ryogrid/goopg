(idle — nothing in flight)

## Loop summary (2026-07-12, loop #68)

**Nightly triage:** action-items batch `20260711-011536` (same as #58–#67) —
all 3 AI items already `[x]` in M-NIGHTLY. No new nightly work.

**Task — M0122-0007 (object-creation default GUC stubs).** pg_dump/pg_restore
emit a fixed SET preamble before every CREATE TABLE (`SET default_tablespace =
'';`, `SET default_table_access_method = heap;`, and `SET
default_toast_compression = 'pglz';` for non-default column compression) — but
none of these three CLIENT_CONN_STATEMENT/PGC_USERSET GUCs were registered, so
replaying a real-PG dump aborted with "unrecognized configuration parameter".
Registered all three as accepted stubs.

Landed (files):
- internal/config/defaults.go: 3 GUCs (default_table_access_method string/heap,
  default_tablespace string/'', default_toast_compression enum {pglz,lz4}/pglz).
- internal/config/postgresql.conf.sample: 3 entries (TestSampleConfigCoversRegistry).
- internal/catalog/catalog.go: 3 pg_settings literal rows (winning VirtualRows list).
- internal/config/object_default_guc_stubs_test.go (new); catalog_test.go
  TestPgSettingsObjectDefaultGUCs (new).
- docs/design/root-0004-configuration-and-guc.md new section; .ralph/fix_plan.md
  done-note under M0122-0007; deferral_ledger.md row (behavioral no-op:
  goopg ignores the values at CREATE TABLE — heap-only AM, no tablespaces,
  built-in TOAST default).

Gates: go build ./... clean; go vet config+catalog clean; config+catalog tests
PASS; end-to-end wire smoke vs cmd/goopg (all 3 SETs succeed, SHOW/pg_settings
report, ='lz4' moves, 'zstd' rejected); ralph-state-guard consistent (repaired
prev clean-exit marker); pgbench smoke via pre-commit hook (see commit).

Next-loop candidates (genuinely open, larger):
- M0122-0006 On-disk catalog persistence (persistent pg_index heap).
- ALTER DOMAIN SET SCHEMA — BLOCKED: domains (and all user types: enum/composite)
  hardcode typnamespace=public; needs broad "namespace-scope user types" feature,
  NOT a one-loop task. Avoid until that infra exists.
- More missing GUC families (autovacuum ~22, log_* ~40, vacuum cost) — bounded
  per-family, low value/risk.

In-flight: none
