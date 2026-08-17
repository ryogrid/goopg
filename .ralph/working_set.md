# Working set — M0134-0005 Bucket 1 landed; case still open, Buckets 2/3 are next

**Task:** M0134-0005 (`constraints.sql`) — **Bucket 1 LANDED 2026-08-18**
(commit `8edbf9ee`); the case stays `[ ]`. Selected per the Current Priority
banner (M0134 next after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md`
is still run `20260817-011734`, all 6 filed and `[x]` — nothing new to file.

**What landed.** `PREPARE p (regclass[])` returned a false `42704 type
"regclass[]" does not exist`, and every following EXECUTE then reported
`prepared statement "get_nnconstraint_info" does not exist` — 13 of 30 hunks,
the only cascade in the file. `isValidSQLTypeName`
(`internal/postmaster/dispatch.go:2038`, called from `:700`) was a hand-written
allowlist of **bare** built-in names. It now rejects unbalanced parens, strips
balanced `( … )` typmod groups wherever they appear (`timestamp(3) with time
zone`), strips trailing `[]`/`[N]` array suffixes (non-numeric subscript =
reject, not strip), then matches an **additively** extended allowlist (`reg*`
family, `bit`/`varbit`, `inet`/`cidr`/`macaddr`/`macaddr8`, `money`, `xml`,
`tsvector`, `tsquery`, `jsonpath`, `int2vector`, `oidvector`, `pg_snapshot`,
`"char"`, `character`/`character varying`). Signature and call site unchanged.

**Read this before re-measuring.** Cascade 13 → **0**, but the diff *grew*
1496 → **1515** lines (30 hunks both sides). That is an **unmasking** fix —
statements that used to abort on one error line now execute and emit real,
still-wrong result sets. Diff line count is NOT this case's metric of record.
Both numbers are post-C19 harness fix; **never compare to a pre-2026-08-18
`constraints` number**.

**Files:** `internal/postmaster/dispatch.go`,
`internal/postmaster/prepare_param_type_test.go` (new, `TestIsValidSQLTypeName`),
`docs/design/0134-0005-constraints-sql-divergence.md` (new — full 7-bucket map),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`
(3 rows 2026-08-18).

**Next step:** stay on M0134-0005 and brief **Bucket 2** — `NOT ENFORCED` CHECK
constraints are still enforced on INSERT; `internal/executor/operators_fk.go:1664`
`checkConstraints` loops `tbl.CheckConstraints` unconditionally and never consults
`tbl.NamedChecks[i].NotEnforced` (PG skips `conenforced=false`). Bucket 3
(`DROP`/`RENAME CONSTRAINT` can't resolve a NOT NULL constraint by name,
`operators_ddl.go:10719`) is equally bounded but **must first confirm whether
`execAlterTableRenameConstraint` shares the omission** — sibling pair, Hard-won
Rule #2. **Do not brief Bucket 7** (float8 DEFAULT truncation / `a_expr`-vs-`b_expr`
DEFAULT grammar / `currval()` default ordering): root cause unpinned, needs a
research pass on the missing-column catalog-default *fill* path. Buckets 4
(deferred statement-level UNIQUE) and 5 (GiST `circle_ops`) are milestone-sized.

**Gates run:** `go test ./internal/postmaster/` PASS; `TestIsValidSQLTypeName`
re-run by coordinator pre-commit PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/pg-regress-runner.sh constraints`
re-measured by coordinator at the committed tree (1515/30, cascade 0); pre-commit
pgbench smoke PASS (TPC-B 338 tps, simple update 635 tps, select-only 12.8k tps).
No TPC-H/TPC-DS — no planner/executor/codec change.

**Delegation:** `tmp/ralph-handoffs/m0134-0005-s01-measure/` (researcher
`a060c437efcb7b08d`, 1 round — produced the 7-bucket map, now durable in the
design doc); `tmp/ralph-handoffs/m0134-0005-s02-prepare-typenames/`
(implementer `a73212e2ab0f16dde`, 1 round, reported NEEDS-DECISION purely because
the diff grew — coordinator resolved it as unmasking after re-measuring).

**In-flight:** none.
