# Working set — M0134-0002 alter_table.sql (C9 final LANDED, committed 045b9302)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop finished and
committed the in-flight **C9 final** slice (S1-S4 + the ONLY-cascade fix it
unmasked). C9 is now complete for the partitioned-parent block.

**Landed (045b9302):** S1 ADD COLUMN on a partition ⇒ 42809 (first guard,
`im.IsPartitionChild`, NOT `PartitionParentOID`); S2 `markAttachedColumnsInherited`
in BOTH ATTACH arms + `clearAttachedColumnsInherited` in BOTH DETACH arms
(tablecmds.c:17500 / :18009-18014) — closes the ATTACH `Inherited` gap (row 1410a);
S3 `colStillInherited` falls back to `im.PartitionParentOf`; S4 circular ATTACH ⇒
**42P07** (not 42P17 — that was a placeholder; tablecmds.c:20338-20362);
`execAlterTableDropConstraint(only bool)` no longer cascades under ONLY
(tablecmds.c:14025-14110). Diff 4102→4073 (−29).

**Files:** internal/executor/operators_ddl.go,
internal/executor/operators_ddl_c9_residuals_final_test.go (new),
internal/executor/operators_ddl_partition_recursion_test.go,
docs/design/0134-0002-alter-table-sql-divergence.md (new "C9 final" §),
.ralph/fix_plan.md, .ralph/deferral_ledger.md (3 new rows + cyclic row RESOLVED).

**Key symbols:** `markAttachedColumnsInherited` / `clearAttachedColumnsInherited`
(new twins), `colStillInherited` (map fallback), `execAlterTableAddColumn`
(42809 first guard), `execAlterTableDropConstraint(tbl, act, only)`,
`allDescendants` (visited set, reused for the cycle test).

**Next step (NEXT LOOP — re-read the fix_plan banner first):** M0134-0002 **C11**
— rules/view-DML + `at_view_2` + the top-level-* freeze; this is the last named
class for alter_table.sql. Cheaper alternatives if C11 needs research first: the
ledgered C9 leftovers — already-a-partition 42809 re-ATTACH guard
(alter_table.sql:2697), ADD CONSTRAINT duplicate-name merge accounting (extra
`merging constraint` NOTICEs), ONLY-guards for SET NOT NULL / ADD CONSTRAINT
(ledger row 1423).

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/`
PASS (cached); `go test -run 'C9|TestAlterTableDescendantWalkCycleSafe' -v` PASS;
`scripts/pg-regress-runner.sh alter_table` 4073 lines (from 4102, −29, no
regression); pre-commit pgbench smoke PASS at commit. tpch-spotcheck NOT run —
DDL-guard-only diff, no query/planner/codec path.

**Nightly triage:** ci/logs/action-items.md run 20260816-005117 — all 3 `## AI-`
items already filed under M-NIGHTLY (001 open, 002/003 ticked). Nothing new.

**Delegation:** tester gate-brief at
tmp/ralph-handoffs/0134-0002-c9-residuals-final/gate-brief.md — DONE (verdict DONE,
all four gates green). Implementer slice from the prior loop: DONE.

**In-flight:** none.
