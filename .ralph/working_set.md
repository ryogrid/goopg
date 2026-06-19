(idle — nothing in flight)

Last landed: DU-002 slice 262 (loop #29) — a multi-column RANGE partition with a
MINVALUE/MAXVALUE open edge on a NON-leading column (`FROM (MINVALUE, MINVALUE) TO
(10, MAXVALUE)`) now round-trips through pg_dump and routes correctly. Coverage slice:
NO production code changed — the per-element bound machinery from slices 169/261 already
handled the shape; this slice proves + guards the first end-to-end multi-column exercise
(all prior fixtures were single-column).

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — new `public.pmc (a,b,val) PARTITION BY
  RANGE (a, b)` + child `pmc_lo FOR VALUES FROM (MINVALUE, MINVALUE) TO (10, MAXVALUE)`;
  Contains-asserts the parent key clause + verbatim ATTACH bound (bare keywords).
- internal/catalog/catalog_test.go — new TestRangeTupleMultiColumnOpenEdge drives
  rangeStrTupleGE/LT directly across concrete-prefix + open-suffix flag tuples.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 262 section + Next note.
- .ralph/fix_plan.md — slice 262 progress entry.

Gates: gofmt clean; go build ./... clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS (3.16s, byte-matches real pg_dump 18.3);
pgbench pre-commit smoke runs on commit.

Next (slice 263+): multi-level partition-tree fidelity beyond the single-leaf `psub`
tree (a sub-partitioned middle node with MULTIPLE leaves), or per-partition
column-level options on a partition child.
