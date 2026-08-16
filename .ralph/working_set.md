# Working set — M0134-0002 alter_table.sql (C9 residuals: partition recursion landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**C9 residuals — ONLY-on-partitioned guard + descendant-partition recursion**.

**Findings:** the partitioned-parent block (`alter_table.sql:2850-2858,2902-2903`)
cascades off ONE root cause: goopg silently drops `b` from `list_parted2` on
`ALTER TABLE ONLY ... DROP COLUMN`, so every later statement 42703's on the gone
column. Three guards close the three guard-statements: (1) ONLY DROP guard —
42P16 `cannot drop column from only the partitioned table when partitions exist`
+ HINT `Do not specify the ONLY keyword.` (tablecmds.c:9385-9389, Pos 0);
(2) descendant recursion — `allDescendants` (OID-sorted) walked when `!only`,
`partitionKeyUsesColumn` per descendant, 42P16 names the DESCENDANT (`part_5`);
DROP Pos 0, ALTER TYPE `act.Pos()` (tablecmds.c:9373/9422-9424/14576);
(3) ALTER TYPE inherited-column guard — 42P16 `cannot alter inherited column "%s"`
(tablecmds.c:14436-14440, `act.Pos()`, before own-key). `only` threaded from
`execAlterTable` (`s.Only`). Diff 4110→4102 (−8), `:2850`/`:2902`/`:2903` byte-green.

**Files:** internal/executor/operators_ddl.go (3 guards + only-threading),
internal/executor/operators_ddl_partition.go (partitionKeyUsesColumn helper),
internal/executor/operators_fk.go (allDescendants visited set),
internal/executor/operators_ddl_partition_recursion_test.go (5-case + cycle-safety);
docs/design/0134-0002-alter-table-sql-divergence.md (§C9 residuals → LANDED);
.ralph/deferral_ledger.md (2 new rows); fix_plan.md (C9 residuals landed note).

**Key symbols:** `partitionKeyUsesColumn` (new), `allDescendants` (visited set),
`execAlterDropColumn`/`execAlterColumnType` (only param + guards),
`colStillInherited` (reused), `hasInheritanceChildren` (reused).

**Deferred (2 new ledger rows):** cyclic-ATTACH 42P17 accepted (visited set is the
guard, not the fix — tablecmds.c:17336); `part_2 ADD COLUMN c text` accepted (no
`cannot add column to a partition` guard, tablecmds.c:7250). Also still open:
ATTACH-PARTITION `Inherited`-marking gap (row 1410a) blocks `part_2 DROP/RENAME/
ALTER` inherited refusals — the ALTER TYPE inherited guard (3) is LATENT until
that lands (part_2's `b` isn't flagged Inherited).

**Next step:** close the three remaining C9 residuals to fully green the
partitioned-parent block, then C11 (rules/view-DML + at_view_2 + top-level-* freeze):
(1) `execAlterTableAddColumn` relispartition guard (PartitionParentOID != 0 →
`cannot add column to a partition`); (2) mark attached columns `Inherited` in the
ATTACH PARTITION path (unblocks :2854-2858 inherited refusals); (3) cyclic-ATTACH
42P17 ancestor-walk in `execAlterTableAttachPartition`.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/`
PASS (6.6s); `scripts/pg-regress-runner.sh alter_table` 4110→4102 (−8), three
guard statements byte-green. tpch-spotcheck NOT re-run — DDL-only ALTER guards,
no query/planner/codec path. Pre-commit pgbench smoke: runs at commit (hook).

**Delegation:** researcher `0134-0002-c9-residuals-partition-recursion-research`
DONE; implementer `0134-0002-c9-residuals-partition-recursion` DONE (deviation:
allDescendants visited set — justified, cyclic-ATTACH hang; NEEDS-DECISION
resolved: keep visited set + defer cycle rejection).

**In-flight:** none.
