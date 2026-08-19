# Working set — M0134 renumbered; 0005 PARKED, next is M0134-0006

**Task:** bookkeeping-only loop (user directive 2026-08-19): re-prioritise M0134
by renumbering, park M0134-0005, point the loop at the new M0134-0006.
No production code touched.

**What changed.** Eighteen user-named cases were **pair-swapped** into
M0134-0006..0023; the sixteen tasks they displaced took the vacated numbers in
ascending order; the other 155 tasks keep their filed numbers. The task lines in
`.ralph/fix_plan.md` and the table in the milestone doc were renumbered AND
re-sorted ascending, so "topmost unchecked" still equals "lowest ID".

**Files:**
- `.ralph/fix_plan.md` — 184 task lines renumbered+resorted; M0134 preamble
  rewritten; Current Priority banner extended; M0134-0005 line marked PARKED with
  its full resume ranking folded in (the baton is no longer the only copy).
- `docs/milestones/0134-regress-sql-failed-not-tried-digestion.md` — new
  "Priority renumbering — 2026-08-19" section with the old→new table and the
  three list-reconciliation rulings; Goals §2 and the task-list order text fixed.
- `docs/design/README.md` — 0134-0005 row marked `case PARKED 2026-08-19`.
- `docs/design/0134-0002-…md:358` (vacuum 0084→0021),
  `docs/design/0134-0001-p2-…md:1153,1383` (explain 0017→0082) — cross-refs.

**Findings worth carrying.**
- **The ID band no longer segregates status.** `failed`=0001..0087 /
  `not-tried`=0088..0189 was true only at filing. `select_parallel`
  (`not-tried`) is now 0008; `float4` (`failed`) is now 0153. Read each task
  line's own `` `failed` ``/`` `not-tried` `` word, never the number.
- **Three cases the user listed have no task:** `select.sql`, `delete.sql`,
  `sysviews.sql` already carry CSV `pass`. And `index.sql` does not exist
  upstream — read as `indexing.sql` (confirmed by the user).
- **M0134-0002/0003/0004/0005 are ALL parked.** 0006 is the first selectable
  M0134 task.

**Next step:** select **M0134-0006 (`select_having.sql`)** and, before any design
or implementation, run `scripts/pg-regress-runner.sh select_having` at HEAD — it
is one of the four "possible regression, verify" cases (milestone doc, Per-task
discipline §3). If it already passes, the whole task is a CSV flip to `pass` /
`pass_required=yes` with a "stale — already fixed" rationale plus
`make check-testport-inventory`; no implementation, no design doc.

**Gates run:** none required — documentation/bookkeeping only, no code changed.
Integrity verified instead: 189 IDs present exactly once and covering
0001..0189, exactly 34 IDs changed, task block sorted ascending, and the
(id, case) pairs in `.ralph/fix_plan.md` and the milestone doc identical.

**Delegation:** none. **In-flight:** none.
