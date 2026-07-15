Task: M0119-0004 (DU-002 pg_dump round-trip slice-by-slice advance). Fixed
the OUT-parameter ALTER/DROP/COMMENT FUNCTION signature-matching bug the
prior loop (#39/40 boundary) traced but didn't fix — resume point (5) from
that loop's `catalog.Routines` cross-database-isolation row. Commit
`70ced3b7` (see "In-flight" below — same unresolved git-push blocker as
loops #38/#39, not touched this loop).

Files touched:
- `internal/catalog/routines.go` — added `Routines.LookupWithArgModes`/
  `ResolveBySigWithArgModes` (take an extra `argModes []string` param so
  the internal `*Routine` lookup stub's `Signature()` can correctly
  exclude OUT-mode args); `Lookup`/`ResolveBySig` are now thin wrappers
  delegating with `nil` argModes — zero signature/behavior change for the
  ~20 pre-existing non-OUT-param callers (CAST/transform-func resolution,
  every `*_test.go` call site).
- `internal/executor/operators_call.go` — new `funcArgModes([]parser.
  FunctionArg) []string` helper (mirrors CREATE FUNCTION/PROCEDURE's
  existing inline mode switch), placed after `routineArgListStr`.
- `internal/executor/operators_ddl.go` — wired `funcArgModes`+
  `LookupWithArgModes`/`ResolveBySigWithArgModes` at all 6 rebuilt-stub
  call sites: `execAlterFunction`'s main lookup, `execDropFunction`'s
  is-a-function pre-check + CASCADE-target lookup + deferred/autocommit
  `ResolveBySig` (2x), `execCommentOn`'s "function" case, and
  `execCreateFunction`'s `ErrRoutineKindChange` DETAIL lookup. Also fixed
  DROP SCHEMA CASCADE's routine-collection loop: `rs.Drop(name,
  r.ArgTypes, dropDBOid)` (same bug shape, error silently swallowed via
  `_ =`) → `rs.DropRoutine(r)` (r already carries live ArgModes, no stub
  rebuild needed).
- `.ralph/deferral_ledger.md` — flipped nothing to `resolved` (per the
  file's own header: only M0119 triage tasks set that column); appended a
  new open row: full landed/deferred/resume breakdown, including the NEW
  VARIADIC-array signature bug the DU-002 probe advanced to (see Next
  step).
- `docs/design/0122-0018-per-database-catalog-namespace.md` — new
  "OUT-parameter ALTER/DROP/COMMENT FUNCTION signature-matching fix —
  LANDED" subsection under the routine-registry section; `docs/design/
  README.md` — appended a same-day follow-up clause to the 0122-0018 row.
- `.ralph/fix_plan.md` — M0119-0004 slice entry appended (same item block
  as the prior loop's routine-registry dbOid slice).

Key symbols: `catalog.Routines.LookupWithArgModes`/
`ResolveBySigWithArgModes`/`Routine.Signature()` (routines.go);
`funcArgModes` (operators_call.go); `execAlterFunction`/
`execDropFunction`/`execCommentOn`/`execCreateFunction` (operators_ddl.go);
guard test `TestPort_PgDumpConnectionSetup` (DU-002 probe, soft t.Logf not
hard-fail).

Next step: DU-002 probe now fails restoring an ALTER/COMMENT-shaped
statement against `CREATE FUNCTION public.sum_variadic(VARIADIC arr
integer[]) RETURNS integer` (fixture: `internal/testport/
pgdump_connsetup_test.go:5864`): `ERROR: function sum_variadic(integer)
does not exist`. Root cause NOT traced to a specific file yet — real PG
stores a VARIADIC parameter's `proargtypes` entry as the parsed type OID
AS WRITTEN (the ARRAY type, e.g. `_int4`/`integer[]`, not the element
type — verified against `postgres/src/backend/commands/functioncmds.c:
261,306-309`), and `format_type_be` auto-renders the `[]` suffix for an
array OID, so `pg_get_function_identity_arguments` should round-trip
`VARIADIC arr integer[]` symmetrically (confirmed pg_dump's CREATE
FUNCTION output preserves it verbatim — `pgdump_connsetup_test.go:9755`).
goopg's error drops the `[]` somewhere in the restore path. Investigate in
this order: (1) `internal/parser/function.go`'s VARIADIC-arg parse path
(`arg.Mode = parser.FuncArgVariadic` sites ~472/498/549/576) — confirm
`IsArray` gets set on the arg's `ColumnType` when a mode keyword precedes
an array-bracketed type; (2) `internal/executor/operators_ddl.go`'s CREATE
FUNCTION/PROCEDURE arg-building loop (~11391-11412) — write a throwaway
probe test asserting `ArgTypes[0].Name == "integer[]"` for a live `CREATE
FUNCTION ... VARIADIC arr integer[]`; (3) if storage is already correct,
chase `buildFunctionArguments`/`pg_get_function_identity_arguments`
(`expr.go` ~13989+) as the render-side culprit instead. Repro: `go test -v
-run '^TestPort_PgDumpConnectionSetup$' ./internal/testport/` (soft-log,
not a hard failure) or minimal SQL: `CREATE FUNCTION sum_variadic(VARIADIC
arr integer[]) RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$; SELECT
pg_get_function_identity_arguments('sum_variadic'::regproc);` (expect
`VARIADIC arr integer[]`, see what goopg actually returns, then chase
whichever direction diverges).
Also still open (lower priority, unchanged from prior loop, see ledger):
the 5 signature-cascading DDL-support helpers (access-method/FDW-handler/
FDW-validator/event-trigger/conversion func resolution) and cross-file
read sites (grant_ddl.go, plpgsql_runtime.go, expr.go, planner.go, etc.)
are not dbOid-threaded; `Routines.List()`'s pg_proc-view row-scoping is
deferred.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean
repo-wide; `go test ./internal/catalog/... ./internal/executor/...` PASS;
`go test -short $(go list ./... | grep -v /internal/testport)` (full
repo, short mode, 0 FAIL, incl. `internal/initdb` 232s); `go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/` PASS (soft-log
confirms advance to the new VARIADIC blocker); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/
ralph-precommit-test.sh` PASS (0 failed, all 3 workloads, both the
standalone pre-commit run and the git-hook-triggered run at commit time);
`make ralph-state-guard` clean (one auto-repaired stale-progress-marker,
same benign pattern as loops #36-#39).

M-NIGHTLY: `ci/logs/action-items.md`'s current run (20260715-010036, sha
751b82178025, 11 AI items) remains fully triaged/closed (confirmed again
this loop via `grep -n "20260715-010036" .ralph/fix_plan.md` — all 11
items have `[x]` entries). No new nightly items to add this loop.

In-flight: **git push still BLOCKED — same unresolved human-decision item
loop #38 flagged, NOT touched this loop.** Local `wal-format-mod` is now
`ahead 10, behind 2` of `origin/wal-format-mod` (peer's WAL-removal PR #53
already merged upstream; the ahead count grew from 6→10 across loops
#38-#40's genuinely-new, non-duplicate commits — routine-registry dbOid
fix + this loop's OUT-parameter fix — all disjoint files from every WAL
commit). **Do NOT attempt to auto-resolve the push conflict** — loop #38's
working_set (readable via `git log`) already spelled out 3 resolution
options for the user; wait for explicit human direction before any
rebase/force-push on this branch.
