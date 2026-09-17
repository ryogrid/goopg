(idle — nothing in flight)

Last completed: M0143-0003f (`poc.UniqueColumns`/`LIKE ... INCLUDING INDEXES`
never set `idx.IsConstraint` — a LIVE bug, not a restart-reload gap like
0003a-e). Not a new fix_plan entry: it was already filed `[ ]` at HEAD by the
previous loop, so flipping it to `[x]` added no new lineage-guard descendant.

What landed: two one-line fixes in `internal/executor/operators_ddl.go` —
`execCreateTable`'s `LIKE ... INCLUDING INDEXES` UNIQUE-clone loop
(`likeUniqueIndexes`, ~line 4254) and `execCreatePartitionChild`'s
`PARTITION OF ... (col UNIQUE)` inline-column loop (`poc.UniqueColumns`,
~line 5505) both call `createBTreeIndex` but never set `idx.IsConstraint =
true` afterward, unlike every sibling UNIQUE path (inline column,
table-level, named, PK auto-index) — so the resulting index never appeared
in `pg_constraint` even on a table that never restarts. Fix was the flag
flip alone; NO explicit `syncConstraintCatalogRow` call was needed — both
loops already run ahead of the pre-existing tail-of-function resync gate
`tableHasUniqueConstraintIndex(...)` (landed by M0143-0003c), which scans
`IndexesOnTable` directly rather than a fixed call-site list, so it picked
up the newly-flipped flag automatically and wrote the row via
`syncTableToCatalogHeap`. Two new tests in
`internal/executor/operators_ddl_like_indexes_test.go`:
`TestLikeIncludingIndexesMarksUniqueAsConstraint`,
`TestPartitionOfInlineUniqueMarksAsConstraint` (both assert
`idx.IsConstraint == true` on the cloned/created index, matching this
file's existing in-memory-catalog-assertion style, not a live `pg_constraint`
SELECT).

Gates run this loop: `go build ./...` clean; `go test
./internal/executor/...` PASS (12.4s, includes the 2 new tests);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full green
(all packages); `python3 scripts/ralph-lineage-guard.py` exit 0 (no new
descendant — task pre-existed as `[ ]`); `make ralph-state-guard` — found
`status=running`/`progress=completed` mismatch from the previous loop's
clean exit, auto-repaired to `in_progress`, clean after. No TPC-H data
needed/run — unit-scoped CREATE-TABLE-time fix, no planner/executor
row-count surface, consistent with the P0-E6-wait selection rule (`:65433`
HOLD marker `bench/tpch/runtime_goopg/data.HOLD` still present).

Docs updated: `.ralph/fix_plan.md` (M0143-0003f `[ ]`->`[x]` with full Done
note), `docs/design/0100-0149/m0143-0003-pg-constraint-reload-gap.md` (new
`## M0143-0003f` section + Sequencing line — noted as out-of-scope-for-title
but landed in the same investigation), `docs/design/README.md` (m0143-0003
index row appended with the 0003f summary).

Next step: re-read `.ralph/fix_plan.md`'s `## Current Priority` banner.
Check whether P0-E6 flipped `[x]` (HOLD marker was still present as of this
loop). If still `[!]`, M0143-0003's entire lineage (0003a-f) is now fully
closed — no more M0143-0003 descendants are queued. Continue the
P0-E6-wait fallback: scan M0143 remaining tasks (fix_plan lines ~7403+,
starting M0143-0004/0005/0006/0007...) for the next one whose gate doesn't
need TPC-H data, or move to recon-only tasks from items 3-6, or M-NIGHTLY
items, per the banner's explicit ordering. Read AGENT.md §"Plan-parity
harness" before selecting, per the banner's standing instruction.

In-flight: none.
