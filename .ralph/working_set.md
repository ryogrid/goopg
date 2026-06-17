(idle — nothing in flight)

Last landed: DU-002 slice 170 (loop #137) — legacy table inheritance
(`CREATE TABLE child (...) INHERITS (parent)`) now round-trips through pg_dump.
REAL DIVERGENCE FIXED.

Root cause (two lost dump signals): (1) `pg_inherits` VirtualRows emitted rows
ONLY for partition children (`PartitionParentOID != 0`), so a legacy inheritance
edge produced no row → pg_dump dropped the `INHERITS (...)` clause. (2) The
INHERITS branch of execCreateTable left inherited columns `attislocal=true`
(`Column.Inherited` never set, unlike the PARTITION OF path) → pg_dump re-emitted
the parent's columns inline. Net: child structurally different + columns doubly
defined on restore.

Fix: `catalog.Table.InheritsParentOIDs []uint32` (ordered direct parents),
populated in execCreateTable's INHERITS branch; that branch also marks each
purely-inherited column (in a parent, not locally redeclared) `Inherited=true`.
`pg_inherits.VirtualRows` emits one (child,parent) row per entry, inhseqno =
declaration order, mutually exclusive with the partition-child branch. Routing +
the existing inheritanceChildren map untouched.

Files: internal/catalog/catalog.go (field + pg_inherits VirtualRows),
internal/executor/operators_ddl.go (populate + mark cols),
internal/catalog/catalog_test.go (TestPgInheritsEmitsLegacyInheritanceRows),
internal/testport/pgdump_connsetup_test.go (inh_parent/inh_child fixture +
assertions), docs/design/0110-0001-pg-dump-tap-port.md (slice 170),
.ralph/fix_plan.md (#137).
Gates: gofmt OK; go build ./internal/... OK; go vet ./internal/testport/ clean;
TestPgInheritsEmitsLegacyInheritanceRows PASS; TestPort_PgDumpConnectionSetup
PASS (2.81s, not skipped); catalog + full executor suites PASS; pgbench
pre-commit smoke on commit.

Next (slice 171 candidates): (1) dedicated MINVALUE/MAXVALUE keyword-AST-node
(parser collapses keyword vs literal `'MINVALUE'`, affects routing — latent).
(2) multi-level partition trees. (3) multi-parent inheritance + inherited-CHECK
(conislocal=false) dump fidelity. (4) column-level STORAGE/COMPRESSION (needs
parser keywords). See ledger.
