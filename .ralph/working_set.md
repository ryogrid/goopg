# Working set — M0134-0005p landed (NOT NULL inheritance bookkeeping)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005p LANDED** (pushed).
Sub-item `[x]`, parent case stays `[ ]`. Selected per the Current Priority banner
(M0134 after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed (52 production lines, design §23):** four counter-arithmetic fixes,
all `internal/executor/operators_ddl.go`. **A** `SET NOT NULL`'s already-exists
branch now merges in place and returns **without cascading** (`tablecmds.c:7950-8010`);
**B1** the cascade propagates the source constraint's `NotValid` instead of
hardcoding false; **B2** the cascade's `visited` guard is re-keyed child-OID →
**(child OID, immediate-parent OID) edge** so a diamond descendant's `coninhcount`
is 2, not 1; **C** `PARTITION OF` stops copying the parent's `NotValid` onto a
brand-new empty partition. Measured **775 → 763 lines**, hunks 29 → 30 (line-shift
split, not a new divergence).

**Three things worth not re-learning:**
- **Hunk span is an upper bound on a sub-bug's size, never an estimate.** These 5
  hunks span ~205 lines but are entangled with ≥3 independently-rooted bugs; only
  ~40-60 were ever ours. A partial shrink was the *predicted* success criterion —
  brief it that way so the implementer doesn't chase the rest.
- **Root cause was one missing primitive:** `catalog.Table.AddNotNull` is a pure
  append that validates nothing, so five hand-written call sites each decide the
  four bookkeeping args by hand. That is how four independent arithmetic errors
  accumulated. Distinct from 0005o — all four sites already had the heap-resync
  quadruple; this was pure arithmetic.
- **PG's cascade has NO visited-set at all** (`tablecmds.c:8062-8079`); it relies
  on the inheritance DAG being acyclic. Copying a per-child visited map was the bug.

**Gates run:** `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(40 subtests); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(warm cache, not cold); `scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** —
Rule #1); pre-commit pgbench smoke via the hook.

**Next step — re-measure the census, then pick from the remaining cluster.**
Baseline is now **763 lines / 30 hunks** (never compare to a pre-2026-08-18
number). Each candidate still needs its own researcher pass per §21.1. Ranked:
1. `ATTACH PARTITION` NOT-NULL absorption — a **complete no-op** today (~90 hunk
   lines, ledgered 2026-08-18). Structurally site A at ATTACH time with
   `conislocal` flipping t→f. **Its PG oracle must be pinned first**: trace
   `tablecmds.c:ATExecAttachPartition` to its `AdjustNotNullInheritance` call —
   that unpinned citation is exactly why it was cut from 0005p.
2. `ALTER TABLE t INHERIT p` false "inherited from more than once" + the missing
   "conflicts with NOT VALID constraint on child table" check.
3. `ATExecValidateConstraint` not recursing to descendants
   (`QueueNNConstraintValidation`, `tablecmds.c:13213-13290`) — found by 0005p,
   made visible by its own B1 fix.
4. Cosmetic/small: suppressed inheritance NOTICEs; `regclass` ORDER BY sorting by
   non-OID; the diff:616-621 check-ordering bug (D1 family, §21.3).
**Do NOT re-rank `pg_get_partition_constraintdef` up** — it witnesses zero hunks;
the `constraints.sql:912` citation that promoted it was wrong (that line is the
diamond block). Also still open + ledgered, unwitnessed here: `PRIMARY KEY USING
INDEX`/`UNIQUE USING INDEX` promotion (M0097-0023), the central `evalGenExpr`
silent-NULL fallback, COPY BINARY `PushBinaryData`, COPY FROM's 9 error sites
omitting `execErrDetailFields(perr)...`.

**Delegation:** `tmp/ralph-handoffs/m0134-0005p-nninh/` — researcher
`af5e7c0818e936848` (1 round, DONE — re-measured census, pinned 5 write sites,
corrected the filler ranking); implementer `aa63e6df0eecdbca3` (1 round, DONE);
tester `addf8ebedaed78fcb` (1 round, DONE).

**In-flight:** none.
