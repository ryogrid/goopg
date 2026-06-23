Loop #23: M0118-0008 — allow_system_table_mods GUC enabler (design 0118-0065). COMMITTED? pending at loop end.

## What landed (enabler, NOT a promotion)
reindex-concurrently-toast.spec global setup runs `SET allow_system_table_mods TO true;`.
goopg didn't register the GUC ⇒ setup failed `unrecognized configuration parameter
"allow_system_table_mods" (22023)`, aborting permutation 0. Registered it mirroring PG
guc_tables.c: PGC_SUSET (→ContextSuset), bool, boot off, DEVELOPER_OPTIONS — following the
allow_in_place_tablespaces precedent (recognised developer no-op; goopg gates NO catalog
modification on it). Added mandatory `#allow_system_table_mods = off` to postgresql.conf.sample
(TestSampleConfigCoversRegistry requires every file-settable GUC there) + unit test.

Files: internal/config/defaults.go (register after allow_in_place_tablespaces),
internal/config/postgresql.conf.sample (DEVELOPER OPTIONS section),
internal/config/allow_system_table_mods_test.go (new),
docs/design/0118-0065-allow-system-table-mods-guc.md + README index, deferral_ledger.

Gates: TestAllowSystemTableModsGUC + full internal/config (incl. sample parity) PASS;
go vet ./internal/config clean; go build ./... clean; live probe confirms reind-con-toast
setup divergence advanced GUC-error → PL/pgSQL `qualified names not supported in expressions`
(r.table_name in EXECUTE). pgbench smoke = pre-commit hook.

## Probe ranking of the M0118-0008 hard tail (this loop, throwaway zz_probe deleted)
- reindex-concurrently-toast: was GUC error; now PL/pgSQL qualified-name in EXECUTE → then
  FUNDAMENTAL: needs real TOAST relations as catalog objects (goopg stores text/bytea INLINE,
  reltoastrelid=0, nothing to rename/REINDEX CONCURRENTLY). Multi-loop subsystem.
- partition-concurrent-attach: first div L7 — INSERT into default partition must <waiting>
  behind concurrent uncommitted ATTACH then ERROR partition-constraint after commit; needs
  default-partition implicit constraint (complement of sibling bounds) + lock + re-validate.
- alter-table-4: first div L6 — SELECT SUM(a) FROM p must <waiting> behind uncommitted
  ALTER…NO INHERIT/INHERIT then still see old children (sum 11). COUPLED to transactional
  DDL catalog visibility (goopg applies DDL to shared in-mem catalog non-transactionally,
  visible cross-session immediately). Milestone-sized MVCC catalog subsystem.
- partition-drop-index-locking: needs full pg_locks view population (returns 0 rows today).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf) but no executor site; needs
  per-row CTID capture in cursor + CTID-restricted rewrite. Project-wide.

Next step: commit + push this enabler. Then pick next M0118-0008 tail slice — all remaining
are Effort-L distinct subsystems; partition-drop-index-locking (pg_locks) is broadly reusable.
