(idle — nothing in flight)

Last landed: DU-002 slice 240 (loop #6) — `toast.vacuum_max_eager_freeze_failure_rate`
(RELOPT_KIND_HEAP|TOAST, REAL range **0.0–1.0** — page fraction, NOT 0.0–100.0; default -1),
seventeen-element TOAST reloptions array. Mirrors parent heap arm slice 216.

IMPORTANT CORRECTION: the slice-239 working-set claim that the `toast.*` surface was
COMPLETE was WRONG. PG has **18** `RELOPT_KIND_TOAST` options (`grep -c RELOPT_KIND_TOAST
postgres/.../reloptions.c`), goopg covered 16. Slice 240 added the 17th (this real one).
The 18th and last is `toast.vacuum_index_cleanup` (ENUM auto/on/off; heap arm = slice 217,
operators_ddl.go:1486-1515) — that is the NEXT slice (241). After that the toast.* surface
is genuinely complete → composite types (`CREATE TYPE AS`; pg_class.reltype hardcoded 0).

Files: internal/executor/operators_ddl.go (toast real gather block after the slice-239
toast.autovacuum_vacuum_insert_scale_factor arm; ParseFloat + !(f>=0 && f<=1) → 22023),
internal/executor/operators_fillfactor_reloptions_test.go (2 new unit tests),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 17 options incl.
vacuum_max_eager_freeze_failure_rate=0.5 + combined-WITH assertion x2),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 240 + correction note), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor reloption unit tests PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.46s); pgbench pre-commit smoke on commit.

Next: slice 241 = `toast.vacuum_index_cleanup` (enum) — copy slice-217 heap enum validation
(switch over auto/on/off/true/false/yes/no/1/0), store VERBATIM trimmed in toastReloptions,
extend optoast fixture to 18 options. Then composite types.
