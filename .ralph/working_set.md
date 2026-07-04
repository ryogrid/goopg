(idle — nothing in flight)

---

**Loop #15.** Concurrent `ralph_loop.sh` peers confirmed again at loop start
(SessionStart guard + manual `ps -eo pid,ppid,cmd` ancestry trace): two
independent trees, both actively spawning `claude` sessions against this
same working tree — one rooted at the screen session (`2085426` →
`2085428`/`2427617`), one rooted elsewhere (`2087326` → `2087655` →
`2425775` → this loop's own `claude`/`2425781`). `git status` at loop start
showed the peer's in-progress WIP (`internal/catalog/catalog.go`/`codec.go`,
`internal/executor/codec.go`/`pg18_user_catalog_rows.go`(+test),
`internal/initdb/open.go`/`view_ddl_recovery_test.go`) — none of those files
touched this loop.

Picked the orthogonal next step `working_set.md` itself named: `FROM
regexp_matches(...)` (the FROM-clause SRF form deferred by loop #14's
SELECT-list-only landing, `b3f4c9e3`). Landed + pushed `c968bd90`:
- `internal/planner/plan.go`: new `FromRegexpMatches` (StringExpr/
  PatternExpr/FlagsExpr, single `text[]` output column).
- `internal/planner/planner.go`'s `planTableFuncRangeVar`: new
  `regexp_matches` branch dispatching to `planFromRegexpMatches` (mirrors
  `planFromUnnest`'s arg-context/alias/WITH-ORDINALITY handling exactly).
  Default column name `regexp_matches` — verified against a real
  PostgreSQL 18.3 cluster this loop.
- `internal/executor/operators_from_regexp_matches.go`: new
  `fromRegexpMatchesOp` (Schema/Open/Next/Close), reuses the existing
  `evalRegexpMatchesSRF` unchanged — no new match-expansion logic.
  Registered in `executor.go`'s `Build()` switch.
- Tests: `internal/executor/from_regexp_matches_test.go`
  (`TestFromRegexpMatches` — no-flag/'g'-flag/no-match/aliased-column/
  WITH-ORDINALITY-via-`SELECT *`, all byte-verified against real PG).
- **Discovered two GENERIC pre-existing gaps while testing** (confirmed via
  throwaway control tests using plain `unnest` — NOT introduced by this
  loop, so deliberately NOT fixed here, new ledger row instead):
  1. `WITH ORDINALITY AS t(m, n)` — naming BOTH aliases explicitly in the
     outer `SELECT m, n FROM ...` fails `42703: column "n" does not exist`;
     `SELECT *` over the same FROM item works fine. Resume point: the
     target-list column-name resolver vs `*`-expansion in
     `internal/planner/planner.go` — one of them reads a stale/pre-wrap
     binding snapshot.
  2. A same-level comma-join (even with explicit `LATERAL`) correlating a
     FROM-clause SRF's arg to a PRECEDING sibling FROM item's column fails
     `XX000: column ref ... on nil slot` — `ctx.OuterRows` appears wired
     only for subquery-based correlation, not a top-level multi-item
     `FROM a, b` nested-loop. Blocks the realistic pg_dump-style
     `FROM tbl, regexp_matches(tbl.col,...) AS t(m)` idiom for ANY
     FROM-clause SRF, not just this one.
  Both recorded in `.ralph/deferral_ledger.md` (M0122-0002 FROM-clause
  follow-up row) with resume points; M0122-0002 itself is now FULLY closed.
- Design: `docs/design/0122-0002-pg-relation-size-real-sizes.md` new
  "Follow-up: `regexp_matches` FROM-clause SRF form" section;
  `docs/design/README.md` existing 0122-0002 row appended in place.
  `.ralph/fix_plan.md` M0122-0002 banner appended.
- Gates: `go build ./...`/`go vet ./internal/planner/... ./internal/executor/...`
  clean; `go test ./internal/executor/... ./internal/planner/...` PASS (no
  regressions); `scripts/tpch-spotcheck.sh` PASS (Q12=2 rows/28.2s,
  Q13=33 rows/91.8s). Pre-commit pgbench smoke hook PASS (~235-251 TPS
  TPC-B, ~14.4k TPS select-only). `make ralph-state-guard`: found+
  self-repaired one status/progress inconsistency (peer-tree write race on
  `.ralph/progress.json`, reconciled to `in_progress`), clean after repair.
- Committed via explicit `git add -- <9 files>` + `git commit -- <same 9
  files>` (never bare `git add`/pathspec-less `git commit`), per
  `ralph_concurrent_commit_pathspec_required`; verified `git show --stat
  HEAD` touched only those 9 files and the peer's dirty set
  (catalog.go/codec.go/pg18_user_catalog_rows*.go/open.go/
  view_ddl_recovery_test.go) was untouched before AND after commit. Pushed
  clean fast-forward (`a1982de0..c968bd90`, included the peer's
  already-landed commits from before this loop started).

Next step: two independently-resumable, cross-cutting gaps discovered this
loop are good next picks (both affect every FROM-clause SRF, not just
regexp_matches) — the `WITH ORDINALITY` named-column-list resolution bug,
or the comma/LATERAL-join `ctx.OuterRows` wiring gap for multi-item FROM
clauses (bigger, touches the FROM-list join executor). Otherwise: re-check
`git status`/`.ralph/deferral_ledger.md` first — the peer's reloptions-heap
persistence line of work may have landed by now; if `catalog.go`/
`pg18_user_catalog_rows.go`/`open.go` are clean, `M0122-0001` (backlog
triage, doc-only, exempt) or the next open `M0122-000x` item are also live
options.
