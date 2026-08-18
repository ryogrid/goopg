# Working set — AI-20260819-011823-001 fixed (REINDEX CONCURRENTLY phase order)

**Task:** M-NIGHTLY / AI-20260819-011823-001 —
`TestPort_IsolationReindexConcurrently`. **LANDED**, item ticked. Selected per
the Current Priority banner (M-NIGHTLY regression fixes outrank M0134).
Nightly log now at run `20260819-011823`, **items: 1** — that one item, now fixed;
the next nightly should drop it.

**What landed:** `REINDEX ... CONCURRENTLY` waited for concurrent lockers AFTER
building the shadow index, the reverse of PG (`WaitForLockersMultiple`
`indexcmds.c:4088` before `index_concurrently_build` `:4111`). The build's heap
scan therefore saw the spec's uncommitted concurrent INSERT as a live duplicate
(`isLiveForUniqueCheck` is right to call an active xmin live) and raised a
spurious 23505 before the wait ran. Fix = move `waitForRelationLockers(tblRel)`
above the build in BOTH twins (`rebuildIndexConcurrently`,
`rebuildTableIndexesConcurrently`), one source file.

**Three things worth not re-learning:**
- **A test that passed while the feature under it was a no-op proves nothing.**
  Ported 2026-06-22 when REINDEX CONCURRENTLY was catalog-only; `c8703d08`
  (2026-07-09) added the real build and broke it silently for six weeks.
- **Ask "new test or new regression?" BEFORE any bisect.** One `git log -S` on the
  test + the CSV row answered it in minutes and skipped a bisect entirely.
- **A design doc can assert a false invariant.** 0122-0007 claimed the wait was
  "unchanged in position, so spec timing is unaffected" — that sentence WAS the
  bug. The amendment says so explicitly rather than quietly rewriting it.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(7m46s; `internal/executor` cached, `internal/initdb` 421s — warm profile, not a
cold-cache event); `scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**);
`^TestPort_IsolationReindex` family PASS (3/3); `go test ./internal/executor/`
PASS; pgbench smoke via hook.

**Next step:** M-NIGHTLY has no other open item → **select M0134** per the banner.
M0134-0005 (`constraints.sql`) baseline is **465 lines / 22 hunks** (never compare
to a pre-2026-08-19 number). No census exists at this baseline — the last one
(`tmp/ralph-handoffs/m0134-0005w-census/report.md`) is FOUR slices stale on causes,
so **re-census before briefing**. Known live candidates:
1. **`SYS_COL_CHECK_TBL`** — system-column (`tableoid`/`ctid`) refs inside a CHECK
   are not validated; confirmed present, own slice, unrelated to inheritance.
2. `pg_get_partition_constraintdef` missing builtin (diff ~337-368) — introspection.
3. Unsliced: sequence-schema-qualification in defaults, deferred-PK index-option
   rendering, NOT-NULL-footer gaps on `PARTITION OF`, the "merging column" NOTICE
   family (~10 lines / 4 hunks; the plain-`INHERITS` redeclaration emit site is
   still unpinned).
**Not selectable** (ledgered, zero payoff / zero fixture reachability): the
`ALTER TABLE … INHERIT` CHECK-merge and `ADD CONSTRAINT CHECK` cascade gaps,
`AddCheckInherited` `NotEnforced`/`NotValid` on the two partition sites, identity
`NOT VALID` / `ADD GENERATED`, `ATExecValidateConstraint` recursion, the 15 lock
`ee.Pos` sites, FK `:11600`, the circle/GiST opclass cascade, the
`execCreateTable` single-sync restructure. Also newly ledgered and NOT selectable
now: `CREATE INDEX CONCURRENTLY`'s same build-before-wait defect (own slice, no
spec covers it) and REINDEX's still-absent phase-3 validate scan.

**Delegation:** `tmp/ralph-handoffs/ai-20260819-001-reindexconc-{repro,research,impl,gates}/`
(tester `a41f526114eb00137` repro DONE; researcher `a621a5a8f37b0f89f` DONE —
answered "new test, not new regression" and named the exact reorder; implementer
`ad0216bf3a919c81e` DONE 1 round, 0 deviations; tester `ae599bfbbbc6d93b5` gates
DONE 1 round, all PASS).

**In-flight:** none.
