(idle — nothing in flight)

M0122-0003 (EXPLAIN SETTINGS rendering) committed and pushed to
`align-data-structure-with-pg` (`adfb7dc7`):
- `internal/config/guc.go` gains `FlagExplain` (mirrors `guc_tables.c`'s
  `GUC_EXPLAIN`), tagged on the 45 goopg-registered GUCs upstream flags
  out of 62 total (extracted via a full-file scan of
  `postgres/src/backend/utils/misc/guc_tables.c`). 17 upstream names have
  no goopg registry entry at all (geqo*, temp_buffers, etc) — unreachable,
  not a bug, noted in the ledger.
- `internal/config/session.go`'s `SessionRegistry.ExplainVariables()`
  compares each FlagExplain GUC's effective value against its
  **canonicalized** BootVal — caught and fixed a real bug where comparing
  against the raw literal BootVal (e.g. "512MB") vs. the canonicalized
  effective value (e.g. "524288") false-flagged nearly every unit-bearing
  GUC as "modified" even at boot (`TestExplainVariablesEmptyByDefault`
  caught it before commit).
- New `Context.ExplainSettings` field wired in BOTH
  `internal/server/dispatch.go` and `dispatch_extended.go` (simple +
  extended protocol paths — sibling-paths rule).
- `internal/executor/operators_explain.go`: `appendExplainSettingsRow`
  (TEXT, omitted when empty) + `addExplainSettingsGroup` (JSON/XML/YAML,
  always-present `{}` when empty) called from all 4 `explainOp.Open`
  branches.
- Design: `docs/design/0122-0003-explain-format-xml-yaml.md` (new
  "SETTINGS rendering" section + cluster-status table updated) +
  `docs/design/README.md` index entry extended in place (no new doc file
  — same milestone slice).
- Tests: `internal/executor/explain_settings_test.go` (6 tests),
  `internal/config/guc_test.go` (+2 tests). Ledger row appended
  (`.ralph/deferral_ledger.md`, 2026-07-04, M0122-0003) — BUFFERS/
  `pg_stat_io`/`track_io_timing` still open, unchanged.
- Gates: build/vet clean; `internal/executor`+`internal/config`+
  `internal/server` full packages PASS; `make ralph-state-guard`
  auto-repaired the routine running/completed skew (harmless, documented
  pattern); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke
  via pre-commit hook PASS.

**Concurrency hazard — STILL PRESENT, now 4+ loops (#6,#7,#8,#9), but
non-blocking.** Two independent `ralph_loop.sh --live` trees are both
still running on this exact working tree (Tree A: root `2085426`
`SCREEN -dmS ralph`, this loop's own lineage; Tree B: root `2087325`
screen, PID `2087655`). This loop caught Tree B's WIP mid-session
(`internal/executor/pg18_user_catalog_rows.go` +2, `internal/wal/
recovery.go` +73, then moments later two new untracked files
`internal/initdb/matview_ddl_recovery.go`/`_test.go` appeared) — clearly a
materialized-view DDL/recovery feature in progress. Followed the #6-#8
precedent: staged an EXPLICIT file list for `git add` (never `-A`/`.`),
verified `git status` after staging showed none of Tree B's files, and
committed/pushed cleanly without touching its work. Did not attempt any
kill (per the historical 2026-05-25 saga in memory, killing a peer
supervisor is policy-blocked without user authorization anyway; also
moot here since this hazard hasn't actually blocked progress in 4 loops —
unlike that 6-hour standoff, both trees keep landing disjoint work
cleanly). If a future loop finds itself editing a file Tree B is also
mid-edit on (check `git status` + `git diff --stat` for files you didn't
touch, before every `git add`), stop and reconcile per the root-0026
precedent rather than force-add.

Next step: pick up the M0122-0003 remainder — BUFFERS rendering +
`pg_stat_io` real data + `track_io_timing` runtime SET all share one root
cause (no buffer-pool hit/read/write counters exist anywhere in
`internal/executor/instrument.go`'s `nodeStats`); this is a real
instrumentation-layer addition (locate `Pool.Get`/pin-count call sites in
the storage package, decide hit-vs-read, add counters), bigger than a
single loop — scope the first slice narrowly (e.g. just `shared hit=N
read=N` counters for SeqScan/IndexScan, skip dirtied/written/local/temp
initially). Alternatively continue the M0119-0004 pg_dump catalog-view
parity battery per `.ralph/fix_plan.md` if BUFFERS instrumentation looks
too large for one loop.
