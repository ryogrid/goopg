Task: M0122-0007 4e follow-up — `catalog.UserCollation` cross-database
isolation (fix_plan.md, appended right before "## Archived — complete
(completed_fix_plan_009.md)"). COMPLETE and committed (9764d37e).

Files: internal/catalog/catalog.go (UserCollation.DBOid field;
CreateCollation/DropCollation/RenameCollation/SetCollationOwner/
SetCollationSchema/CollationAttrsByName gained trailing `dbOid ...uint32`;
new ListUserCollationsForDBOid/PGCollationRowsForDBOid); internal/executor/
context.go (PgCollationRows field); internal/executor/operators.go
(pg_collation dispatch branch); internal/executor/operators_ddl.go (8 call
sites thread catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)); internal/
server/dispatch.go (pgCollationRowLister interface + wireExtensionRows
wiring); internal/catalog/create_collation_test.go (new
TestCreateCollationCrossDatabaseIsolation).

Key symbols: catalog.UserCollation.DBOid, catalog.PGCollationRowsForDBOid,
executor.Context.PgCollationRows, dispatch.pgCollationRowLister.

Hypothesis/Findings: M-NIGHTLY queue was fully empty this loop (confirmed
via ci/logs/action-items.md — same run 20260715-010036, all 11 items closed
by the prior loop). Followed the Current Priority banner's M0110/M0119
work order. M0119-0004/0005/0006/0007's "official" fix_plan bullets are
stale (frozen ~2026-07-07) but a MUCH more active, unindexed narrative
(M0122-0007's per-database catalog namespace epic, docs/design/0122-0018)
has continued through 40+ follow-ups since. Used
TestPort_PgDumpConnectionSetup's soft DU-002 round-trip probe as a live
oracle for "what's the next unscoped catalog object type" rather than
trying to reconstruct history from the deferral ledger/fix_plan text.
Fixed collations (this loop) — the probe's failure point moved from
`collation "builtin_coll" already exists` to `type "b_in" already exists"`
(a CREATE TYPE / pg_type collision). Ruled out: heapallindexed wire-up
(M0119-0006) looked bounded at a glance but the real missing piece
(visibility test + HOT-chain root-tuple attribution for index_form_tuple)
is genuinely large/risky for one loop — deferred that avenue, did NOT touch
internal/amcheck or operators_bt_index_check.go this loop.

Next step: **CREATE TYPE / pg_type cross-database isolation** — same audit
pattern as this loop (grep catalog.go for the user-defined-type registry
backing pg_type, likely `c.userTypes`/similar flat map; add DBOid field +
thread dbOid through Create/Drop/Lookup + a per-connection pg_type row
lister, mirroring this commit exactly). Re-run
`go test -v -run '^TestPort_PgDumpConnectionSetup$' ./internal/testport/`
after to confirm the probe's failure point moves again (or fully passes).
Remember: per-role `datconnlimit`-style residuals and WAL-persistence gaps
are OK to defer (ledger row) if scope grows — keep ONE loop = ONE object
type, matching the M0122-0007 4e follow-up numbering precedent.

Gates run (all PASS this loop): go build ./...; go vet (catalog/executor/
server/initdb); go test ./internal/catalog/... ./internal/server/...
./internal/initdb/... ./internal/executor/...; go test -short full repo
excl. testport (1 unrelated pre-existing flake in internal/access/btree's
TestConcurrentInsertSearch, reproduced clean 3/3 standalone — not caused by
this diff); scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh — first attempt hit 1
transient pgbench failure (0.009%, unrelated), retry PASS clean (0 failed,
3 workloads); make ralph-state-guard — auto-repaired 1 stale marker
(previous loop's clean-exit progress.json), consistent after.

In-flight: none — task fully committed, no abandoned gates/processes.
Untouched foreign/stray files present at loop start and still present
(analysis/tpch-explain-baseline.md, ci/logs/launch.log, postgres submodule
dirty, weekly_loc.*, analysis/perf-optimize3/runs/*, kaitai-struct-dash*.txt)
— same as every prior loop, left alone (not part of this loop's diff).
