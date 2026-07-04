(idle — nothing in flight)

---

**Loop #14.** Concurrent `ralph_loop.sh` peers detected again at loop start
(SessionStart hook + `ps -eo pid,ppid,cmd`): two independent trees rooted at
separate screen/parent sessions, both actively spawning `claude` sessions
against this same tree. `git status` showed a large in-progress WIP
(`internal/catalog/catalog.go`/`codec.go`, `internal/executor/codec.go`,
`pg18_user_catalog_rows.go`(+test), `internal/initdb/open.go`,
`view_ddl_recovery_test.go` — the M0119-0004 reloptions-heap-encoder /
updatable-views work) — confirmed via `.ralph/deferral_ledger.md` this is
the peer's continuation of the `check_option`/`security_barrier` reloptions
round-trip line of work. None of those files touched this loop.

Picked an orthogonal M0122-0002 residual instead (verified zero file overlap
with the peer's dirty set before starting): `regexp_matches`'s deferred
`'g'`-flag multi-row SRF case (ledger row 2026-07-04, M0122-0002). Landed +
pushed `b3f4c9e3`:
- `internal/planner/plan.go`: new `RegexpMatchesCol` (ColIdx/StringExpr/
  PatternExpr/FlagsExpr) + `ProjectSet.RegexpMatchesCols`.
- `internal/planner/planner.go`'s `buildSelectSrfProjectSet`: detects a bare
  `regexp_matches(string, pattern[, flags])` SELECT-list target exactly like
  the existing `unnest(...)` detection (2-3 arity check, 42883 on mismatch),
  resolves args, assigns output type `text[]`, participates in the existing
  `srfColMap`/zip-width machinery unchanged.
- `internal/executor/operators_project_set.go`'s `openSelectSrfMode`: new
  `regexpMatchesResults` branch (evaluate string/pattern/flags per child row,
  call `evalRegexpMatchesSRF`, zip like `unnestResults`/`userResults`).
- `internal/executor/expr.go`: factored the pre-existing
  `regexpFirstMatchArray` into shared `regexpMatchArrayDatum` + new
  `regexpAllMatchesArrays(re, s, global bool)` (via
  `FindAllStringSubmatchIndex` when global) + `evalRegexpMatchesSRF` glue.
  Updated the scalar `case "regexp_match", "regexp_matches"` comment to
  clarify it now only handles the non-bare-target-list reachable cases.
- **Verified against a real PostgreSQL 18.3 cluster** (throwaway unix-socket
  data dir, `postgres/local_install/bin/{initdb,pg_ctl,psql}`,
  `LD_LIBRARY_PATH=postgres/local_install/lib`), byte-for-byte: `'g'` flag →
  one row per match; no flag → at most the first match; no match → **zero**
  rows (NOT the scalar path's NULL — a real, PG-matching asymmetry, not a
  bug). All 4 probe queries matched goopg's new output exactly.
- Tests: `internal/executor/regexp_matches_srf_test.go`
  (`TestRegexpMatchesSRF` — 4 bare-target cases; `TestRegexpMatchesSRFPerRow`
  — per-child-row zip against a passthrough column over a real 3-row table,
  confirming a no-match source row contributes zero output rows, not a NULL
  row).
- Design: `docs/design/0122-0002-pg-relation-size-real-sizes.md` new
  "Follow-up: `regexp_matches` SRF" section; `docs/design/README.md` existing
  0122-0002 row appended in place (no new row needed — already indexed).
  `.ralph/fix_plan.md` M0122-0002 banner appended.
- New ledger row (status `-`): the FROM-clause form
  (`FROM regexp_matches(...)`) is still unwired — needs a
  `planFromRegexpMatches` counterpart to `planFromUnnest`/
  `planFromGenerateSeries` (`internal/planner/planner.go`, ~line 3905).
- Gates: `go build ./...` clean; `go test ./internal/executor/...
  ./internal/planner/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
  PASS (Q12=2 rows/27.3s, Q13=33 rows/92.6s — ran in background while writing
  docs, no hang this loop). Pre-commit pgbench smoke hook PASS (~237-251 TPS
  TPC-B, ~14.5k TPS select-only). `make ralph-state-guard` clean.
- Committed via `git add -- <explicit 9 files>` + `git commit -- <same 9
  files>` (never a bare `git add`/pathspec-less `git commit`), per
  `ralph_concurrent_commit_pathspec_required`. **Caught a live repeat of the
  exact failure mode `HEAD~1` (`7ff4f286`) had already had to hand-fix once
  today**: `.ralph/deferral_ledger.md` and `docs/design/README.md` had a
  STALE INDEX entry (from an earlier `git add` sweep, one line different from
  both HEAD and the current worktree) sitting uncommitted before this loop
  even started — `git commit -- <pathspec>` re-stages the worktree content
  for exactly the named paths regardless of that stale index, so committing
  only after an explicit `git add -- <same 9 files>` (not relying on
  whatever was pre-staged) was essential; verified via `git status`/`git show
  --stat HEAD` before and after that the peer's `pg18_user_catalog_rows*.go`/
  `catalog.go`/`codec.go`/`open.go`/`view_ddl_recovery_test.go` files were
  untouched by this commit. Pushed cleanly (fast-forward,
  `282cf4c6..b3f4c9e3`, included the peer's already-landed `b4691ebd`/
  `7ff4f286` from before this loop started).

Next step: pick up the next M0119/M0122 item once the peer's
reloptions-heap-encoder fix lands (check `git log`/`.ralph/deferral_ledger.md`
first). Otherwise: `planFromRegexpMatches` (FROM-clause SRF form, this loop's
own new ledger row) is a clean, well-scoped next pick — or a fresh
`unimplemented_feat.json`/ledger `status=-` row not touching
`pg18_user_catalog_rows.go`/`catalog.go`/`codec.go`/`open.go` (still the
peer's active files as of this loop's end — re-check `git status` before
picking).
