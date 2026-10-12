# M0146-0155 — a routine's SET clauses are applied at call time

Status: landed 2026-10-12.
Parent: none (S2, placed into banner item 2a by the owner on 2026-10-12).
Found by the M0122-0008 SET TIME ZONE probe.

## Problem

`CREATE FUNCTION ... SET search_path = s1` stored `{search_path=s1}` in
`pg_proc.proconfig`, but nothing applied it when the function ran. Measured
against PostgreSQL 18.3:

- `sp()` with `SET search_path = s1` and body `select v from t` read
  `public.t` on goopg and `s1.t` on PG. plpgsql, set-returning functions,
  trigger functions and procedures behaved the same.
- `SET work_mem` and `SET timezone` on a function were equally ignored.
- `CREATE PROCEDURE ... SET` did not even store the clause. The parser
  collected it, but `applyProcAttrs` dropped it, and `CreateProcedureStmt` had
  no field for it.
- A security definer function that relies on `SET search_path` (PG's
  documented defence against search-path hijacking) was therefore unsafe on
  goopg.

Fixing the call-time half exposed two more defects, both pre-existing. The
function's own repro depends on them.

- **Unqualified names ignored the search path order.**
  `SearchPathCatalog.LookupTable` tried the bare name first, which answers
  `public` (and temp and pg\_catalog). It consulted the search path only when
  that missed. So `SET search_path = s1, public; select v from t` read
  `public.t` at top level too.
- **The cross-session plan cache ignored the search path.** Its key is the SQL
  text, the database and the planner fingerprint. A plan bound under one
  search path was served to a session, or a later statement, with another.

## PostgreSQL's mechanism

`fmgr_info_cxt_security` routes any function with `prosecdef` or a non-null
`proconfig` through `fmgr_security_definer` (fmgr.c). Per call, that:

1. opens a GUC nest level (`NewGUCNestLevel`);
2. applies proconfig with `ProcessGUCArray(..., PGC_S_SESSION,
   GUC_ACTION_SAVE)`. The context is `PGC_SUSET` for a superuser and
   `PGC_USERSET` otherwise;
3. runs the function;
4. on return calls `AtEOXact_GUC(true, save_nestlevel)`.

On error there is no catch. The enclosing (sub)transaction abort unwinds the
level.

`push_old_value` / `AtEOXact_GUC` (guc.c) decide what survives the level:

- **A variable the clause saved (GUC\_SAVE)** gets its prior value back. A
  `SET LOCAL` of that variable inside the function leaves the entry
  GUC\_SAVE, so it is undone too.
- **A plain `SET` or `RESET` of a saved variable** turns the entry into
  GUC\_SET. At exit it merges into the enclosing level and makes that entry
  GUC\_SET as well, so the new value outlives the function.
- **Variables the function sets without a clause** get entries at the
  function's level that merge down. SET and SET LOCAL behave as though the
  function boundary were not there.
- **ParameterStatus.** GUC\_REPORT variables are reported only at the end of
  the query, and only if the value differs from the last one reported. A
  saved value that is always restored therefore sends nothing.

`ExecuteCallStmt` makes a procedure with a SET clause atomic, so `COMMIT` /
`ROLLBACK` inside it raise 2D000.

`RangeVarGetRelid` walks the active search path, with the temp schema and
pg\_catalog implicitly first. The plancache replans when
`SearchPathMatchesCurrentEnvironment` fails.

## Design

### Save levels in the session registry

`misc.SessionRegistry` gains `saveLevels []*gucSaveLevel`
(`internal/utils/misc/session_save_level.go`).

- **`PushSaveLevel(config)`** opens a level and applies each `name=value`
  through `prepareSet`, the same validation SET uses. Clause values go into
  the local layer, which shadows the session layer, and the level records
  each saved key's prior local value. On a validation error the level closes
  itself and the error fails the call.
- **`PopSaveLevel(level)`** closes that level and any still open above it.
  For every saved key that was not overridden it restores the prior local
  value.
- **Overrides.** `Set` (non-local), `SetInternal` and `Reset` call
  `markOverridden(key)`, which flags the key in every open level that saved
  it. This is GUC\_SET merging down.
- **What needs no state.** `SET LOCAL` inside a level just writes the local
  layer: the pop restores a saved key, and an unsaved key persists to
  transaction end.
- **ParameterStatus.** All six report sites now go through `report()`, which
  notes the key in the open levels. Applying and restoring clause values
  report nothing. A restore reports only if something inside the level
  already sent a ParameterStatus for the key, so the client never keeps a
  stale value.
- **Process-global OnChange callbacks** (track\_io\_timing and others) fire
  on apply and restore, so backend state follows the effective value.

