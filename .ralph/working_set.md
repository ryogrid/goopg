# Working set — M0134-0005w landed (spurious LINE cursor positions)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005w LANDED** (`9c1254ee`).
Parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed (design §30):** `internal/executor/operators_ddl.go` — four
`Pos: act.Pos()` → `Pos: 0` at `:10121/:10130/:10139/:10557`. PG's
`pg_constraint.c` has **zero** `errposition()` calls, so none of
`AdjustNotNullInheritance`'s three rejections nor `heap.c:ConstraintNameIsUsed`
sets a position. New `operators_ddl_errpos_identity_test.go` (4 tests, Pos==0,
FAIL-pre/PASS-post). **647 → 601 lines / 30 → 28 hunks.**

**Three things worth not re-learning:**
- **`Pos` bugs are the cheapest lines-per-edit on this gate.** 4 edited lines
  closed 46 diff lines vs a ~12-line forecast: each spurious position costs
  *three* diff lines (message + `LINE 1:` echo + caret) and the caret displaces
  context. Multiply any position-bug estimate by ~3-4×. Prefer these first.
- **The re-census earned its keep again (3rd consecutive loop).** The carried #1
  (`DROP CONSTRAINT … ONLY` InhCount) is real and reachable but *shares its hunk*
  with a 15-line `pg_get_partition_constraintdef` gap — landing it alone closes
  no hunk. The carried #3 (ATACC3 `Nullable`) was badly *understated*: 8
  occurrences / 6 hunks / 3 code paths, not "the last residual line". Never brief
  off a carried ranking without re-measuring.
- **A "~2-line check" can hide a missing feature.** §29.5 estimated the identity
  `NOT VALID` check at 2 lines assuming the identity-add path existed. It does
  not: `ADD GENERATED … AS IDENTITY` is a *silent no-op* (verified live —
  succeeds, `attidentity` never set). Verify the host path exists before sizing a
  check inside it.

**Gates run:** `go build ./...`; `go test ./internal/executor/`; 4 new guard tests
PASS; `TestPort_.*(NotNull|Constraint|Identity|Inherit)` PASS (33s);
`scripts/pg-regress-runner.sh constraints` **601/28**;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (7m45s);
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**, Rule #1); pgbench smoke via hook.

**Next step — baseline is now 601 lines / 28 hunks** (never compare to a
pre-2026-08-18 number). Full measured hunk-by-hunk census with citations:
`tmp/ralph-handoffs/m0134-0005w-census/report.md` — read it before re-measuring.
Ranked, but re-verify reachability against 601/28 first:
1. **ATACC3 `Nullable`-blank family** — now the biggest in-theme bucket (~33+
   lines, 8 occurrences, 6 hunks) via 3 distinct paths: inline PK+INHERITS at
   CREATE TABLE; `ADD CONSTRAINT … PRIMARY KEY` heap-`attnotnull`-sync gap;
   `PRIMARY KEY USING INDEX` (missing the not-null constraint *and* the index
   rename). Needs **3 separate slices** — brief one path at a time.
2. Inherited-CHECK-enforcement family (82 lines / 2 hunks) — the most
   *consequential* correctness bug in the diff (inherited CHECK not enforced on
   child INSERT) but bundles ≥5 sub-bugs; needs its own research loop, not a slice.
3. `DROP CONSTRAINT … ONLY` InhCount — only worth doing bundled with the
   co-resident `pg_get_partition_constraintdef` gap.
**Not selectable** (ledgered, zero payoff here): identity `NOT VALID` (blocked on
the missing `ADD GENERATED` implementation), `ATExecValidateConstraint` recursion,
the CHECK half of `MergeConstraintsIntoExisting`, the child-has-no-NOT-NULL arm,
the 15 lock `ee.Pos` sites, FK `:11600`, the circle/GiST opclass cascade (67
lines, out of theme).

**Delegation:** `tmp/ralph-handoffs/m0134-0005w-census/` (researcher
`a6755b24ad7aba182`, 1 round, DONE) and `tmp/ralph-handoffs/m0134-0005w-errpos/`
(implementer `a2f58afa8c840b841`, 1 round, Part A DONE / Part B BLOCKED-ledgered;
tester `ab7e5e2761b4b420c`, both gates PASS).

**In-flight:** none.
