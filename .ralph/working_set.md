(idle — nothing in flight)

M0119-0004 (plain CREATE VIEW restart persistence) committed and pushed to
`align-data-structure-with-pg` (`71cab6e7`):
- Root cause: `execCreateView` never called `syncTableToCatalogHeap`, so a
  plain (non-materialized) view had zero on-disk persistence — it simply
  ceased to exist after any restart, unlike a matview (M0119-0004's earlier
  loop) which at least survived as a downgraded table. Mirrored that fix one
  level down: `buildUserPGClassRow` (`internal/executor/pg18_user_catalog_rows.go`)
  emits `relkind='v'` (relfilenode=0) for `tbl.View != nil`;
  `loadUserTablesFromHeap` (`internal/initdb/open.go`) accepts relkind='v'
  and sets the reloaded table's `Virtual=true`; new `wal.RecordKindCreateView`
  (byte 103) carries `tableOID|queryLen|querySQL`, emitted from
  `syncTableToCatalogHeap`; new `internal/initdb/view_ddl_recovery.go`'s
  `replayViewRecords` (near-copy of `replayMatViewRecords`) re-parses it
  after `loadUserTablesFromHeap`, wired into `open.go`.
- Also closed two hazards in the same diff: (a) `CREATE OR REPLACE VIEW`'s
  OID churn (catalog.CreateView always assigns a fresh OID even on replace)
  — old OID's heap rows now stamped xmax-deleted via `deleteCatalogRowsForOID`;
  (b) `DROP VIEW`/`DROP MATERIALIZED VIEW` never stamped xmax on their own
  CREATE's heap rows (the matview half was a pre-existing gap from the
  earlier M0119-0004 loop, closed here as the sibling fix) — both
  `execDropOneView`/`execDropOneMatView` now mirror `dropTableByRefImmediate`'s
  cleanup.
  Tests: `internal/initdb/view_ddl_recovery_test.go` (4 tests, all pass).
  Design: `docs/design/0110-0001-pg-dump-tap-port.md` new "Follow-up: plain
  CREATE VIEW restart persistence" section.
