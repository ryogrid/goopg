(idle — nothing in flight)

Loop #35: LANDED INSERT…SELECT crash fix (design 0118-0038, M0118-0002/0008 enabler,
NOT a spec promotion). `INSERT INTO t SELECT a,b` with no explicit column list +
fewer source columns than the table panicked the backend (`index out of range` in
`insertOp.Next`, operators_storage.go:1187 — ColumnIndex=[0,1,2] indexed a 2-col
row). Fixed in `planInsert` (planner.go ~L7113): reconcile source arity with
colIndex — over-wide→42601, explicit-list under-wide→42601, implicit-list
under-wide→truncate colIndex so executor applyDefaultsForMissing fills the rest.
Type-independent; VALUES path unaffected. Units TestPlanInsertSelect{FewerColumns…,
MoreColumnsErrors,ExplicitListArityMismatchErrors}; e2e default-fill verified;
planner+executor suites + pgbench smoke (0-failed, -S ~14.3k TPS) green.

Probed ALL 16 remaining M0118-0008 specs this loop (ranking table in design
0118-0038). NONE is a single-loop promotion — each needs a milestone-sized
subsystem: transactional DDL catalog visibility (alter-table-1/2/4), role/ACL
privilege enforcement (truncate/vacuum/cluster-conflict), real TOAST relations
(reindex-concurrently-toast, plpgsql-toast), partition CONCURRENTLY +
ATTACH/DETACH (detach-*, partition-concurrent-attach), pg_locks population +
recursive partition lock (partition-drop-index-locking), reltuples accounting +
buffer-pin cleanup-lock (vacuum-no-cleanup-lock).

Adjacent bug found (NOT fixed): dispatch.go ~L131 tryHandleRoleDDL swallows an
entire simple-query batch `CREATE ROLE x; CREATE TABLE y;` and drops the
CREATE TABLE — why the *-conflict setups leave the table missing. Needs
statement-splitting in the parser-fail recovery path.

Next step: pick the next M0118 spec needing a bounded mechanism, OR start one of
the milestone-sized subsystems above (role/ACL enforcement unblocks 4 *-conflict
specs at once and is broadly useful).
