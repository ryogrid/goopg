(idle — nothing in flight)

## Loop summary (2026-07-10, loop #9)

**Outcome: M0122-0021 — verified & closed the last remaining sub-item
("restart persistence") of the VIEW WITH CHECK OPTION `unimplemented_feat.json`
entry (DU-002 slice 365). No code change; verify-before-implement finding,
same pattern as M0122-0019/0020 (loops #7/#8). Committed + pushed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task from loop #6; `go build ./...` clean
  at loop start — no new M-NIGHTLY task needed.
- Considered but rejected as too large for one loop (central-type / wide
  blast radius, matches `m0074_partial_scope_lessons`): timestamp-timestamp
  interval arithmetic + sub-day interval units (`unimplemented_feat.json`
  entries #4/#5) — properly implementing either requires extending
  `KindInterval`'s carrier (currently packs only months+days into one
  int64) with a microseconds component, touching codec.go wire encode/
  decode, spill.go serialization, comparison/format/justify functions, and
  the parser's interval-unit grammar. Also considered #84 (typed-table
  composite types in non-public schemas) — ruled out for the same reason:
  `catalog.InMemory.compositeTypes`/`compositeTypeFields` have ZERO
  schema-awareness (keyed by bare lowercase name only, no dbOid/schema
  param anywhere), which is a systemic namespace gap, not a bounded fix.
- Instead surveyed `unimplemented_feat.json`'s other ~99 open entries for a
  genuinely bounded item and found #82 (VIEW WITH CHECK OPTION) already had
  a 2026-07-04 audit marking enforcement+parsing RESOLVED but restart
  persistence still flagged open, with a note that a concurrent M0119-0004
  loop was mid-flight on exactly that gap. Checked: that loop landed the
  next day (`8107a8de`, 2026-07-05) — `buildUserPGClassRow`
  (`internal/executor/pg18_user_catalog_rows.go:462-474`) already encodes
  check_option into the heap pg_class row, `loadUserTablesFromHeapForDB`
  (`internal/initdb/open.go:2709`) + `catalog.ApplyTableReloptions`
  (`case "check_option"`, `catalog.go:15314`) already decode it back on
  restart, and the pre-existing dedicated test
  `TestTableAndViewReloptionsSurviveRestart`
  (`internal/initdb/view_ddl_recovery_test.go`) already covers this exact
  scenario end-to-end and PASSes at HEAD.
- Flipped `unimplemented_feat.json`'s DU-002/slice-365 entry `open`→
  `resolved` via surgical `Edit` (2-line diff: `status` + `code_audit`) —
  NOT a full `json.load`/`json.dump` rewrite. Verified with
  `python3 -c "json.load(...)"`: 82/181 resolved, 99 open (was 81/100).
- Added `.ralph/fix_plan.md` entry `M0122-0021` documenting the finding (no
  design-doc change — `docs/design/root-0025-updatable-views.md` already
  describes the landed behavior accurately).
- Gates run (foreground, all PASS): `go build ./...`, `go vet ./...`;
  `go test ./internal/initdb/... -run
  TestTableAndViewReloptionsSurviveRestart`; `go test ./internal/initdb/...`
  (full package, ~264s); `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` (0
  failed, all 3 workloads); `make ralph-state-guard` (self-repaired a stale
  running/completed mismatch, same pattern as loops #7/#8).
- Committed, pathspec-scoped to `.ralph/fix_plan.md`, `.ralph/progress.json`,
  `.ralph/working_set.md`, `unimplemented_feat.json` — did NOT touch the
  concurrently-modified `ci/logs/*`, `analysis/*`, `postgres` submodule, or
  untracked `weekly_loc.*` files sitting in the tree from another process.
  Pushed to `origin/align-data-structure-with-pg`.

**Next natural work:** M0122-0007's remaining real (non-stale) scope —
index/typed-table TEMPLATE copying (index-file cloning + per-database
sys-btree catalog bootstrap; composite-type OID resolution for typed
tables — NOTE: this is now confirmed a genuinely large item, composite
types have zero schema-namespace scoping anywhere in `catalog.InMemory`,
do not attempt as a single-loop bounded fix), OR per-database index/type
catalog rows + sys-btree bootstrap (independent of TEMPLATE copy), OR
`pg_statistic_ext`/`information_schema.routines` registry redesign — see
follow-up 43's deferral ledger row. OR continue surveying
`unimplemented_feat.json`'s remaining 99 open entries for more stale ones
(quick wins) before committing to a large architectural item — candidates
NOT yet checked: #67 (`pg_get_expr()` stub), #80 (COLLATE/USING in ALTER
TYPE ALTER ATTRIBUTE ignored), #88 (WITH OPTIONS column clause no-op), #93
(EXCLUDE USING constraint type-validation bypass). OR pick a different
milestone entirely for continued variety.

Gates run: go build, go vet, go test (initdb full package + targeted),
tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired) — all PASS.
In-flight: none
