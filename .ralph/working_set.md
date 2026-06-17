(idle — nothing in flight)

Last landed: DU-002 slice 171 (loop #138) — multi-level (sub-partitioned)
partition tree round-trips through pg_dump. CLEAN POSITIVE (verified, no fix
needed): pinned the only relation that is simultaneously relispartition=true AND
relkind='p' (a partition that is itself partitioned) as a regression guard.

Why it already works: buildUserPGClassRow (+ catalog.go VirtualRows sibling)
derives relkind='p' from PartitionMethod regardless of isPartition, and sets
relpartbound whenever isPartition && len(PartitionBounds)>0 — so the middle node
carries relkind='p' + relispartition=true + non-empty relpartbound together.
execCreatePartitionChild sets the sub-partition key (pg_get_partkeydef renders
it). pg_inherits emits one edge per PartitionParentOID, so the 2-level tree walks.

Fixture: psub (PARTITION BY LIST region) → psub_east (LIST partition of psub,
itself PARTITION BY RANGE id) → psub_east_lo (RANGE leaf). 4 assertions: top key
clause, middle node's own key clause, middle ATTACH-to-top FOR VALUES IN ('east'),
leaf ATTACH-to-middle FOR VALUES FROM (0) TO (100).

Files: internal/testport/pgdump_connsetup_test.go (fixture + assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 171 section),
.ralph/fix_plan.md (loop #138 progress note under M0110-0001).
Gates: gofmt clean; go build ./internal/... OK; go vet ./internal/testport/ clean;
TestPort_PgDumpConnectionSetup PASS (2.45s, NOT skipped); pgbench pre-commit smoke
on commit (.githooks/pre-commit).

Next (slice 172 candidates): (1) dedicated MINVALUE/MAXVALUE keyword-AST-node —
parser collapses keyword vs literal 'MINVALUE'; affects routing (latent). (2)
multi-parent inheritance INHERITS (a, b): ordering + shared-column merge dump
fidelity (NOTE: inherited-CHECK does NOT diverge in the dump — pg_dump suppresses
inherited checks either way, so child-has-no-check matches; ruled out). (3)
column-level STORAGE/COMPRESSION (needs parser keywords). See deferral ledger.
