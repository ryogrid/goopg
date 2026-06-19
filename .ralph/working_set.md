(idle — nothing in flight)

Last landed: DU-002 slice 224 (loop #39) — first `toast.*` namespace reloption
(`toast.autovacuum_enabled`) round-trips through pg_dump; first synthesized TOAST
pg_class row. Opens the `toast.*` namespace flagged by slice 223 as bigger work.

What happened: PG keeps `toast.`-prefixed reloptions on the table's TOAST relation's
pg_class.reloptions (WITHOUT the prefix); pg_dump re-adds the prefix via the
reltoastrelid join (LEFT JOIN pg_class tc ON c.reltoastrelid=tc.oid AND tc.relkind='t').
Closed two gaps: (a) parser combines the dotted WITH key, (b) goopg models a synthetic
TOAST relation. Parser (parseWithOptions): a bare `.` after the key label triggers
consuming a second label → one dotted map key. Executor: gathers toast.* keys into
catalog.Table.ToastReloptions (normalized `autovacuum_enabled=false`, no prefix);
validates the bool (non-bool→22023). catalog: new Table.ToastReloptions []string;
when non-empty the pg_class view sets parent reltoastrelid=OID+100_000_000 AND emits
an extra relkind='t' row `pg_toast_<oid>` (ns 99) carrying the reloptions. getTables
WHERE excludes 't', so the TOAST row is join-target-only — never dumped.

Files: internal/parser/ddl.go (+ddl_test.go), internal/executor/operators_ddl.go
(+operators_fillfactor_reloptions_test.go), internal/catalog/catalog.go,
internal/testport/pgdump_connsetup_test.go (NEW optoast fixture+assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 224), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; parser+catalog+full-executor PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: the TOAST-row machinery now exists, so the rest of the RELOPT_KIND_TOAST-capable
options become one-line gather additions: toast.autovacuum_vacuum_threshold,
toast.autovacuum_vacuum_scale_factor, toast.autovacuum_vacuum_cost_delay,
toast.autovacuum_vacuum_cost_limit, toast.autovacuum_freeze_*_age, toast.log_autovacuum_min_duration,
toast.vacuum_truncate, toast.autovacuum_enabled (done). Mirror slice 224's gather +
add a fixture/assertion each. After that: composite types.
