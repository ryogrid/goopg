# Working set — M0134-0005 Bucket 3 landed; case still open, Bucket 6 is next

**Task:** M0134-0005 (`constraints.sql`) — **Bucket 3 LANDED 2026-08-18**; the case
stays `[ ]`. Selected per the Current Priority banner (M0134 next after M-NIGHTLY).
M-NIGHTLY drained: `ci/logs/action-items.md` advanced to run `20260818-005518` with
**items: 0** — nothing to file.

**What landed.** `ALTER TABLE … DROP CONSTRAINT <name>` and `… RENAME CONSTRAINT
<old> TO <new>` now resolve named NOT NULL constraints. Both were blind; the twin
check the previous baton demanded came back **positive**.

**Four facts the research pass settled (don't re-derive).** (1) The RENAME path is
NOT a function named `execAlterTableRenameConstraint` — it is an inline `case
parser.AlterTableRenameConstraint:` at `operators_ddl.go:8796`. (2) It already
*referenced* `tbl.NotNullConstraints` inside the `constraintNameInUse` helper
(`:8836-8840`), which is exactly what makes a grep-based check wrongly report
"already handled"; its own resolution chain skipped NOT NULL. Its comment claiming
parity with the drop path's "same four stores" was wrong in both count and contents.
(3) `NamedNotNullConstraint.Name` is always populated, auto-named
`<table>_<col>_not_null` at every construction site — no data-model gap. (4) PG
recurses to children by **column name** for not-nulls and by constraint name for
everything else (`tablecmds.c:14251-14255`).

**Measurement.** `constraints` diff 1465 → **1431** lines, hunks 31 → **33**. An
**unmasking** like Bucket 1, not a plain shrink: every added hunk traced to a
pre-existing unrelated gap (ALTER CONSTRAINT ENFORCED / INHERIT parser gaps, NOT
NULL inheritance propagation). **Never compare to a pre-2026-08-18 `constraints`
number** (pre-C19 harness).

**Files:** `internal/executor/operators_ddl.go` (new `clearNotNullConstraint`
helper factored from the `AlterTableDropNotNull` case; step "3.7" in
`execAlterTableDropConstraint`; step "2.5" in the rename case; two stale comments
corrected), `internal/executor/operators_fk_unique_drop_constraint_test.go`,
`internal/executor/operators_ddl_rename_constraint_test.go`,
`docs/design/0134-0005-constraints-sql-divergence.md` (§5 Bucket 3, §6 next),
`docs/design/README.md`, `.ralph/deferral_ledger.md` (2 rows).

**Next step:** stay on M0134-0005 and brief **Bucket 6** — `ALTER TABLE … ALTER
CONSTRAINT <name> NOT VALID / INHERIT / NO INHERIT` is a parse error (no production
near `internal/parser/ddl.go:2863` `parseFKConstraintAttrs`; PG
`tablecmds.c:ATExecAlterConstrEnforceability` + the NO INHERIT toggle). It is the
smallest remaining and Bucket 3's unmasking just made two more of its hunks visible.
Watch the split flagged in the bucket table: the *parser* gap is a slice, the
*semantics* of toggling inheritance on an existing not-null is separate and larger —
brief the parser arm only, ledger the semantics. Alternative if Bucket 6 needs a
matching executor arm: the NOT NULL inheritance-propagation gap ledgered this loop
(`operators_ddl.go:4748-4753`). **Do not brief Bucket 7** (root cause unpinned).
Buckets 4 (deferred statement-level UNIQUE) and 5 (GiST `circle_ops`) are
milestone-sized.

**Gates run:** guard tests FAIL pre / PASS post; `go test ./internal/executor/` and
`./internal/catalog/` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (~9 min; `cmd/goopg` + `internal/initdb` ran
cold — cache miss, not a regression); `scripts/tpch-spotcheck.sh` PASS (Q12=2,
Q13=35 — executor change, Rule #1); `scripts/pg-regress-runner.sh constraints`
re-measured 1431/33. No TPC-DS — DDL-only change.

**Delegation:** `tmp/ralph-handoffs/m0134-0005-s04-notnull-constraint-byname/`
(researcher `a3681e5c45d1307f2` 1 round → the four facts above; implementer
`a6a36ac04a3f339df` 1 round DONE; tester `aef5d01a9beb8d0ca` 1 round, 4 gates).

**In-flight:** none.
