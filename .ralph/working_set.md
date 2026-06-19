(idle — nothing in flight)

Last landed: DU-002 slice 241 (loop #7) — `toast.vacuum_index_cleanup`
(RELOPT_KIND_HEAP|TOAST, the ONLY enum RELOPT_KIND_TOAST option; spellings
auto/on/off/true/false/yes/no/1/0 case-insensitive; stored VERBATIM trimmed so
`=on` round-trips as `=on`). Eighteen-element TOAST reloptions array. Mirrors
parent heap enum arm slice 217 (operators_ddl.go:1486-1515).

MILESTONE: with slice 241 the `toast.*` reloption surface is GENUINELY COMPLETE —
all 18 RELOPT_KIND_TOAST options (`grep -c RELOPT_KIND_TOAST reloptions.c` = 18)
now round-trip through pg_dump.

Files touched: internal/executor/operators_ddl.go (toast enum gather block after the
slice-240 toast.vacuum_max_eager_freeze_failure_rate arm, ~line 1872: switch over
nine spellings → 22023, store trimmed verbatim in toastReloptions),
internal/executor/operators_fillfactor_reloptions_test.go (2 new unit tests),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 18 options incl.
vacuum_index_cleanup=on + combined-WITH assertion x2 + array-literal comment),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 241 section), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor reloption unit tests PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.43s); pgbench
pre-commit smoke on commit.

Next: toast.* surface is DONE. Next DU-002 frontier = composite types
(`CREATE TYPE AS`; pg_class.reltype currently hardcoded 0) — a larger structural
task, not a one-arm slice. Scope it before coding.
