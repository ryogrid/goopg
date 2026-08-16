# Working set — M0134-0002 alter_table.sql (C3 slice 1 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**C3 slice 1 — constraint row-validation scans** (commit `93948d24`).

**Findings:** C3 (constraint validation scans absent) split into two slices by a
researcher reassessment. Slice 1 = the three non-index scans; slice 2 = the
index-build path (deferred). Diff 4298→4185 (−113), zero raw `(byte N)` leaks.

**C3 slice 1 landed:** ADD CHECK (no NOT VALID/NOT ENFORCED), SET NOT NULL + ADD
CONSTRAINT NOT NULL (both spellings), VALIDATE CHECK now scan existing rows and
refuse via new `forEachLiveRow` (page-Pin live-row iterator) +
`validateCheckConstraintRows` (parse-once → `planner.ResolveIndexPredicate` →
per-row `evalExpr`; 23514 on a definite boolean FALSE only — NULL/UNKNOWN passes).
55000 `cannot validate NOT ENFORCED constraint` guard + VALIDATE-CHECK convalidated
flip. All Pos 0. A reviewer round caught + fixed a `parser.SyntaxError` `(byte N)`
suffix leak. 8 tests.

**Files:** internal/executor/operators_ddl.go, operators_ddl_constraint_scan_test.go
(new), docs/design/0134-0002-alter-table-sql-divergence.md (§"C3 first slice"),
.ralph/fix_plan.md + .ralph/deferral_ledger.md (5 rows).

**Key symbols:** `forEachLiveRow`, `validateCheckConstraintRows` (operators_ddl.go),
`planner.ResolveIndexPredicate`, `evalExpr`.

**Deferred (5 ledger rows):** (1) C3 slice 2 — ADD PK/UNIQUE 23505 `DETAIL: Key …
is duplicated.` (per-entry value capture in `btree.BulkEntry`) + PK-over-NULL 23502
scan + Pos 0 on 23505; (2) FK-VALIDATE shadowing (C4 ADD-FK dup-name); (3)
nondeterministic partition-key DROP COLUMN guard (operators_ddl.go:21879-21883
`strings.Contains(fmt.Sprintf("%v", expr), …)` matches ASLR pointer hex — flips the
alter_table regress section between runs); (4) `parseCheckExpr` quote-loss (42601 vs
42703); (5) ONLY-partitioned DROP COLUMN guard missing.

**Next step:** C3 slice 2 (complete C3 — PK/UNIQUE 23505 DETAIL, PK-over-NULL 23502
reusing `forEachLiveRow`, Pos 0). Alternatively pull deferral #3 (nondeterministic
partition-key guard) forward first — it makes the alter_table regress diff
non-reproducible (measurement concern). New classes PLPGSQL/TYPEDS after C3/C9.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/executor/
./internal/parser/ ./internal/catalog/` PASS (8 tests); `scripts/pg-regress-runner.sh
alter_table` 4298→4185; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pre-commit
pgbench smoke PASS (12635 tps select-only).

**Delegation:** researcher `0134-0002-c3-constraint-scan-research` DONE; implementer
`0134-0002-c3-constraint-scan-impl` DONE (2 rounds); reviewer
`0134-0002-c3-constraint-scan-review` DONE; tester tpch-spotcheck DONE.

**In-flight:** none.
