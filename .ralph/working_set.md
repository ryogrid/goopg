(idle — nothing in flight)

Last landed: DU-002 slice 132 (loop #97) — table→VIEW dependency-ordering
(topological emission) regression guard for pg_dump. NO production change:
empirically verified vs goopg's own pg_dump output that `CREATE TABLE public.foo`
is emitted BEFORE all three views that select from it (foo_view, foo_rview,
foo_mv). pg_restore replays top-to-bottom with no forward refs, so a view ahead
of its base table = unrestorable dump (`relation "public.foo" does not exist`).
Slices 57/58/60 only asserted view TEXT PRESENCE; this slice pins POSITION via
strings.Index offset comparison (table offset < each view offset). pg_dump
topo-sorts its TocEntry DAG from goopg's pg_depend / getDependencies edges.
Files: internal/testport/pgdump_connsetup_test.go (slice-132 positional assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 132), .ralph/fix_plan.md.
Verified: TestPort_PgDumpConnectionSetup PASS (2.07s).
Committed + pushed.

Next direction (slice 133): a partial-index predicate round-trip on a
constraint-backed path, OR a UNIQUE NULLS NOT DISTINCT constraint (NOTE: parser
has NO `NULLS NOT DISTINCT` support yet — would be a real feature, not a guard),
OR a multi-table FK dependency-ordering case (referenced table before
referencing table).
