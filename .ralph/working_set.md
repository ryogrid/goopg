# Working set — M0134-0002 alter_table.sql (C3 COMPLETE)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**C3 slice 2 — the index-build path**, completing class C3 (commit `838172d5`).

**Findings:** C3 slice 2 was smaller than the slice-1 deferral note predicted —
duplicate detection already existed but was mis-formatted (no DETAIL, spurious
`LINE 1`), and only the ADD-PK-over-NULL 23502 scan was genuinely absent.
Diff 4185→4157 (−28).

**C3 slice 2 landed:** ADD PK/UNIQUE on duplicates now emits 23505
`could not create unique index %q` + `DETAIL: Key (…) is duplicated.` (new
`btree.BulkEntry.KeyDesc` value capture + `sortBuildEntriesFindDuplicate`→`int`
dup index + `btreeBuildKeyDescription` renderer); ADD PK over NULL → 23502 via
`forEachLiveRow` (dup-then-null = PG pass order, ADD UNIQUE exempt); `Pos=0` on
all raise sites + the REFRESH MATVIEW sibling re-wrap. 7 tests. tpch-spotcheck
PASS (Q12=2/Q13=35).

**Files:** internal/executor/operators_ddl.go, pgindex_btree.go,
operators_ddl_constraint_scan_test.go (7 new tests), pgindex_buildkey_test.go
(4 call sites); internal/access/btree/bulkload.go (`KeyDesc` field);
docs/design/0134-0002-alter-table-sql-divergence.md (§"C3 slice 2").

**Key symbols:** `btreeBuildKeyDescription`, `btree.BulkEntry.KeyDesc`,
`sortBuildEntriesFindDuplicate` (now `int`), `forEachLiveRow`.

**Deferred (1 ledger row, 4 residuals):** float4/float8 DETAIL rendering
(`Datum.Format()` no float kind), multi-column PK null attnum-order,
`Duplicate keys exist.` ACL case, 42703/42P16 Pos on the ADD PK/UNIQUE arms.

**Next step:** pull deferral #3 forward — the nondeterministic partition-key
DROP COLUMN guard (`operators_ddl.go:21879-21883`
`strings.Contains(fmt.Sprintf("%v", expr), …)` matches ASLR pointer hex), which
flips the alter_table regress diff between runs. It pollutes the diff measurement
for every remaining class (C4/C9/C10/C11). Replace with a structural
`is_partition_attr`-style walk. Then C4 (ADD-FK dup-name / FK VALIDATE) or C10.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/access/btree/
./internal/executor/` PASS; `scripts/pg-regress-runner.sh alter_table` 4185→4157;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pre-commit pgbench smoke PASS
(12642 tps select-only).

**Delegation:** researcher `0134-0002-c3-slice2-research` DONE; implementer
`0134-0002-c3-slice2-impl` DONE; tester tpch-spotcheck DONE.

**In-flight:** none.
