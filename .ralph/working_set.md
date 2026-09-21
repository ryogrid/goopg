(idle — nothing in flight)

# Loop #71 result — current_database()/current_catalog FIXED

Banner: nightly run id UNCHANGED (20260922-004850) → PgoutputInterop stays
non-selectable. M0119 is first among the pre-existing milestones; worked the
`current_database()` task filed last loop (Parent: M0119-0006).

## The measurement answered the filed question AND split it in two
I asked routing-vs-reporting by creating a table in each database and
checking where it landed:
- **newdb** (real CREATE DATABASE) is genuinely ISOLATED — its table is not
  visible from postgres — yet `current_database()` said `postgres`. So for
  the ordinary case this was a pure REPORTING bug, and a wrong VALUE, not an
  imprecise one.
- **template1** is NOT isolated: its table IS visible from postgres. A
  genuine ROUTING defect, separate from the reporting one.

## Fix
One helper `currentDatabaseName`, used by BOTH `current_database()` and
`current_catalog`. They are the same value BY DEFINITION (PG exposes one
function under both names), both were hardcoded to `"postgres"`, and fixing
only one would leave a sibling pair that disagrees. The
empty-`CurrentDatabase` → `"postgres"` fallback is kept and pinned, because
`Context.CurrentDatabase`'s own doc says embedded/test contexts never set it.

Verified live: postgres/newdb/template1 all report themselves, both spellings
agree. Non-vacuity: disabling the helper fails exactly 4 assertions (2
spellings x 2 non-postgres databases); postgres and the fallback still pass.

## Filed, not fixed: template1 shares the postgres namespace
Its own task, with the control that makes it precise (newdb is correctly
isolated, so isolation WORKS and template1 specifically misses it). Suspect
template1 resolving to `DefaultDBOid` via `catalog.NamespaceDBOid` aliasing.
**Check first whether it is the same root cause as M0119-0006's remaining bs
item** ("template1 install re-attributes to postgres on restart") — if
CREATE EXTENSION in template1 really executes in postgres' namespace, the
restart is not re-attributing anything and the two are ONE piece of work.
Stated plainly in the task and ledger: this loop's fix makes that bug MORE
visible (the name now says template1 while storage still lands in postgres),
which is an improvement — the old answer masked it by being wrong in the
same direction.

## Gates (all green)
units; FULL upstream regress suite (only the known `partition_aggregate`);
tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99 changed=0`;
acceptance arm 24/24; pgbench smoke.
Note: `gofmt` reports a pre-existing import-ordering diff in `expr.go` — the
known repo-go1.25 vs newer-local-gofmt mismatch, NOT from this change; left
alone per the standing rule against `gofmt -w`.

## Next loop
Check the nightly run id FIRST. Otherwise M0119-0006's remaining bs items —
start with the template1 namespace task above, since it likely subsumes one
of them — then M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110.

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted. 3.
partition_aggregate's inventory row marks a never-passing case must-pass.
