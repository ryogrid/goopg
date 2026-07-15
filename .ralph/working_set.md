(idle — nothing in flight)

Loop #43 landed and committed: fixed the `catalog.accessMethods` cross-
database isolation gap (DU-002 round-trip probe unblock), the next item in
the M0122-0007 4e follow-up series after loop #42's VARIADIC call-site
argument-collapsing fix exhausted the function/routine sub-series.

Note for future loops: `.ralph/working_set.md`'s own prior note (loop #42,
"Next DU-002 resume point ... c.schemas") was WRONG/stale by the time this
loop started — schemas were never actually the next collision (the `public`
schema pre-exists in every database, so the dump never re-issues `CREATE
SCHEMA public`). Ground-truthed by re-running `TestPort_PgDumpConnectionSetup`
directly instead of trusting the design doc's/working_set's last-recorded
probe state, which found the REAL current blocker was
`access method "goopg_am" already exists`. **Lesson: always re-run the DU-002
probe test live before trusting any doc's/ledger's/working_set's recorded
"next blocker" — it can go stale within a single loop once a sibling fix
(here, loop #42's VARIADIC fix) advances the probe past what was recorded.**

Fix (same established M0122-0007 4e pattern as ForeignServer/UserMapping/
UserCollation/Domain/Routines): `catalog.AccessMethod` gained a `DBOid
uint32` field; new `accessMethodKey(dbOid, name)` helper (mirrors
domainKey/enumKey/compositeKey/rangeKey, `"<dbOid>\x00<name>"`, no
case-folding since the pre-existing code never lowercased AM names);
`RegisterAccessMethod`/`DropAccessMethod`/`UserAccessMethodOID` gained a
trailing variadic `dbOid ...uint32` (resolved via the existing package-level
`resolveDBOid`); `RegisterAccessMethodDuringRecovery` normalizes a zero
`DBOid` to `DefaultDBOid` (WAL record carries no dbOid — startup recovery
still single-database). Threaded through all 3 live call sites in
`internal/executor/operators_ddl.go` (`execCreateAccessMethod`, the `DROP
ACCESS METHOD` branch, `execCommentOn`'s "access method" case) — all
mechanical `ddlOp`-method sites, no signature-cascade exceptions needed.

Files: internal/catalog/catalog.go (AccessMethod struct + 4 methods +
accessMethodKey helper), internal/catalog/create_access_method_test.go (new
— TestAccessMethodCrossDatabaseIsolation), internal/executor/operators_ddl.go
(3 call sites), docs/design/0122-0018-per-database-catalog-namespace.md (new
"Access method registry" section + status line update), docs/design/README.md
(0122-0018 row appended, surgical single-line Python edit — the row is a
73KB single line, do NOT attempt Edit-tool string matching on it), .ralph/
fix_plan.md (new [x] entry after the VARIADIC call-site-collapsing item),
.ralph/deferral_ledger.md (new open row recording the next blocker).

Key symbols: catalog.AccessMethod, accessMethodKey (new),
RegisterAccessMethod, DropAccessMethod, UserAccessMethodOID,
RegisterAccessMethodDuringRecovery, ListAccessMethods (all catalog.go);
execCreateAccessMethod, execCommentOn, the DROP-statement "access method"
case (all operators_ddl.go).

Deliberately NOT fixed this loop (recorded in ledger, matches the
Routines.List() precedent): `ListAccessMethods()` still has no dbOid filter
— its sole caller (pg_am's `VirtualRows` closure, a bare `func()` with no
per-connection dbOid in scope) can't supply one yet. Harmless today (no
existing test creates AMs across 2 distinct databases in one process
lifetime).

Next DU-002 resume point: confirmed via a LIVE re-run of the probe (not
doc archaeology) that the round-trip's failure point moved past the
access-method blocker to `operator 1(bigint,bigint) already exists in
operator family "op_family_loose"` — the operator-family/operator-class
registry (`CREATE OPERATOR FAMILY`/`CREATE OPERATOR CLASS`) is the next flat,
dbOid-less registry in the M0122-0007 4e series. Not yet located/measured —
start by grepping `internal/catalog/catalog.go` and
`internal/executor/operators_ddl.go` for `opFamil`/`OpFamily`/`opClass`/
`OpClass`/`CreateOperatorFamily`/`CreateOperatorClass`, apply the identical
DBOid-field + composite-key + variadic-param pattern, then re-run
`TestPort_PgDumpConnectionSetup` live to confirm the probe advances further
(do not trust any doc's recorded "next blocker" without re-running it fresh —
see the lesson noted above).

Housekeeping note (carried from loops #41/#42, re-verified): a concurrent
interactive `claude` session (pid 872994, pts/20) may still be running in
this same working tree, alongside pre-existing untracked
`Markdown_Table_Repair_Design_Doc.md`/`tools/mdtablefix/` (unrelated markdown-
table-repair tooling, not this loop's work) and modified-but-unrelated
`analysis/tpch-explain-baseline.md`/`ci/logs/launch.log`. All left untouched
and excluded from this loop's commit via explicit pathspec (only
.ralph/{fix_plan.md,deferral_ledger.md,working_set.md}, docs/design/{
0122-0018-per-database-catalog-namespace.md,README.md}, internal/catalog/{
catalog.go,create_access_method_test.go}, internal/executor/operators_ddl.go
were staged).

Gates run this loop (all green): `go build ./...`/`go vet
./internal/catalog/... ./internal/executor/...` clean; `go test
./internal/executor/... ./internal/catalog/... ./internal/parser/...` PASS;
`go test -short $(go list ./... | grep -v /internal/testport)` (full repo,
short mode, 0 FAIL, ~230s incl. internal/initdb's 230s); `scripts/
tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads); `make
ralph-state-guard` clean (auto-repaired the same benign stale-progress-marker
pattern as loops #36-#43).

In-flight: none. git push status (unrelated to this loop, carried from
loops #38-#42): local `wal-format-mod` was ahead of `origin/wal-format-mod`
by increasing amounts each loop; this loop adds one more commit on top. Do
NOT attempt to auto-resolve any push conflict — loop #38's working_set
(readable via `git log`) already spelled out 3 resolution options for the
user; wait for explicit human direction before any rebase/force-push.