- Deferred (ledger rows, both `-` status): (1) view `reloptions`
  (CheckOption/SecurityBarrier/SecurityInvoker) still don't round-trip
  through the heap-persisted pg_class row (`buildUserPGClassRow` hardcodes
  `reloptions="{}"` unconditionally — pre-existing, not view-specific). (2)
  `DROP MATERIALIZED VIEW` still leaks its physical heap file on disk (no
  `Pool.Manager().DropRelation` call for the matview's own storage).
- **New MAINT discovery (ledger row, unrelated to this loop's correctness):**
  `scripts/tpch-spotcheck.sh` hangs indefinitely against the long-lived
  `bench/tpch/runtime_goopg/data` dir (2.2GB, `global/`=283MB — abnormally
  large). Confirmed via `git stash` of this loop's 4 production files +
  rebuild that the hang **predates and is unrelated to** this change (still
  hangs with the files stashed). Leading hypothesis: repeated non-idempotent
  spotcheck runs across many prior loops bloated the mirrored shared
  catalogs (`mirrorTouchedCatalogsToPostgresDB`) without ever resetting.
  Gates run instead: `go build`/`go vet` clean; `go test
  ./internal/wal/... ./internal/initdb/... ./internal/executor/...` all
  PASS (one confirmed-flaky unrelated WAL test, reran 3/3 green in
  isolation); pre-commit pgbench smoke hook (fresh tmp data dir, unrelated
  to the bloated bench dir) PASS at commit time.

**Concurrency note (loop #11):** Tree B (the peer `ralph_loop.sh` tree) was
actively mid-edit on M0122-0003 BUFFERS rendering at loop start
(`instrument.go`/`operators_explain.go`/`bufpool.go`/`explain_buffers_test.go`)
and committed+pushed it cleanly mid-session (`2ca35bee`/`1fe2ef84`); by the
time this loop was ready to commit, Tree B had already staged (index only,
not committed) a NEW slice touching `docs/design/0122-0003-*.md`,
`docs/design/README.md`, `explain_buffers_test.go`, `operators_explain.go`.
Used `git commit -- <explicit pathspec list>` (not `git add` + plain
`git commit`) specifically BECAUSE Tree B's files were already index-staged
— pathspec-limited commit records only the named paths' current worktree
content and leaves everything else (staged or not) completely alone.
Verified via `git status` before AND after that none of Tree B's files
changed status. `.ralph/deferral_ledger.md` is a shared append-target both
trees write to — re-read it fresh immediately before editing to avoid
clobbering a concurrent append (this loop's Edit only flipped one row's
status field and appended after the true current last line).

Next step: either (a) `bench/tpch/setup_goopg.sh` to regenerate the TPC-H
data dir and confirm the spotcheck hang clears (a one-time MAINT reload,
see the new ledger row), restoring the mandatory spotcheck gate for future
executor/planner loops; or (b) pick up the next M0119/M0122 item per
`.ralph/fix_plan.md` — e.g. the CheckOption/SecurityBarrier/SecurityInvoker
reloptions-heap gap or the DROP MATERIALIZED VIEW physical-storage leak
just deferred above, both bounded single-loop items.

---

**Tree A addendum (this same loop #11, written after the note above —
Tree A here = this session).** Landed M0122-0003's BUFFERS FORMAT
JSON/XML/YAML slice, committed+pushed `96f390a3`:
`planToJSONWithStats` now sets `obj["Shared Hit Blocks"]`/`obj["Shared
Read Blocks"]` from the existing `nodeStats.bufHit`/`bufRead`
whenever `opts.Buffers` is set, unconditionally (even zero) — matches
`explain.c`'s non-TEXT `show_buffer_usage` branch (flat sibling
properties, confirmed by reading the upstream source directly — an
earlier ledger note's guess at a nested `"Buffers"` wrapper key was
wrong and is corrected in a new ledger row). No XML/YAML-specific code
needed (generic map renderer + `xmlTagName` already handle it). Tests:
3 new cases in `explain_buffers_test.go`. Design doc + README updated.
Gates: build/vet clean, `internal/executor` full package PASS, pgbench
pre-commit smoke PASS. tpch-spotcheck.sh was NOT run clean (see the
bloated-data-dir MAINT note above from Tree B — same root cause, hit
independently from this side too: 2 startup-timeout FAILs + 1 mid-Q13
`Killed`, Q12's row count was correct every time; relied on the
targeted `internal/executor` suite instead since the diff never
touches Q12/Q13's query path).

**Hit the git-index race Tree B's note above warns about, the hard
way — before reading this file's own advice.** Ran `git add <5
explicit files>` then a plain `git commit -m ...` (no pathspec on the
commit itself). Between those two commands Tree B staged its own new
`internal/initdb/view_ddl_recovery.go`/`_test.go` into the SAME shared
index; the pathspec-less `git commit` recorded the *entire* current
index, so `96f390a3` absorbed those 2 foreign files too. Verified
harmless before worrying further: `git diff HEAD -- <those 2 files>`
was empty right after (their content was already final), and Tree B's
own next commit (`71cab6e7`) correctly did not re-add them — no
duplication, no data loss, just a cosmetic misattribution in
`96f390a3`'s file list. Both commits were already pushed by the time
this was noticed, so no history rewrite was attempted (rewriting a
shared already-pushed branch to fix a cosmetic issue risks far worse
damage to a live peer's next push than the misattribution itself).
**Confirmed lesson, now doubly-validated:** under concurrent Ralph
loops, always commit via `git commit -- <explicit pathspec list>`
(records only the named paths' current content, ignores the rest of
the index) — never `git add <files>` followed by a pathspec-less `git
commit` (that commits the *entire* index, including anything a peer
staged in between). Saved as a durable memory
(`ralph_concurrent_commit_pathspec_required`) so this stops being
rediscovered per-loop.

Next step (Tree A): pick up `pg_stat_io` real data or `track_io_timing`
runtime `SET` (both M0122-0003 sub-items, both block on the same
storage-layer I/O-counter gap; `pg_stat_io` likely more tractable since
`Pool.BufferCounters()` already exists and just needs pool-wide
aggregation instead of per-node attribution).
