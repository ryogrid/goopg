(idle — nothing in flight)

# Loop #65 result — PgAmcheck003 x4 FIXED (AI-20260922-004850-002..-005)

Banner: items 0-9 unchanged (item 3 blocked by M0145-0001 `[!]`;
`partition_aggregate` `[!]` awaiting an owner ruling). First open item-10
task in document order was the four pg_amcheck cases.

## The engine was right; the fixture was stale
All four failed on one shared signature — 42710 `extension "amcheck"
already exists` — from an unconditional `CREATE EXTENSION amcheck` run
after their stop/corrupt/restart cycle, under the comment "Runtime-only
amcheck install does not survive restart (gap #7c)". That was written
against a goopg whose extension install was in-memory only. Catalog DDL
durability has since landed, so the `pg_extension` row now SURVIVES the
restart (what PG does — it is a catalog, not session state) and a duplicate
CREATE EXTENSION then correctly raises 42710 (also what PG does). Both
halves of the new behaviour are PG-correct; only the workaround was stale.

## The judgement NOT taken (the transferable bit)
The obvious minimal edit was `CREATE EXTENSION IF NOT EXISTS` — one line,
all four green. It would also pass whether or not the row survived,
quietly re-admitting the gap it was written for. Instead each site now
ASSERTS `count(*) FROM pg_extension WHERE extname='amcheck'` = 1 after the
restart: same cost, and it converts a stale workaround into a regression
pin on the behaviour that replaced it.
Applied to all FOUR files — the identical stale step appeared verbatim in
each, so fixing one would have left the same defect under three names.

## Gates
Whole `TestPort_PgAmcheck*` family PASS (9.7s — catches sibling breakage
across the other amcheck ports); units PASS; pgbench smoke via hook.
Checked with `-v` that all four GENUINELY pass rather than taking one of
these files' own `t.Skipf` paths. Non-vacuity: flipping the expected count
makes the assertion report the value it actually read from the server.
No spotcheck/sf025/acceptance-arm — ZERO production diff (verified:
`git status` over `internal/` shows only `*_test.go`).

## No inventory change
AC-003 is `defer`/not-pass-required and its stated blockers (unsupported
index AMs, box/int4range/int4[] columns, STORAGE EXTERNAL TOAST corruption,
multi-DB orchestration) are untouched by a fixture fix, so the promotion
workflow does not apply.

## Next loop
Item 10's last open group: **PgoutputInterop x10**
(AI-…-006..-015, publisher/subscriber start failures, "second sighting").

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
