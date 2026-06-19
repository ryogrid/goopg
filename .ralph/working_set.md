(idle — nothing in flight)

Last landed: DU-002 slice 263 (loop #30) — a WIDE multi-level partition tree now
round-trips through pg_dump. Slice 171 proved a single-chain sub-partitioned tree
(`psub_east` middle node + one leaf `psub_east_lo`); this slice widens it to the two
real fan-out shapes: (a) a second leaf `psub_east_hi` under the SAME middle node (two
pg_inherits rows → same parent; per-parent inhseqno increments per leaf), and (b) a
SIBLING sub-partitioned middle node `psub_west` + leaf `psub_west_lo` whose bound text
is IDENTICAL to psub_east_lo's, so the leaf→parent link is proven from PartitionParentOID
not the bound. NO production code changed — pg_inherits already keys each parent row off
the child's own PartitionParentOID (catalog.go ~4110).

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — three new children on the psub tree +
  assertions (psub_east_hi ATTACH; psub_west CREATE TABLE + ATTACH-to-top LIST bound;
  full single-line ALTER TABLE ONLY public.psub_west ATTACH ... psub_west_lo for the
  immediate-parent link).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 263 section + Next note.
- .ralph/fix_plan.md — slice 263 progress entry.

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS (3.33s,
byte-matches real pg_dump 18.3); pgbench pre-commit smoke runs on commit.

Next (slice 264+): per-partition column-level options on a partition child (child-only
NOT NULL / DEFAULT / CHECK), or INHERITS-tree dump fidelity beyond the single child.
