Task just completed: **M0134-0157 — psql_pipeline.sql**, PARKED with a contained
fix (`not-tried` → `failed`). 357 → 291 diff lines, `^+ERROR` 22 → 15.

Only TWO root causes — rare for M0134, because one cascades over ~85% of the file:

1. **(PARKED, REFACTOR-tier) goopg's extended protocol has no implicit
   transaction block.** Execute auto-commits (`dispatch_extended.go:171-201`)
   and `MsgSync` (`server.go:1820`) never touches `connTx`. PG never commits at
   Execute: `XACT_FLAGS_PIPELINING` (postgres.c:2317) → `BeginImplicitTransactionBlock`
   on the NEXT `start_xact_command` (xact.c:4326) → commit at Sync after
   `EndImplicitTransactionBlock` (postgres.c:4968). So PG's FIRST command in a
   message group is NOT in a block and every later one IS, and a mid-group
   `BEGIN` converts the block in place so `ROLLBACK` undoes earlier statements.
   goopg commits the pre-BEGIN INSERT → next pipeline hits a duplicate key in an
   explicit block whose COMMIT is (correctly) skipped-until-Sync → session
   aborted for ~300 lines. goopg already has the SIMPLE-path analogue
   (`dispatch.go:1071`); design doc carries a 5-step port.
2. **(LANDED) Bind/Describe protocol-error text parity** — exact upstream
   strings incl. PG's unnamed-prepared-statement special case
   (postgres.c:1671/:2669); both sibling lookups now share
   `missingPreparedStatement`.

Also landed (crash found by the gate, contained not root-fixed): `stmtSQL`
(`dispatch.go`) sliced `sql[28:0]` and **panicked the backend / closed the
client socket** for `CREATE TABLE …; PREPARE …; ALTER TABLE … ADD COLUMN …;
EXECUTE …` because `AlterTableStmt.Pos()` is 0. Clamped + pinned; this also
un-breaks the pre-existing red `TestPrepareExecuteRejectsResultTypeChange`.

Files: internal/postmaster/extended.go, internal/postmaster/dispatch.go,
extended_bind_message_parity_test.go (new), stmt_sql_position_guard_test.go
(new), docs/design/m0134-0157-extended-implicit-transaction-block.md (new) +
README index, CSV + regen-testport, fix_plan, deferral_ledger (4 rows).

Gates run: `internal/postmaster` full package **PASS** (was 1 pre-existing FAIL
at HEAD); `RALPH_PRECOMMIT_SCOPE=units` PASS (exit 0); 6-file regress A/B
(prepare / psql_pipeline / plpgsql / transactions / select / prepared_xacts) —
no regressions, only psql_pipeline moved; `make check-testport-inventory` +
`make regen-testport` PASS; `make ralph-state-guard` OK; commit-hook pgbench smoke.

In-flight: none of mine (throwaway server /tmp/gp5533 stopped).

**Carried obligations (same two as last loop — nightly 20260828-235424 was still
running for this entire loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for M0134-0156 and now -0157). Once the
   nightly is done: `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are still unadjudicated** —
   read `ci/logs/20260828-235424/testport/results.csv` when it finishes.
   NOTE: that run's **testport stage looks WEDGED** — started 23:54, go-test.log
   last written 23:59, still no growth at 01:40 (matches the known isolation
   wedge); its 120m timeout fires ~01:54.

NEXT LOOP: file any new `## AI-` items (action-items.md is still the 20260827
file), then per the Current Priority banner work **M0134-0158 (publication.sql)**.
Two smaller filed follow-ups exist if a short loop is wanted: M0134-0157a (parser
`Pos()==0`, needs the goyacc playbook §12 read) and M0134-0157b (non-deterministic
function overload resolution — plpgsql regress alternates 4401/4402 lines).
