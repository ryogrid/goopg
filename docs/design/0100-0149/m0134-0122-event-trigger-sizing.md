# M0134-0122: `event_trigger.sql` sizing — PARKED

**Status:** PARKED (regress-sql `not-tried` → CSV `failed`, `pass_required=no`).
**Oracle:** `postgres/src/test/regress/sql/event_trigger.sql` /
`postgres/src/test/regress/expected/event_trigger.out` (PG 18.3).
**Live sizing:** `scripts/pg-regress-runner.sh --verbose event_trigger` — 0%
parity, diff 649 lines pre-fix / 635 lines post-fix.

## Why this is parked

goopg's `CREATE EVENT TRIGGER` support (`internal/executor/operators_ddl.go`'s
`execCreateEventTrigger`) has always been scoped to `pg_dump` round-trip
fidelity only — the runtime `pg_event_trigger` registry exists so
`getEventTriggers`/`dumpEventTrigger` can reproduce the DDL, but **no DDL
execution path anywhere in goopg ever looks up or invokes an event trigger
function** (`evtfoid` is written to the catalog and never read back at
statement-execution time). This was a deliberate, documented scope decision
from DU-002/M0119-0004, not a regression introduced here.

`event_trigger.sql` is built almost entirely around observing *whether and
when* event triggers actually fire:

```sql
create event trigger regress_event_trigger on ddl_command_start
   execute procedure test_event_trigger();
...
create table event_trigger_fire1 (a int);
NOTICE:  test_event_trigger: ddl_command_start CREATE TABLE
NOTICE:  test_event_trigger: ddl_command_end CREATE TABLE
```

Every one of these `NOTICE` assertions, the `session_replication_role`-gated
enable/disable semantics (`ENABLE`/`ENABLE REPLICA`/`ENABLE ALWAYS`/`DISABLE`),
`pg_event_trigger_ddl_commands()`, and `pg_event_trigger_table_rewrite_oid()`
are unreachable without a real firing engine — the dominant share of the
file's 640 lines. `session_replication_role` is not even a registered GUC
today (`ERROR: unrecognized configuration parameter
"session_replication_role"`), an independent prerequisite gap.

This matches the established M0134 PARK pattern (cf. M0134-0109..-0121):
size live, land whatever is safely isolable and firing-engine-independent,
record the rest with a concrete resume point, and leave the CSV row `failed`/
`pass_required=no` rather than `pass`.

## What landed this loop

Two independent bugs, both self-contained inside `CREATE EVENT TRIGGER`'s
*validation* path (no firing engine needed):

### 1. Duplicate filter-variable detection

PG's `CreateEventTrigger` (`postgres/src/backend/commands/event_trigger.c`
~155-170) walks the WHEN clause's `DefElem` list in source order and raises
`error_duplicate_filter_variable` the moment a `tag` filter appears a second
time:

```c
foreach(lc, stmt->whenclause)
{
    DefElem *def = (DefElem *) lfirst(lc);
    if (strcmp(def->defname, "tag") == 0)
    {
        if (tags != NULL)
            error_duplicate_filter_variable(def->defname);
        tags = (List *) def->arg;
    }
    else
        ereport(ERROR, ... "unrecognized filter variable \"%s\"" ...);
}
```

goopg's parser (`internal/parser/ddl.go`'s `parseCreateEventTriggerTail`)
only ever kept a single last-write-wins `FilterVar string` field plus a
merged `Tags []string` slice — so `WHEN TAG IN ('create table') AND TAG IN
('CREATE FUNCTION')` silently combined both clauses' tag lists into one
`Tags`, instead of erroring. Fixed:

- `CreateEventTriggerStmt` (`internal/parser/ast.go`) gained
  `FilterVars []string`, appended once per WHEN clause in source order
  alongside the existing `FilterVar`/`Tags` writes.
- `execCreateEventTrigger` (`internal/executor/operators_ddl.go`) now walks
  `FilterVars` in a loop mirroring PG's `foreach` exactly: the first
  non-`"tag"` entry raises `unrecognized filter variable`, a second `"tag"`
  entry raises `filter variable "tag" specified more than once` (both
  `42601`, no `LINE` marker — matching the existing `unrecognized event
  name`/`unrecognized filter variable` ExecErrors' `Pos: s.Pos()` style,
  which already renders without a `LINE` marker on the wire).

### 2. Missing superuser-creation HINT

PG attaches `errhint("Must be superuser to create an event trigger.")` to the
`42501 permission denied to create event trigger "..."` error
(`event_trigger.c`, same function, superuser check). goopg's
`ExecError` already has a `Hint` field (used across the codebase for other
`HINT:` lines) but this call site left it unset. Added the hint text
verbatim.

New unit tests (`internal/executor/operators_ddl_event_trigger_test.go`):
`TestCreateEventTriggerDuplicateFilterVarErrors`,
`TestCreateEventTriggerNonSuperuserHint`.

## What's still deferred (ledger: `.ralph/deferral_ledger.md`, M0134-0122)

1. **The firing engine itself** — no hook anywhere in the DDL dispatch path
   looks up enabled event triggers by event name, evaluates the tag filter,
   honors `session_replication_role`, or invokes the trigger function's
   plpgsql body with `tg_event`/`tg_tag`/etc. bound. Resume point: a
   DDL-event-trigger dispatch hook injected at the top/bottom of each
   `ddl_command_start`/`ddl_command_end`/`sql_drop` exec path in
   `operators_ddl.go`, reusing `validateDDLTags`'s tag-matching logic
   (`cmdtag_table.go`) and mirroring the existing regular-DML-trigger
   `TG_*`-variable-binding machinery (`operators_trigger.go`,
   `plpgsql_runtime.go`) for a DDL-context variant.
2. **`session_replication_role` GUC** — entirely unregistered. Needed
   independently of event triggers (also gates logical-replication loop
   prevention). Resume point: add to the GUC registry as an enum
   (`origin`/`replica`/`local`), default `origin`, matching PG 18's BootVal
   (`guc_defaults_must_match_pg` convention).
3. **`CREATE FUNCTION`-time `event_trigger`-return-type validation** — four
   checks PG's `CreateFunction` (`functioncmds.c`) performs that goopg
   skips entirely: "function ... must return type event_trigger" (currently
   misreports "does not exist" via `resolveEventTriggerFunc` silently
   filtering to functions already event-trigger-shaped), "event trigger
   functions cannot have declared arguments", "SQL functions cannot return
   type event_trigger", and "trigger functions can only be called as
   triggers" (blocking a plain `SELECT test_event_trigger()`).

Both remaining gaps are multi-file REFACTOR-tier features (a firing hook
touching every DDL exec path; a new GUC; new CREATE FUNCTION validation
rules) — out of a single bounded loop's budget.

## Verification

- `go build ./...`
- `go test ./internal/parser/... ./internal/executor/...` — includes the two
  new tests above, plus the full pre-existing `operators_ddl_event_trigger_test.go`
  suite (unaffected).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
- `scripts/pg-regress-runner.sh --verbose event_trigger` — diff 649 → 635
  lines (both target divergences confirmed gone; the remainder is entirely
  firing-engine-dependent).
- Not a planner/executor cost-model change; `tpch-spotcheck.sh`/TPC-DS SF0.5
  gate not required.
