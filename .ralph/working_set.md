# Working set — written at the end of loop #58

## What this loop did

Banner item 10 (M-NIGHTLY open items). Selected the first open item in the
new nightly run: **AI-20260922-004850-001 `TestPort_IsolationEvalPlanQual`**
(the nightly regenerated `ci/logs/action-items.md` mid-investigation, so the
item I started as AI-20260921-000212-002 is now numbered -001).

Outcome: a **narrowing, not a fix**. No production code changed; all debug
instrumentation was reverted and the scratch cluster removed. Committed
documentation only.

## The EPQ finding (the thing to resume from)

Reproduced outside the TAP harness with two psql sessions, which separates
the real defect from the harness's cosmetic timing-only `<waiting>` line
offset cascade. Minimal pair, IDENTICAL plan shape
(`LockRows -> Index Scan using table_a_pkey`) except for an attached SubPlan:

  A. `SELECT id, value FROM table_a WHERE id=1 FOR UPDATE`
     -> `1|newTableAValue`              CORRECT
  B. `SELECT ta.id, ta.value, (SELECT ROW(tb.id,tb.value) FROM table_b tb
      WHERE ta.id=tb.id) FROM table_a ta WHERE ta.id=1 FOR UPDATE OF ta`
     -> `1|tableAValue|(1,tableBValue)` WRONG (pre-update value)

Instrumenting `lockRowsOp.Next` (internal/executor/operators_lockrows.go,
`entry.newPtrValid` block ~L1042) showed the EPQ machinery is NOT failing
where it appeared to: chain walk reached a distinct successor
(`ptr={0 9} newPtr={0 10} rowlen=4`), `refetchRow` returned non-nil
(`applied=true newcols=2`), merge ran — yet emitted
`[1, "tableAValue", "(1,tableBValue)", "(0,5)"]`.

TWO HYPOTHESES REFUTED BY READING CODE, do not re-explore them:
- NOT snapshot visibility. `refetchRow` (~L1797) applies NO snapshot at all;
  it pins the buffer and reads the raw heap tuple at the pointer, which is
  exactly what PG's EvalPlanQual wants. (This refuted the hypothesis loop #58
  started with.)
- NOT a chain-walk failure. `newPtr != ptr`.

## Next step — ONE measurement before any fix

Print the CONTENTS of `newLockedCols` (not just its length) in the
`entry.newPtrValid` merge block. The two branches have DISJOINT fix sites:
- `[1, "newTableAValue"]` -> `refetchRow` is right, the MERGE POSITIONS are
  wrong for a 4-wide `[id, value, subplan, ctid]` row: `lk.ColPos` /
  `lk.ColOffset+i` from the planner.
- `[1, "tableAValue"]`    -> `refetchRow` decoded the wrong image despite a
  correct `newPtr`: suspect `natts` from `Infomask2 & 0x07FF`, or the `cols`
  pick that takes the FIRST `o.plan.Locks` entry matching the relfilenode.

Do NOT write a concurrency fix before that print — a wrong guess in
row-locking code is a silent-wrong-answer class.

## Also settled this loop

- **AI-...-003 `TestPort_IsolationReceiptReport` CLOSED, not-reproducing.**
  PASSES at HEAD in 7.40s; also absent from run 20260922-004850, which
  independently confirms it. Ticked `[x]` with `Movement: none`; no code
  changed for it.
- **Filed the one new nightly subject: AI-20260922-004850-016
  `TestPort_RegressSuite`** (partition_aggregate, select_having,
  select_implicit, union). I filed it with the hypothesis that it was
  M0143-0007b slice-1/2 fallout already fixed by slice 3/4 (the run's sha
  `c07ebf0112d7` IS my slice-2 commit) — then **measured and REFUTED it**:
  all four still FAIL at HEAD. Note for the taker is in the task: that test
  does not persist its diff, so use `scripts/pg-regress-runner.sh` against a
  HEAD-worktree baseline, not `-v` on the subtest.

## State

- Ledger row appended (M-NIGHTLY AI-20260922-004850-001, `status = -`).
- `make ralph-state-guard`: OK after auto-repair.
- Units gate green; no production diff, so no spotcheck/sweep was required.
- Owner escalations STILL UNANSWERED, carried since loops 48 and ~57:
  (1) M0145-0018's cost-model no-go; (2) the banner-ordering question —
  M0145-0003 is strictly the first `[ ]` in item 3, yet loops 39-51 worked
  0009 -> 0018.
