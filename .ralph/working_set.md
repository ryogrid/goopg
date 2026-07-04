(idle — nothing in flight)

M0122-0003 (EXPLAIN (ANALYZE, BUFFERS) shared hit/read rendering)
committed and pushed to `align-data-structure-with-pg` (`2ca35bee`):
- `internal/storage/bufpool.go`: `Pool` gains `sharedHitCount`/
  `sharedReadCount` atomic counters + `BufferCounters()` accessor.
  Incremented at `Pin()`'s fast-path CAS hit, `pinSlow`'s `tryPinSlot`
  hit, and once per real `pinLoad` disk read. NOT counted (deferred,
  ledger row): `PinNew` (new-block allocation) and the rare
  race-recovery `tryPinSlot` re-pin calls inside `PinNew`/`pinLoad`.
- `internal/executor/instrument.go`: reused the existing per-node
  `instrumentedOp` ANALYZE wrapper (same nested-stopwatch pattern as
  `totalNs`/`rowsOut`) rather than inventing a second mechanism.
  `nodeStats` gains `bufHit`/`bufRead` (cumulative, inclusive of
  children) + `bufBaseHit`/`bufBaseRead` (last-seen Pool snapshot).
  `accountBuffers()` diffs `ctx.Pool.BufferCounters()` at Open/Next/Close.
- `internal/executor/operators_explain.go`: `formatBuffersLine` +
  wiring in `walkPlanAnalyzeFiltered` (TEXT + ANALYZE path only).
- Design: `docs/design/0122-0003-explain-format-xml-yaml.md` new
  "BUFFERS rendering" section + cluster-status row updated (no new doc
  file — same milestone slice, matches SETTINGS precedent).
- Tests: `internal/executor/explain_buffers_test.go` (3 tests: line
  present with hit=/read=, off-by-default without BUFFERS option,
  warm-cache repeat-scan shows hit-only no read=).
- Ledger row appended (`.ralph/deferral_ledger.md`, 2026-07-04,
  M0122-0003) — 4 residual gaps: FORMAT JSON/XML/YAML rendering,
  planning-only BUFFERS (no ANALYZE, PG17+ semantic), dirtied/written/
  local/temp terms, the 2 uncounted Pin() call sites.
- Gates: build/vet clean; `internal/storage`+`internal/executor` full
  packages PASS (`internal/storage` also PASS under `-race`);
  `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via
  pre-commit hook PASS.

**Process note (loop #10):** this loop's OWN first tool call combined
`pgrep -af ralph_loop.sh` with `cat working_set.md` in one Bash command;
the tool result truncated at 2KB before the `cat` output appeared, so
this loop never actually saw loop #9's working_set (which already said
"mid-edit on bufpool.go, unstaged" — i.e. this exact task). It
re-derived the same design independently and landed the same place, but
if a future loop's working_set.md read looks suspiciously short/missing
after a combined command, re-run `cat .ralph/working_set.md` ALONE to
be sure — don't let a truncated preview stand in for the real read.

**Concurrency hazard — STILL PRESENT (checked again this loop, #10).**
Two independent `ralph_loop.sh --live` trees are both still running on
this exact working tree: root `2085426` (`SCREEN -dmS ralph`, detached)
and root `2087325` (a `pts-9` screen, attached — this loop's own
lineage as of loop #10; the tree-A/tree-B PID roots can shift across
loops if a screen session restarts, so re-derive via `ps -eo
pid,ppid,args | grep ralph_loop.sh` at loop start rather than trusting
an old note's PIDs). This loop caught the OTHER tree mid-session
working on materialized-view restart persistence (M0119-0004:
`operators_ddl.go`, `initdb/open.go`, `pg18_user_catalog_rows.go`,
`wal/recovery.go`, `matview_ddl_recovery.go`/`_test.go`,
`docs/design/0110-0001-*.md`, `docs/design/README.md`); it had
committed and pushed cleanly (`0ef0584d`/`56168032`) by the time this
loop was ready to commit its own BUFFERS work, so `git status` showed a
clean split with zero overlap — staged an EXPLICIT file list (never
`-A`/`.`), verified `git status` after staging showed none of the other
tree's files, committed, fetched (only 1 ahead, no divergence), pushed
cleanly. Did not attempt to kill either loop (killing a peer supervisor
needs user authorization per the 2026-05-25 saga in memory; still
moot — 5+ loops now, both trees keep landing disjoint work without real
corruption). Also present: an untracked top-level `postgres` entry and
dirty `weekly_loc.csv`/`weekly_loc.png` (auto-generated LOC chart, not
from this loop) — left untouched, not part of any explicit add.

Next step: FORMAT JSON/XML/YAML BUFFERS rendering is the natural next
BUFFERS sub-item — no new counters needed, just add a `"Buffers"` key
(shared hit/read only) to `planToJSONWithStats` in
`internal/executor/operators_explain.go`, read from the same
`nodeStats.bufHit/bufRead` this loop populated. Alternatively pick up
`pg_stat_io` real data (needs the same `Pool.BufferCounters()` this loop
added, aggregated pool-wide rather than per-node) or continue the
M0119-0004 pg_dump catalog-view parity battery per `.ralph/fix_plan.md`.
