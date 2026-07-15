Task: M0119-0004 (DU-002 pg_dump round-trip slice-by-slice advance). Fixed
`catalog.Routines` (function/procedure registry) cross-database collision —
the M0122-0007 4e series' last unaudited registry (after domains/
userCollations/enumTypes/compositeTypes/rangeTypes). Commit pending (see
"In-flight" below — same unresolved git-push blocker as loop #38, not
touched this loop).

Files touched:
- `internal/catalog/routines.go` — `Routine` gained `DBOid uint32`; registry
  key folds it via `routineDBPrefix(dbOid)+schema.name(sig)` (mirrors
  `domainKey`/`enumKey`/`compositeKey`/`rangeKey`). `Create`/
  `CreateDuringRecovery` normalize DBOid==0 to `DefaultDBOid`.
  `Lookup`/`LookupByName`/`Drop`/`DropByName`/`ResolveByName`/`ResolveBySig`/
  `LookupDropCandidates` gained trailing variadic `dbOid ...uint32`.
  `DropRoutine`/`RenameRoutine`/`SetSchema` unchanged signature (read
  `r.DBOid` directly — they already receive a registry-sourced `*Routine`).
- `internal/executor/operators_ddl.go` — threaded
  `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)` through every
  CREATE/ALTER/DROP FUNCTION/PROCEDURE + COMMENT ON FUNCTION + DROP SCHEMA
  CASCADE routine-collection call site (`execCreateFunction`,
  `execCreateProcedure`, `execAlterFunction`, `execDropFunction`,
  `execDropProcedure`, `execCommentOn`'s function case, the DROP SCHEMA
  CASCADE loop). All `ddlOp` methods — no signature-cascading helpers
  needed touching.
- `internal/catalog/routines_dbid_isolation_test.go` — new
  `TestRoutinesCrossDatabaseIsolation` (mirrors
  `TestCreateDomainCrossDatabaseIsolation`).
- `.ralph/deferral_ledger.md` — new open row: full landed/deferred/resume
  breakdown, including the NEW proc_out OUT-parameter signature-matching
  bug the DU-002 probe advanced to (see Next step).
- `docs/design/0122-0018-per-database-catalog-namespace.md` +
  `docs/design/README.md` — new "Routine/function registry dbOid scoping"
  section + index row update.
- `.ralph/fix_plan.md` — M0119-0004 slice entry appended.

Key symbols: `catalog.Routines`/`Routine.DBOid`/`routineDBPrefix`/
`routineKey`/`nameKey` (routines.go); `execCreateFunction`/
`execCreateProcedure`/`execAlterFunction`/`execDropFunction`/
`execDropProcedure`/`execCommentOn` (operators_ddl.go); guard test
`TestPort_PgDumpConnectionSetup` (DU-002 probe, soft t.Logf not hard-fail).

Next step: DU-002 probe now fails restoring an ALTER/COMMENT-shaped
statement against `proc_out(a integer, OUT b integer)`:
`ERROR: procedure proc_out(integer, integer) does not exist`. Root cause
(traced, not fixed): `execAlterFunction`'s `argTypes` stub
(`internal/executor/operators_ddl.go`) is built from `s.Args` without
populating `ArgModes`, so `catalog.Routine.Signature()` (which excludes OUT
params, matching `pg_proc.proargtypes`) computes `"(integer,integer)"` for
the ALTER's full IN+OUT arg list against the stored routine's real
OUT-excluding signature `"(integer)"` — mismatch, lookup fails. Fix options
(pick one): (a) populate `ArgModes` in every ALTER/DROP/COMMENT arg-type
stub from `s.Args[i].Mode` before calling `Lookup`/`ResolveBySig`, or (b)
switch those call sites to a full-arg-list matcher like
`LookupDropCandidates` (already handles OUT-param full-arg matching
correctly) instead of `Signature()`-based lookup. Repro: `go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/` (soft-log, not a
hard failure) or minimal SQL: `CREATE PROCEDURE p(a int, OUT b int)
LANGUAGE sql AS $$...$$; ALTER PROCEDURE p(int, int) OWNER TO x;`.
Also still open (lower priority, see ledger row): the 5 signature-cascading
DDL-support helpers (access-method/FDW-handler/FDW-validator/
event-trigger/conversion func resolution) and cross-file read sites
(grant_ddl.go, plpgsql_runtime.go, expr.go, planner.go, etc.) are not
dbOid-threaded; `Routines.List()`'s pg_proc-view row-scoping is deferred.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean
repo-wide; `go test ./internal/catalog/... ./internal/executor/...` PASS
(incl. new `TestRoutinesCrossDatabaseIsolation`); `go test -short $(go list
./... | grep -v /internal/testport)` (full repo, short mode, 51 pkgs) PASS
0 FAIL; `go test -v -run '^TestPort_PgDumpConnectionSetup$'
./internal/testport/` PASS (soft-log confirms advance to the new blocker);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
bash scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads,
confirmed twice — no flake this loop); `make ralph-state-guard` clean (one
auto-repaired stale-progress-marker, same benign pattern as loops
#36/#37/#38).

M-NIGHTLY: `ci/logs/action-items.md`'s current run (20260715-010036, sha
751b82178025, 11 AI items) remains fully triaged/closed (confirmed again
this loop via `grep -n "20260715-010036" .ralph/fix_plan.md` — all 11
items have `[x]` entries). No new nightly items to add this loop.

In-flight: **git push still BLOCKED — same unresolved human-decision item
loop #38 flagged, NOT touched this loop.** Local `wal-format-mod` remains
`ahead N, behind 2` of `origin/wal-format-mod` (peer's WAL-removal PR #53
already merged upstream; local branch carries 6 redundant WAL-removal
commits `5e4f57af`..`280da2fd` never pushed anywhere, confirmed safe to
drop via rebase). This loop's new commit (routine registry dbOid fix, see
below) is genuinely new, non-duplicate work — disjoint files from every WAL
commit — but was made LOCALLY ONLY, same as loop #38's `2f50766b`. **Do NOT
attempt to auto-resolve the push conflict** — loop #38's working_set (still
readable via `git log`/prior context) already spelled out 3 resolution
options for the user; wait for explicit human direction before any rebase/
force-push on this branch.
