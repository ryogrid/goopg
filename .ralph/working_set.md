Loop #19: M0118-0008 — `reindex-concurrently-toast` (LAST unpromoted spec; other
24 pass strict). Landed TOAST-exposure epic SLICE 1 (design 0118-0084). Spec
stays `defer` (steps 2–5 of the 5-step epic remain).

## What landed (slice 1 of N)
The pg_class virtual builder now auto-exposes a TOAST relation for USER tables
with a toastable column — previously gated only on explicit `toast.*` reloptions.
New `tableNeedsToastRelation(t)` (mirrors PG needs_toast_table) + `columnTypeIsToastable(col)`
(varlena type-name set synced with executor isToastableType, plus array cols).
Gate: `hasToastRel := len(ToastReloptions)>0 || (!IsSystemRelation(OID) &&
relkind IN ('r','m') && tableNeedsToastRelation(t))`. Parent reltoastrelid →
OID+100_000_000; toast-row reloptions NULL unless explicit toast.* set.

## Blast-radius decision (by measurement — the key finding)
USER relations only. First un-scoped attempt attached reltoastrelid to virtual
system catalogs (pg_type=1247, pg_attribute=1249); pg_amcheck's whole-DB walk
FOLLOWS reltoastrelid → `could not open relation` on pg_toast_1247/1249 (goopg
has no real heap there). `!IsSystemRelation(t.OID)` fixes it AND preserves prior
DU-002 slice-224 behaviour (explicit toast.* only ever on user tables).

Files: internal/catalog/catalog.go (2 helpers + both gate sites ~L795, L3000, L3066);
internal/executor/toast_relation_exposure_test.go (NEW TestToastRelationAutoExposed);
docs/design/0118-0084-*.md + README; deferral ledger.

## Next step (slice 2)
Resolve the toast OID→name in `LookupTableByOID`/`tableByOID` so
`reltoastrelid::regclass` renders `pg_toast.pg_toast_<oid>` instead of the numeric
OID (the synthetic row lives only in the virtual pg_class builder, not c.tables —
regclassout consults tableByOID). Re-run pgdump_connsetup + pgamcheck after, since
naming the toast relation may change \d/regclass output. Then slices 3 (toast
index in pg_index), 4 (pg_toast RENAME under allow_system_table_mods), 5 (REINDEX
CONCURRENTLY pg_toast.<name> routing, rides 0118-0029).

## Gates run (this loop)
go test ./internal/{catalog,executor}/ PASS; TestToastRelationAutoExposed PASS;
ALL blast-radius parity suites PASS (PgDumpConnectionSetup, PgDump*, PgAmcheck*
incl. alltables/002 whole-DB walks, Scripts*); IsolationPlpgsqlToast +
IsolationReindexConcurrently PASS; build/gofmt/vet clean; make ralph-state-guard
(below); pgbench smoke = pre-commit hook.
