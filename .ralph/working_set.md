# Working set — M0134-0005v landed (ADD PRIMARY KEY / NOT NULL compatibility)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005v LANDED**. Parent case
stays `[ ]`. Selected per the Current Priority banner (M0134 after M-NIGHTLY).
M-NIGHTLY drained: `ci/logs/action-items.md` still at run `20260818-005518`,
**items: 0** — nothing to file.

**What landed (design §29):** `internal/executor/operators_ddl.go` —
`verifyNotNullPKCompatible` (port of `tablecmds.c:9577-9608`) +
`verifyNotNullPKCompatibleChildren` (PG's `!recurse` branch `:9532-9557`), with
`only` threaded from the single call site `:8096`. `ADD PRIMARY KEY` over a
column whose existing NOT NULL is `NO INHERIT` or `NOT VALID` now raises `55000`
with PG-verbatim message/DETAIL/HINT, `Pos: 0`, before index creation and the
null scan. New `operators_ddl_addpk_notnull_pkcompat_test.go` (5 tests,
FAIL-pre/PASS-post). **672 → 647 lines / 30 hunks.**

**Three things worth not re-learning:**
- **A carried ranking is a hypothesis, not a queue.** The §28 baton's ranked #1
  and #2 are both *fixture-unreachable* — real divergences, zero payoff here. The
  reachability pass retired both and found an in-diff 4th candidate at 2.5x the
  surviving #3. Second consecutive loop decided this way. Always re-measure a
  carried ranking against the current diff before briefing.
- **The fixture list is the scope, not the brief's line pointer.** My brief
  pointed at the own-column check (`~:10883-10913`); §29.2's three fixture sites
  needed the `ONLY`-children branch too. The narrow reading would have closed 1
  of 3 sites. The implementer widened correctly and disclosed it.
- **Over-delivery on the estimate**, for once: predicted ~662, measured 647 —
  because the `ONLY` branch closed two sites the estimate credited to one.

**Gates run:** `go build ./...`; `go test ./internal/executor/`; 5 new guard tests
PASS; `TestPort_.*(NotNull|Constraint|Inherit|Partition)` (54 subtests, 75.1s);
`scripts/pg-regress-runner.sh constraints` **647/30**;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (7m48s,
`internal/initdb` + `cmd/goopg` cold cache — not a regression);
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**, Rule #1); pgbench smoke via hook.

**Next step — pick from the remaining cluster.** Baseline is now **647 lines / 30
hunks** (never compare to a pre-2026-08-18 number). Carried, but each needs its
own fixture-reachability pass before any estimate is trusted:
1. `DROP CONSTRAINT … ONLY` not decrementing the child's `InhCount`/`IsLocal`
   (`notnull_tbl5_child`, `constraints.sql:803-806`) — reachable, measured **4
   lines / 1 hunk** at the 672 baseline; re-measure against 647.
2. The identity-column `NOT VALID` check (`constraints.sql:838`,
   `tablecmds.c:8311`) — reachable, ~2 lines.
3. `\d+`'s per-column *Nullable* blank for an inherited + PK-implied column —
   a describe-path bug (the last residual ATACC3 line).
4. Cosmetic: missing "merging column" NOTICE; suppressed inheritance NOTICEs;
   `regclass` ORDER BY sorting by non-OID.
**Not selectable here** (ledgered, zero payoff on this gate): `ATExecValidateConstraint`
recursion, the CHECK half of `MergeConstraintsIntoExisting`, the child-has-no-NOT-NULL
arm of `verifyNotNullPKCompatibleChildren`, the 15 lock `ee.Pos` sites, FK `:11600`.
The remaining candidates are small — consider whether the next loop should instead
re-census the 647-line diff for a new dominant bucket rather than drain 2-4 line items.

**Delegation:** `tmp/ralph-handoffs/m0134-0005v-research/` (researcher
`afdf51fe0f04a976b`, 1 round, DONE) and `tmp/ralph-handoffs/m0134-0005v-pkcompat/`
(implementer `a82eca33bae316c30`, 1 round, DONE — a write-guard blocked its
`report.md`, so its log lives only in the loop transcript and this file; testers
`a04d0873e87a9f90b` 6 gates PASS, `a58603e9601c21aad` spotcheck PASS).

**In-flight:** none.
