Task: M0143-0002b — PK/UNIQUE/EXCLUDE `DROP CONSTRAINT` silently no-opping on
a non-default database (same shape M0143-0002 fixed for FK). LANDED AND
COMMITTED this loop (commit 7b22ae810).

Files: internal/catalog/catalog.go (`dropIndexByName` signature changed from
`(tableOID uint32, name string) bool` to `(tbl *Table, name string) bool`,
keys `c.ns()` off `tbl.DBOid` instead of hardcoded `DefaultDBOid`;
`DropPrimaryKeyConstraint`/`DropUniqueConstraint`/`DropExclusionConstraint`
all changed to take `*Table`; `HasPrimaryKey` keys off `table.DBOid` in
place, no signature change), internal/executor/operators_ddl.go (3 call
sites updated: PK/UNIQUE/EXCLUDE branches of execAlterTableDropConstraint),
internal/catalog/catalog_test.go (2 calls updated), internal/testport/
m0143_0002b_index_backed_drop_constraint_nondefault_db_test.go (new — one
sub-test per constraint kind in db "r", confirmed genuinely red pre-fix via
git stash), .ralph/fix_plan.md (M0143-0002b [x] + Done note; new
M0143-0002c filed, Parent: M0143-0002b — **note the lineage guard requires
`Parent:` at the START of its own body line, not inline after the bold
title on a continuation line — bit me once this loop, fixed by giving it
its own line**), .ralph/deferral_ledger.md (1 new row), docs/design/
0100-0149/p0-e4-catalog-xmax-loss-repro.md (new "M0143-0002b" section),
docs/design/README.md (p0-e4 row title + description updated).

Key symbols: dropIndexByName (catalog.go) — the fixed shared helper;
Table.DBOid (catalog.go ~466) — "already NamespaceDBOid-translated... matching
c.ns(DefaultDBOid)"; the `dbOid := DefaultDBOid; if tbl.DBOid != 0 { dbOid =
tbl.DBOid }` idiom (matches TableRealPages/relAllVisibleCell) is the
established pattern for this kind of fix — reuse it for any future
DefaultDBOid-hardcode sibling instead of re-deriving.

Findings: confirmed live (not just by reading) that `dropIndexByName` and
`HasPrimaryKey` shared the exact M0143-0002 hardcode shape — index
registration is genuinely per-DB (`c.ns(dbOid).byTable[tbl.OID]` at every
registration site) so a PK/UNIQUE/EXCLUDE-backed table in a non-default DB
had its DROP CONSTRAINT silently no-op while reporting success. All 5 call
sites had `tbl *Table` already in scope, so no relookup needed anywhere.
The task's own "six `deleteCatalogRowsForOID` sites" forward-reference is
STILL unresolved after two loops carrying it forward (M0143-0002 →
M0143-0002b) — those call sites already take an explicit `dbOid` param
(confirmed by grep both times), so the note describes something else and
needs fresh identification, not a third assumption; filed as M0143-0002c
with a concrete resume point (grep operators_ddl.go for
deleteCatalogRowsForOID, check METHODOLOGY3/04-forward-plan.md §3 for the
original claim's source).

Also this loop: launched a background investigation agent on M0143-0001
("an in-process test that crosses a DATABASE boundary", the task the
milestone doc calls "highest leverage of the six") purely to scope it before
committing a loop to it. Findings are now recorded directly in fix_plan.md's
M0143-0001 entry (read-only, no code changed) — key takeaway: the
"manual psql evidence only" framing is half-stale, since
`internal/postmaster/database_ddl_test.go` and `database_oid_wiring_test.go`
already have in-process (no socket/psql) precedent for both halves
separately (calling `tryHandleDatabaseDDL` directly, and setting
`ectx.CurrentDatabaseOid` directly) — the real gap is a test that CHAINS
both in one file, which belongs in `internal/postmaster` (not
`internal/executor` — import-cycle blocked) and is concrete/low-risk. The
harder half (reload `ListDatabases` loop under genuine WAL-replay/restart)
has a known wrinkle: `database_template_oid_collision_test.go`'s own
comment says the in-process `*postmaster.Server` harness "hangs on
multi-DB-write shutdown" — scope that as a likely-separate follow-up.

Next step: banner item 0 still gates on P0-E7 (needs P0-E6, owner-run,
`[!]`); sub-order unchanged: M0143 tasks whose gates don't need TPC-H data
first. Nightly triage confirmed complete this loop (all 17 items of run
20260917-004357 already filed). Good next picks, all not gated on TPC-H
data: M0143-0001 (now has a concrete scoped resume point in fix_plan.md —
start with the `internal/postmaster`-level chain test), M0143-0002c (just
filed, concrete grep-first resume point), M0143-0003 (pg_constraint 0-rows
after restart), M0143-0004 (PhysicalTypeIsVarlena IsArray), M0143-0005
(ParamRef LIMIT+DISTINCT), M0143-0006 (parser's 60 failing tests, still
unowned — same count/shape seen 3 loops running now). Re-check `## Current
Priority` fresh next loop per the Precedence rule before picking.

Gates run: `go build ./...` clean. `go vet ./internal/testport/...
./internal/catalog/... ./internal/executor/...` clean. `go test
./internal/catalog/... ./internal/executor/...` PASS. `go test -v -run
'TestPort_M0143_0002|TestPort_M0143_0002b|TestPort_M0143_0008b|TestPort_P0E4|TestPort_P0E5'
./internal/testport/` PASS 12/12 (new tests independently verified to fail
pre-fix via git stash of the 3 touched source files, pass post-fix).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — 60
pre-existing `internal/parser` failures (M0143-0006, unrelated, same
count/shape as prior loops, no parser file touched), every other package
PASS. `scripts/tpcds-sf025-regression.sh sweep` (re-run against staged
content so the gate-stamp's code_tree hash matched what was committed) —
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99
identical, gate-stamp PASS. `scripts/tpch-spotcheck.sh` and
`scripts/tpch-acceptance-arm.sh off <outfile>` (note: needs BOTH args, arm
name AND an out-file path, or it's a usage error before the HOLD check ever
runs) — both SKIP-BLOCKED (exit 3) by the `:65433` evidence hold, same
accepted G6 exception naming P0-E7 as re-run owner. Pre-commit hook's
pgbench smoke: PASS (both commit attempts — first rejected only by the
fix_plan lineage guard on M0143-0002c's `Parent:` line placement, fixed and
re-committed clean; commit-msg hook also required a `PARITY: N/A — <reason>`
line and a staged-content-matched tpch-acceptance-arm gate-stamp, both
supplied).
`make ralph-state-guard`: consistent both before and after commit.

In-flight: none. No servers or background processes left running (all test
clusters use cluster.New's t.Cleanup). The M0143-0001 investigation subagent
completed and its report is already folded into fix_plan.md — nothing to
resume there.
