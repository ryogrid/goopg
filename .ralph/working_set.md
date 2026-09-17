Task: M0143-0002 — FK `DROP CONSTRAINT` silently no-ops on a non-default
database. LANDED AND COMMITTED this loop (commit 0a8856000).

Files: internal/catalog/catalog.go (`DropForeignKeyConstraint` signature
changed from `(tableOID uint32, name string) bool` to `(tbl *Table, name
string) bool` — mutates the caller's already-resolved live pointer instead
of re-resolving by OID scoped to `DefaultDBOid`), internal/executor/
operators_ddl.go (single call site updated, FK branch of
execAlterTableDropConstraint), internal/testport/
m0143_0002_fk_drop_constraint_nondefault_db_test.go (new — FK created/dropped
in db "r", COMMITted not ROLLBACKed, verified genuinely red pre-fix via git
stash), internal/testport/p0e5b_alter_drop_constraint_rollback_undo_test.go
(re-pointed the FK sub-test from its default-db workaround back onto db "r",
per that test's own documented resume point), .ralph/fix_plan.md (M0143-0002
[x] + Done note; new M0143-0002b filed with Parent: M0143-0002 line — the
lineage guard hard-rejects a new M0137-M0143/P0- task without one),
.ralph/deferral_ledger.md (1 new row), docs/design/0100-0149/
p0-e4-catalog-xmax-loss-repro.md (new "M0143-0002" section appended),
docs/design/README.md (p0-e4 row index title + long description updated).

Key symbols: DropForeignKeyConstraint (catalog.go) — the fixed method;
tableByOID(oid, dbOid) (catalog.go:6728) — the OID-relookup helper that was
wrongly hardcoded to DefaultDBOid, now bypassed entirely for this call site;
execAlterTableDropConstraint's FK branch (operators_ddl.go:13404) — already
validates the constraint against the live tbl.ForeignKeys slice before
calling in, so passing tbl directly needs no new nil/not-found handling.

Findings: the bug was ALREADY live-confirmed by the previous loop
(M0143-0008b) while writing its FK sub-test — this loop just implemented the
fix. Root cause: index registries (byTable) genuinely ARE stored per-DB
(`c.ns(dbOid).byTable[tbl.OID]` at every registration site, confirmed by
reading), so `HasPrimaryKey`/`dropIndexByName` (catalog.go:22214,:22261) share
the EXACT same `c.ns(DefaultDBOid).byTable[...]` hardcode shape as the FK bug
— very likely broken the same way for PK/UNIQUE/EXCLUDE constraints on a
non-default-DB table, but NOT live-tested this loop (scope discipline: only
fix what has an in-hand live repro). Filed as M0143-0002b. The original
M0143-0002 task text's "six deleteCatalogRowsForOID sites... never confirmed"
note was also not re-derived — deleteCatalogRowsForOID already takes an
explicit dbOid parameter at its ~15 call sites (grep'd), so whatever the "six
sites... same check" referred to needs its own re-identification, not
assumed to be the same class of bug.

Next step: banner item 0 still gates on P0-E7 (only unlocked once P0-E6 —
owner-run, [!] — completes); "while P0-E6 waits" sub-order unchanged: M0143
tasks whose gates don't need TPC-H data, then recon-only tasks (items 3-6),
then M-NIGHTLY. Remaining M0143 `[ ]` items not gated on TPC-H data:
M0143-0001 (in-process test crossing a DATABASE boundary — "highest leverage
of the six"), M0143-0002b (just filed this loop — natural next pick, same
subsystem/muscle memory, concrete hypothesis already in hand), M0143-0003
(pg_constraint 0-rows-after-restart), M0143-0004 (PhysicalTypeIsVarlena
IsArray), M0143-0005 (ParamRef LIMIT+DISTINCT), M0143-0006 (parser's 60
failing tests — same 60 seen again this loop's precommit gate, still
unowned). Re-check `## Current Priority` fresh next loop per the Precedence
rule before picking — do not just take this suggestion if the banner moved.

Gates run: `go build ./...` clean. `go test ./internal/executor/...
./internal/catalog/...` PASS. `go test -v -run
'TestPort_M0143_0002|TestPort_M0143_0008b|TestPort_P0E4|TestPort_P0E5'
./internal/testport/` PASS 9/9 (new test independently verified to fail
pre-fix via git stash of the 2 touched source files, pass post-fix — not
just green-by-construction). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — 60 pre-existing `internal/parser` failures
(M0143-0006, unrelated — same count/shape as the last two loops' runs, no
parser file touched), every other package PASS.
`scripts/tpcds-sf025-regression.sh sweep` (re-run against staged content
after the fix_plan Parent-line follow-up edit, so the gate stamp's code_tree
hash matches what was actually committed) — PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS.
`scripts/tpch-spotcheck.sh` and `scripts/tpch-acceptance-arm.sh off` — both
SKIP-BLOCKED (exit 3) by the `:65433` evidence hold, same accepted G6
exception as the last two loops' commits (ledger line in commit body names
P0-E7 as the re-run owner). Pre-commit hook's pgbench smoke: PASS (both
commit attempts — first rejected only by the fix_plan lineage guard on
M0143-0002b's missing Parent: line, fixed and re-committed clean).
`make ralph-state-guard`: found status/progress inconsistent from the
previous loop's clean-exit marker, auto-repaired (`progress reconciled to
in_progress`), then clean.

In-flight: none. No servers or background processes left running (all test
clusters use cluster.New's t.Cleanup).
