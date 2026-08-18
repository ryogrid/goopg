# Working set — M0134-0005k landed (NOT NULL conflict validation, 1024 → 933 lines)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005k LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** CREATE TABLE had **zero** not-null constraint-name / `NO INHERIT`
conflict validation, so goopg silently accepted all 14 `notnull_tbl_fail` shapes
(`constraints.sql:667-680`) that PG rejects — then cascaded into `relation already
exists` + phantom columns. 14/14 now byte-exact. Design §18.

**Four things worth not re-learning:**
- **PG runs TWO passes, and the wording tells you which.** Column-level
  (`parse_utilcmd.c:transformColumnDefinition`) says "declarations…constraints"
  (PLURAL); table-level (`heap.c:AddRelationNotNullConstraints`) says
  "declaration…constraint" (SINGULAR). Both appear in `constraints.out`. Emitting
  one spelling for both fails the case — the split is load-bearing.
- **The parser was silently DROPPING the table-constraint form.**
  `[CONSTRAINT name] NOT NULL col [NO INHERIT]` parsed and vanished. The catalog
  model needed **no** extension (`NamedNotNullConstraint` already had
  `Name`+`NoInherit`) — sixth "unwired, not missing" in this milestone. Keep
  probing the producer before any "needs new infrastructure" claim.
- **New coverage exposed three pre-existing bugs**, all required for the shapes to
  fail *correctly* rather than differently-wrong — notably INHERITS copying NOT
  NULL to children while **ignoring the parent's NO INHERIT flag**.
- **The round-1 census mis-cited the ADD COLUMN twin.** `:10523` is
  `execAlterTableAddPrimaryKey`; the real `execAlterTableAddColumn` (`:10145`) has
  a *larger* gap (registers no `pg_constraint` row for an inline named NOT NULL).
  Ledgered, not bundled. Verify a census's line citations before briefing on them.

**Gates run:** `go build ./...`; `go test ./internal/{parser,catalog,executor,optimizer}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit)' ./internal/testport/` (18 subtests);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1); pre-commit pgbench
smoke via the hook. Cache warm except `internal/initdb`/`cmd/goopg` (cold, not a
regression).

**Next step — pick from the §18.1 census, re-measured at the new 933-line/31-hunk
baseline (regenerate first: `GOOPG_CG_UNIT=<n> scripts/pg-regress-runner.sh
--verbose constraints`).** Strongest candidates, both ledgered silent wrong
answers in the SAME subsystem as 0005j so one slice could take them together:
`INSERT … SELECT` with an explicit column list (the `s.Select != nil` early return
in `rewriteInsertDefaultMarkers`, ~55 lines) and `COPY` with a partial column list
dropping every DEFAULT **and** skipping CHECK (`copy.go:323`, ~20 lines).
Otherwise: GiST `circle_ops` (~65, pre-existing), `tableoid`/`ctid` in a CHECK
(~15), `DEFAULT 1 IN (1,2)` grammar (~15), `pg_get_partition_constraintdef` (~15).
The three new ledger rows (ADD COLUMN pg_constraint row, `ALTER TABLE … INHERIT`
validation, inherited `coninhcount`/`conislocal`) are the rest of bucket 1.

**Delegation:** `tmp/ralph-handoffs/m0134-0005k-census/` (researcher
`a5d5b6071c2030ce1`, 2 rounds, DONE — census + PG oracle; note its round-1 line
citations needed correction); `tmp/ralph-handoffs/m0134-0005k-notnull-conflict/`
(implementer `a201ca28110755bdb`, 1 round, DONE);
`tmp/ralph-handoffs/m0134-0005k-gates/` (tester `aaa0d793ec0c2b236`, DONE).

**In-flight:** none.
