(idle — nothing in flight)

## Loop summary (2026-07-11, loop #53)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — closed `unimplemented_feat.json` "goopg does not emit a blank data row
for 0-column SELECT results".** Verify-before-implement traced it to a
sibling-path divergence in the EXTENDED query protocol. The simple-query path
(`internal/server/dispatch.go`, `schema != nil` guards) already emitted a
0-field RowDescription + one zero-column DataRow per source row for
`SELECT FROM t`/`SELECT;`. The extended path dropped them:
- `dispatch_extended.go` `executeExtendedQueryViaExecutor` gated `res.Fields`
  build AND per-row `res.Rows` append on `len(schema) > 0` → zero-column read
  produced no rows, command tag `SELECT 0`.
- `extended.go` `describeViaPlanner` + `handleDescribeFrame` collapsed
  `len(schema)==0` to NoData instead of RowDescription-with-0-fields.
Fix = gate all four sites on `schema != nil` / `schema == nil` (nil ⇒
write/DDL/txn ⇒ NoData; non-nil zero-length ⇒ zero-column result set). Protocol
writers already encode empty slices as count-0 frames.

Verified vs live PG 18.3 (`SELECT FROM t` over 3 rows → 3 rows via `\bind \g`;
`SELECT;` → 1 row). Tests: `internal/server/extended_zero_column_test.go`
(`TestExtendedZeroColumnSelectEmitsRows`, `TestExtendedZeroColumnSelectWithFilter`);
non-vacuous (reverting the row guard → `DataRow count=0, want 2`). Design:
`docs/design/0122-0004-extended-zero-column-rows.md` + README row.
`unimplemented_feat.json` entry → resolved. No PG behavior left → no ledger row.

Gates: `go build ./...` clean; `go vet ./internal/server` clean; full
`internal/server` + `internal/protocol` suites PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard OK.

Next loop: M0122-0004 still-open tail is RANGE window frame mode (needs
per-ORDER-BY-column type-aware +/-/< operator lookup) + sub-day intervals; or
pick another open unimplemented_feat item (92 open).

In-flight: none
