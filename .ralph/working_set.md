# Working set — M0134-0005 Bucket 2 landed; case still open, Bucket 3 is next

**Task:** M0134-0005 (`constraints.sql`) — **Bucket 2 LANDED 2026-08-18**
(commit `30855a6b`); the case stays `[ ]`. Selected per the Current Priority
banner (M0134 next after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md`
is still run `20260817-011734`, all 6 filed and `[x]` — nothing new to file.

**What landed.** `checkConstraints` (`internal/executor/operators_fk.go:1664`)
looped `tbl.CheckConstraints` unconditionally and never consulted
`tbl.NamedChecks[i].NotEnforced`, so a `NOT ENFORCED` CHECK still raised 23514 on
rows PG accepts. Six-line fix: `continue` when the aligned `NamedChecks` entry is
`NotEnforced` (bounds-checked). PG: `execMain.c:ExecRelCheck:1813-1815`.

**Three facts the research pass settled (don't re-derive).** (1) `NotEnforced` is
already parsed/stored for both CREATE TABLE and ALTER TABLE ADD CONSTRAINT, and
the ADD-CONSTRAINT initial scan (`operators_ddl.go:8093`) + VALIDATE CONSTRAINT
(`:8037`) already honour it. (2) `CheckConstraints` and `NamedChecks` are
index-aligned 1:1 by construction — single fan-in `catalog.AddCheckFull`
(`internal/catalog/catalog.go:288`). (3) `checkConstraints` is the ONLY runtime
enforcement site: INSERT/COPY (`operators_storage.go:2494`) and UPDATE
(`checkRowConstraintsForWrite`, `operators_fk.go:1830`) both route through it —
no sibling edit needed (Rule #2 verified, not assumed). Domain checks are a
separate path (`checkDomainConstraintsForRow`), out of scope.

**Measurement.** `constraints` diff 1515 → **1465** lines, hunks 30 → 31 (one
surviving hunk split, NOT new divergence). `NE_CHECK_TBL`/`NE_INSERT_TBL_CON`
gone. A plain shrink, unlike Bucket 1's unmasking. **Never compare to a
pre-2026-08-18 `constraints` number** (pre-C19 harness).

**Files:** `internal/executor/operators_fk.go`,
`internal/executor/operators_fk_check_notenforced_skip_test.go` (new,
`TestCheckConstraintNotEnforcedSkipsRuntimeEvaluation`),
`docs/design/0134-0005-constraints-sql-divergence.md` (§4 Bucket 2),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (1 row).

**Next step:** stay on M0134-0005 and brief **Bucket 3** — `ALTER TABLE DROP
CONSTRAINT` can't resolve a NOT NULL constraint by name
(`internal/executor/operators_ddl.go:10719` checks NamedChecks/FK/UNIQUE/EXCLUDE/PK
but never `tbl.NotNullConstraints`; PG `tablecmds.c:dropconstraint_internal`
handles `CONSTR_NOTNULL`). **First confirm whether `execAlterTableRenameConstraint`
shares the omission** — sibling pair, Hard-won Rule #2; the design doc flags it as
assumed-by-pattern, not read. Bucket 6 (`ALTER CONSTRAINT … NOT VALID/INHERIT/NO
INHERIT` unparsed) is the next-smallest after that. **Do not brief Bucket 7**
(root cause unpinned — needs research on the missing-column catalog-default *fill*
path). Buckets 4 (deferred statement-level UNIQUE) and 5 (GiST `circle_ops`) are
milestone-sized. New ledger item: `NOT ENFORCED` on **UNIQUE** constraints
(`UNIQUE_NOTEN_TBL`) — parser + index-maintenance path, not `checkConstraints`.

**Gates run:** guard test FAIL pre / PASS post; `go test ./internal/executor/`
and `./internal/catalog/` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (~9 min); `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35 — executor change, Rule #1); `scripts/pg-regress-runner.sh
constraints` re-measured 1465/31; pre-commit pgbench smoke PASS (select-only
12.8k tps). No TPC-DS — write-path-only constraint change.

**Delegation:** `tmp/ralph-handoffs/m0134-0005-s03-not-enforced-check/`
(researcher `a4171ab8654e1a662` 1 round → the three facts above; implementer
`ad24ea1b0e8fdeaa3` 1 round DONE; tester `a40d42e7d76325875` 1 round, 4 gates).

**In-flight:** none.
