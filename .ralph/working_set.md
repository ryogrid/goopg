(idle — nothing in flight)

# Loop #77 result — M0122-0008: cross-database GRANT ON DATABASE

Banner: nightly run id STILL 20260922-004850 → PgoutputInterop ×10 remain
blocked. M0119's remaining items are `[!]` or owner-gated, so per banner item
10 moved to **M0122**, bucket 0008 (Auth / roles / multi-DB isolation /
encoding). Consumed `unimplemented_feat.json` entry "Implement multi-database
support" → `resolved`.

## The defect (measured vs a PG 18.3 oracle cluster)
`GRANT CONNECT ON DATABASE otherdb TO r1` from a `postgres` session reported
GRANT and left datacl NULL. An unknown db name succeeded SILENTLY instead of
raising 3D000 — the worse half.

## Root cause + fix (SIBLING PAIR, Rule #2)
A v0 comment justified matching only `ctx.CurrentDatabase`; upstream
`ExecGrant_Database` (aclchk.c) resolves against the SHARED pg_database and
never consults MyDatabaseId.
- writer `execDatabaseACLChange`: resolve every name via `ResolveDatabaseOid`,
  WHOLE list first (one bad name changes nothing before it errors), 3D000 on
  unknown; per-db body split into `applyDatabaseACLChange`.
- reader `pg_database` virtual row builder: render `datacl` for EVERY row, not
  just `postgres` (was dead code; would have hidden the stored ACLs).
- **Why the key is safe**: `ResolveDatabaseOid("postgres")` returns `DBOID()`
  — the key the old code used — so the connected path is unchanged BY
  CONSTRUCTION, not coincidence.

## Verified
Live: cross-db GRANT/REVOKE, multi-name list, 3D000, connected-db path all
match PG 18.3. `global/1262` gains a row version per resync (standby sees it).
Non-vacuity on BOTH halves (neutralise writer → 3 arms fail; neutralise reader
→ render test fails, writer tests stay green).

## Two residuals FILED + ledgered (do not re-discover)
1. `pg_database.datacl` does NOT survive restart — PRE-EXISTING, hits the
   `postgres` row too. Heap write is fine; nothing reloads the ACL store.
   Blocker: no aclitem-array DECODER exists (only the encoder).
2. aclitem array ORDER: goopg owner-first, PG world-first. Shared renderer →
   moves relacl/typacl/paracl/datacl together; own task.

## Gates
units PASS; executor+catalog PASS; tpch-spotcheck Q12=2/Q13=33 PASS;
tpcds-sf025 PLAN-SHAPE same=99 changed=0 PASS; pgbench smoke via hook.
Scratch /tmp/gm22 + PG /tmp/pgm22 stopped and removed.

## ⚠ BANNER CHANGED MID-LOOP — read it first
The OWNER wrote a new block into the `## Current Priority` banner while this
loop was running: **OWNER GO 2026-09-22**, answering the M0145-0001 lineage
escalation and the M0145-0018/0012 escalations — CONTINUE M0145, sequencing
the measured downstream walls first (**M0145-0009**, then **M0145-0019**
NL-costing for derived inners = 0018's option (c), then **M0145-0020**
`examine_simple_variable` CTE arm = 0012's prerequisite), then resuming the
flow chain 0004 → 0005 → 0007 → 0008. Harness additions M0145-0021/0022/0023
may run any time. This loop's M0122 selection predates that edit.
**NEXT LOOP SELECTS PER THE NEW BANNER: M0145-0009.**

## UNCOMMITTED WIP — `.ralph/fix_plan.md` (deliberate, not an accident)
This loop's M0122-0008 write-up IS on disk in fix_plan.md but is NOT committed.
The owner's banner edit is uncommitted in the same file, and the RALPH_LOOP
protected-region guard rejects any commit whose staged fix_plan carries a
banner delta vs HEAD. Committing a fix_plan WITHOUT the owner's hunk would
have dropped their edit. Everything else (code, tests, design doc, ledger)
IS committed. Next loop: once the owner commits the banner, stage
`.ralph/fix_plan.md` as-is — the write-up needs no rework.

## Also next
Continue M0122 buckets only if the banner allows. Note entry #59 (SASL channel binding) is UNIMPLEMENTABLE
as filed: `grep -rl crypto/tls internal/` is EMPTY — goopg has no TLS at all,
so the real blocker is upstream of SCRAM. Re-file it that way before touching
`internal/auth/`.

## Owner escalations OPEN — four (unchanged)
M0145-0018; M0145-0001 lineage; partition_aggregate inventory row; template1
namespace collision (Option A vs B, two dependent tasks).