### Executor call sites

`Context.PushFunctionConfig` is wired in both dispatch paths (simple query and
extended protocol) through `sessionPushFunctionConfig`.
`enterRoutineConfig(r, ctx, pos)` applies `r.Config` and returns the pop,
which every caller defers. It is called at each place a routine body runs:

- `executeStoredRoutine` — scalar functions of every language;
- `evalSQLFunctionSetof` and `evalPLpgSQLFunctionSetof` — set-returning
  functions, after the STRICT short-circuit;
- `executeSQLProcedureCore` — SQL procedures;
- the plpgsql branch of `callOp.Next`, which also clears
  `PLpgSQLCommitChain` when the procedure has a SET clause;
- `executePLpgSQLTriggerBody` — trigger functions.

goopg does not inline SQL functions, so PG's refusal to inline a function
with proconfig needs no counterpart.

### Procedures store their SET clauses

- `CreateProcedureStmt.ConfigOps` is filled by `applyProcAttrs` (yacc) and
  by the legacy `parseCreateProcedureTail`, which now accepts SET / RESET as
  CREATE FUNCTION does.
- `execCreateProcedure` flattens the clauses with
  `flattenFunctionConfigOps` and stores them in `Routine.Config`, which is
  WAL-logged with the routine as functions already were.
- The parity-goldens diff is the new empty field on the three procedure
  records.

### Search-path order and the plan cache

- **Lookup.** `SearchPathCatalog.LookupTable` keeps the bare lookup's answer
  when it is a temp relation or lives in pg\_catalog / information\_schema.
  Otherwise it walks the search path and returns the first listed schema that
  holds the name. It stops at the bare answer's own schema, so the default
  path costs no extra lookup. A name found in no listed schema still gets the
  bare answer, as before.
- **Plan cache.** `sessionPlannerFingerprint` appends the parsed search path,
  so cached plans are keyed by the path they were bound under.

## Verification

- **Probes.** The PG 18.3 probe (`tmp/m0155/probe.sql`) covers SQL / plpgsql
  / SRF / trigger / procedure, SET LOCAL and SET inside, nesting, errors,
  exception blocks and atomic procedures. All its arms match, except the
  pre-existing gaps listed under Residuals. A second probe (ALTER FUNCTION /
  PROCEDURE SET and RESET, set\_config inside, search-path order) is
  byte-identical.
- **Restarts.** Procedure proconfig survives two restarts.
- **New tests.**
  - `TestFunctionSetClauseApplied` and
    `TestSearchPathOrderDecidesUnqualifiedName` (postmaster, end to end).
    Both fail on HEAD in every arm.
  - `TestSaveLevel*` (misc): restore, override, nesting, bad value, and
    ParameterStatus.
- **Regress A/B** over 25 namespace / function / GUC cases:
  - `guc` 385 → 352 lines. Its function-SET section now matches PG.
  - `triggers`, `create_view` and `rowsecurity` differed only by run context.
    Rerun in isolation, HEAD and the new binary are identical.
- **Gates.** Units; postmaster / executor / catalog / parser / initdb
  packages; the pg\_dump testport family; tpch-spotcheck (Q12=2, Q13=33);
  the acceptance arm (24 MATCH); sf025 PASS=99.

## Residuals (ledgered)

- **Security definer is not implemented at all.** goopg never switches
  `current_user` for a security definer routine, so only the GUC half of
  `fmgr_security_definer` exists.
- **No SUSET privilege check.** goopg does not check `PGC_SUSET` parameters
  against the caller, for a plain `SET` or for a function clause. PG raises
  42501 `permission denied to set parameter`.
- **`SET x FROM CURRENT` is dropped.** The parser records no op for it, so
  proconfig stays empty. PG captures the current value at CREATE time.
- **GUCs snapshotted at statement start ignore a function's clause.**
  `ctx.WorkMem`, `StatsTarget`, the parallel settings and others keep the
  caller's value inside the body. Only values read through `GetSetting`
  (search\_path, TimeZone, DateStyle, …) follow the clause.
- **Executor-side resolution of unqualified names** that bypasses the
  `SearchPathCatalog` (paths that use the bare `ctx.Catalog`) still answers
  `public` first. The search-path walk also does not honour an explicit
  `pg_catalog` placed after a user schema.
- **Found by the probe:**
  - `CREATE TEMP TABLE u` fails with "relation \"u\" does not exist" when a
    non-public schema on the search path already holds `u` (temp-shadow
    family, M0134-0018 ledger).
  - A plpgsql COMMIT in an atomic context renders the CALL's position
    instead of a CONTEXT line.
