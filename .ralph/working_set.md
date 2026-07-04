(idle — nothing in flight)

M0122-0002 (catalog system functions & pg_* view stubs, ~9 quick wins) is
now fully closed, committed+pushed to `align-data-structure-with-pg`
(`1d4b2aa2`):
- `regexp_matches(string, pattern[, flags])` was the one genuine gap — its
  `evalExpr` case unconditionally `return NullDatum, nil`. Merged into the
  `regexp_match` case arm behind a new `regexpFirstMatchArray` helper
  (`internal/executor/expr.go`) that returns the pattern's capture-group
  array (or the whole match if no groups) for the FIRST match, with a
  non-participating optional group rendered as array `NULL` — mirrors PG's
  `setup_regexp_matches` (postgres/src/backend/utils/adt/regexp.c) and
  fixes a latent bug in `regexp_match`'s own pre-existing code (it always
  returned the full match, ignoring capture groups).
- The other ~8 items (`isfinite`, `justify_*`, `pg_get_expr`,
  `pg_get_serial_sequence`, `pg_get_indexdef`, `pg_relation_size` family)
  were re-verified against current HEAD and are already correctly
  implemented — `pg_get_indexdef` is under active separate extension via
  M0119-0004 DU-002 slices, deliberately not duplicated here per the
  fix_plan's own dedup rule.
- Deferred (ledger row appended, M0122-0002): true multi-row SRF ('g'-flag)
  expansion for `regexp_matches` — goopg has no FROM-clause/target-list SRF
  wiring for it (unlike generate_series/unnest/regexp_split_to_table,
  `internal/executor/operators_ddl_partition.go` SRF list). Resume point is
  in the ledger row and `docs/design/0122-0002-pg-relation-size-real-sizes.md`.
- Docs: `docs/design/0122-0002-pg-relation-size-real-sizes.md` updated with
  a "cluster closure" section; `docs/design/README.md` gained the missing
  `0122-0002` index row (this row itself had been dropped from the previous
  loop's carry-note — flagged in the design doc as a pending reconciliation
  from a concurrent-dirty-tree race, now resolved since root-0026 merged).
  `.ralph/fix_plan.md` M0122-0002 checkbox flipped `[x]`, "Next up" banner
  rewritten to point at M0119-0004 DU-002 / M0122-0003 next.
- Tests: new `internal/executor/regexp_match_test.go`
  (`TestEvalRegexpMatch`, 7 cases). Gates: `go build ./...` clean; `go vet
  ./internal/executor/...` clean; `go test ./internal/executor/...` full
  package PASS; pgbench smoke via pre-commit hook PASS (0 failed txns,
  TPC-B/simple-update/select-only). `make ralph-state-guard`: auto-repaired
  a stale `progress.json` "completed" marker (prior loop's own clean-exit
  marker, not project completion) → OK.

**Concurrency hazard — confirmed real, needs human attention, NOT
resolved by me:** two independent `ralph_loop.sh --live` driver processes
are running on this exact same working tree right now (no worktree
isolation, no locking in `ralph_loop.sh`): the named `ralph` screen
(PID 2085426→2085428, started 16:58:59 — the one hosting this session) and
an unnamed screen on pts-9 (PID 2087325→2087326→2087655, started
17:00:15, **Attached** — likely a live human terminal). Verified both have
`cwd=/home/ryo/work/goopg/goopg` via `/proc/<pid>/cwd`. This is NOT a
"subshell echo" false positive (checked the actual process tree/cwd/environ).
Empirically this already caused one real collision: root-0026's UPDATE-side
fix landed as "a reconciliation between two independent Ralph loops that
picked up the same fix concurrently" (see docs/design/README.md root-0026
row, merge commits b09085b3/7f5682ce) — survived via a 3-way git merge, not
silent corruption, but it easily could go the other way (e.g. two
overlapping in-progress edits to the same file mid-write, not just two
completed commits). I did NOT kill either process this loop: killing a
process attached to what looks like a live human terminal is a
hard-to-reverse action I can't get confirmation for in an autonomous
invocation (see `interactive_vs_ralph_stop_stash_restore` /
`concurrent_ralph_loops_corrupt_tree` memory notes). **A human should
check screen 2087325 (`screen -r 2087325` or attach via pts-9) and
decide whether to keep it or kill its process tree; if killing, use
`kill -TERM 2087655` then `2087663`, not `pkill -f ralph_loop.sh`.**
This has now been flagged by loop #6 AND loop #7 (this loop) — if a loop
#8+ finds it still unresolved, that is a signal escalation may be needed
beyond a working_set note (e.g. surfacing this in RECOMMENDATION more
loudly, or via whatever human-facing channel Ralph has).

Next step: pick up the next M0119-0004 DU-002 slice (probe
`TestPort_PgDumpConnectionSetup` against a live PG 18.3 A/B for the next
divergence, per the established discovery-probing pattern) or M0122-0003
(EXPLAIN output & pg_stat instrumentation, next unchecked M0122 item in
list order) per `.ralph/fix_plan.md`'s Current Priority banner.
