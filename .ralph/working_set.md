Loop #20: M0118-0008 — `reindex-concurrently-toast` (LAST unpromoted spec; other
24 pass strict). Landed TOAST-exposure epic SLICE 2 (design 0118-0085). Spec
stays `defer` (steps 3–5 of the 5-step epic remain).

## What landed (slice 2 of N)
`reltoastrelid::regclass` now renders the schema-qualified `pg_toast.pg_toast_<oid>`
name (PG regclassout always qualifies pg_toast — never in search_path) instead of
the numeric OID. The synthetic toast pg_class row lives only in the virtual
builder output, not c.tables, so the regclass cast couldn't name it via tableByOID.
- NEW `catalog.tableHasToastRelation(t)` — extracts the slice-1 hasToastRel gate
  into ONE shared function (builder gate + OID→name resolver can't diverge).
- NEW `(*InMemory).ToastRelName(oid)` — for oid >= toastRelidOffset (100_000_000)
  whose parent table still owns an auto-exposed toast rel, returns
  `pg_toast.pg_toast_<parentOID>`, true; else false.
- `expr.go` CastExpr regclass arm: after LookupTableByOID miss, fall through to
  `im.ToastRelName(...)`. InvalidOid→"-" guard (0118-0083) still runs first; a
  non-zero unmatched OID still renders numeric.

Files: internal/catalog/catalog.go (tableHasToastRelation, ToastRelName, builder
gate at ~L3089 now calls helper); internal/executor/expr.go (regclass arm ~L599);
internal/executor/toast_relation_exposure_test.go (NEW TestReltoastrelidRegclassRendersToastName);
docs/design/0118-0085-*.md + README; deferral ledger.

## Next step (slice 3)
Synthesize the TOAST index `pg_toast_<oid>_index` in pg_index AND pg_class
(relkind='i') so the spec's `SELECT indexrelid::regclass::text FROM pg_index
WHERE indrelid = (SELECT oid FROM pg_class WHERE relname='reind_con_toast')`
resolves. Then slice 4 (pg_toast RENAME under allow_system_table_mods +
pg_toast.<name> resolution), slice 5 (REINDEX CONCURRENTLY pg_toast.<name>
routing, rides 0118-0029). Re-run blast-radius parity suites after each
(pg_index/pg_class enumeration feeds pg_dump/pg_amcheck/\d).

## Gates run (this loop)
go test ./internal/{catalog,executor}/ PASS; TestReltoastrelidRegclassRendersToastName
+ TestToastRelationAutoExposed + TestRegclassInvalidOidRendersDash PASS; ALL
blast-radius parity suites PASS (PgDumpConnectionSetup, PgDump*, PgAmcheck* incl.
alltables/002 whole-DB walks, Scripts*); IsolationPlpgsqlToast +
IsolationReindexConcurrently PASS; build/gofmt/vet clean; make ralph-state-guard
OK (auto-repaired prev-loop completed marker); pgbench smoke = pre-commit hook.
