# Working set — M0134-0005y landed (CREATE TABLE attnotnull heap resync)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005y LANDED** (`a10792d9`).
Parent case stays `[ ]` (516 lines / 23 hunks still diverge). Selected per the
Current Priority banner (M0134 after M-NIGHTLY). M-NIGHTLY drained:
`ci/logs/action-items.md` still at run `20260818-005518`, **items: 0**.

**What happened this loop:** loop #14 had already produced the entire 0005y diff
(executor fix + 5 guard tests + design §32 + ledger rows) and run the long gates,
but was cut off **before the commit** — the tree was dirty and the baton still
described 0005x. This loop re-verified the resumed diff against its own named
guards (project rule: a resumed uncommitted diff can build yet fail its guard),
then committed and pushed. No new code was written by this loop.

**What landed (design §32):** `execCreateTable` calls `syncTableToCatalogHeap`
exactly ONCE, before every constraint-processing arm; four later blocks flip
NOT-NULL state (named table-level PK, anonymous/LIKE-folded `pkCols`,
`TableNotNullCols`, INHERITS/LIKE `AddNotNull`) and none reached the heap.
pg_class is virtual but **pg_attribute is heap-backed**, so `\d+`/pg_dump read a
stale `attnotnull` while the `Not-null constraints:` footer (live `catalog.Table`)
was already correct. Fix = `notNullHeapDirty` flag at all four sites +
delete-old-then-resync at function end. The delete is **mandatory**:
`syncTableToCatalogHeap` is append-only, a bare second sync duplicates live rows.
**555 → 516 lines / 26 → 23 hunks.**

**Two things worth not re-learning:**
- **Inline column-level `b int PRIMARY KEY` is NOT the twin** — the parser sets
  `col.NotNull` at parse time, so it was already correct before the early sync.
  The implementer's first guard test passed pre-fix; the genuine twin is the
  **anonymous table-level** `PRIMARY KEY (b)`. Verify FAIL-pre, always.
- **Coordinator cwd drift is real** (`worktree_cwd_path_consistency_hazard`): an
  earlier `cd` into a handoff dir made a relative `mkdir tmp/ralph-handoffs/...`
  create a nested copy, and the tester correctly reported BLOCKED on a missing
  brief. Write handoff paths absolute.

**Gates run:** `go build ./...`; 5 `TestPort_CreateTable*` guards PASS (re-run
this loop, 4.1s/0.5s×4); `go test ./internal/executor/ ./internal/parser/` ok
(cached); pgbench smoke via hook (9.0k TPS select-only). Carried from loop #14:
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS,
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**).

**Next step — baseline is 516 lines / 23 hunks** (never compare to a pre-2026-08-19
number; the hunk census at `tmp/ralph-handoffs/m0134-0005w-census/report.md` is now
THREE slices stale on causes — re-measure before briefing). Ranked candidates:
1. **Inherited-CHECK-enforcement family** (~82 lines / 2 hunks) — the most
   *consequential* correctness bug left (inherited CHECK not enforced on child
   INSERT); bundles ≥5 sub-bugs, so it needs its OWN research loop first, not a
   direct implementation slice.
2. "merging column" NOTICE family (~10 lines / 4 hunks) — mechanical, but the
   emitting call site for the plain-`INHERITS` redeclaration case is still unpinned.
3. Remaining known-but-unsliced: sequence-schema-qualification in defaults,
   deferred-PK index-option rendering, NOT-NULL-footer gaps on `PARTITION OF`
   (surfaced by the 0005y implementer; none touched by that change).
**Not selectable** (ledgered, zero payoff): identity `NOT VALID` / `ADD GENERATED`,
`ATExecValidateConstraint` recursion, the CHECK half of
`MergeConstraintsIntoExisting`, the 15 lock `ee.Pos` sites, FK `:11600`, the
circle/GiST opclass cascade, and the `execCreateTable` single-sync restructure
(ledgered 2026-08-19 — a refactor of a ~600-line function).

**Delegation:** `tmp/ralph-handoffs/m0134-0005y-impl/` (implementer, DONE, 2
guard-fixture deviations documented) and `tmp/ralph-handoffs/m0134-0005y-verify/`
(tester `a0f63dd24b8a5ebd1`, 1 round, all gates PASS).

**In-flight:** none.
