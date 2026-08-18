# Working set — M0134-0005x landed (PK/UNIQUE USING INDEX)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005x LANDED** (`2ee3a987`,
pushed). Parent case stays `[ ]`. Selected per the Current Priority banner
(M0134 after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at
run `20260818-005518`, **items: 0** — nothing to file.

**What landed (design §31):** `ALTER TABLE … ADD CONSTRAINT …
{PRIMARY KEY|UNIQUE} USING INDEX` was a **silent no-op** — the parser discarded
the index name and downgraded to `AlterTableNoOp`. Fixed in
`internal/parser/ast.go` (`UsingIndexName`), `internal/parser/ddl.go` (both arms
keep their Kind + parse trailing `DEFERRABLE`), `internal/executor/
operators_ddl.go` (new shared `adoptExistingIndexAsConstraint` +
`finishPrimaryKeyConstraint` extracted from the inline-PK tail). Rule-#2 twin
`ADD UNIQUE USING INDEX` shipped in the same change. 3 new guard tests.
**601 → 555 lines / 28 → 26 hunks.**

**Two things worth not re-learning:**
- **Carried causal attributions decay faster than carried line counts.** The
  census filed this as an executor catalog-sync gap in the "`Nullable`-blank
  family"; by the time it was selected, paths 1 and 2 were already fixed by
  M0134-0005o/-0005v and path 3's cause was a *parser discard* the executor
  never saw. A ~20-min read-only research pass before briefing saved a wasted
  implementation round. **Always re-verify the cause, not just reachability.**
- **The Rule-#2 twin paid, it wasn't hygiene.** Forecast was 24 lines / 1 hunk;
  fixing the sibling `ADD UNIQUE USING INDEX` in the same slice closed the
  `cnn_uq_idx` hunk too — 46 lines / 2 hunks actual, ~2× the forecast.

**Gates run:** `go build ./...`; `go test ./internal/parser/ ./internal/executor/`;
3 new guard tests PASS (FAIL-pre); `TestPort_.*(Constraint|NotNull|Index)` PASS
(35s); `scripts/pg-regress-runner.sh constraints` **555/26**;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (~8m, cold
`internal/initdb` 430s); `scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**,
Rule #1); pgbench smoke via hook (12.6k TPS).

**Next step — baseline is now 555 lines / 26 hunks** (never compare to any
pre-2026-08-19 number). The full hunk census at
`tmp/ralph-handoffs/m0134-0005w-census/report.md` is now **two slices stale on
causes** — re-measure before briefing. Ranked candidates:
1. **Inline-PK-at-CREATE-TABLE `attnotnull` heap desync** (ledgered
   2026-08-19) — 2 hunks; mirror M0134-0005o's heap resync into CREATE TABLE's
   own inline-PK arm. Smallest, most isolated, cause already traced.
2. **Inherited-CHECK-enforcement family** (~82 lines / 2 hunks) — the most
   *consequential* correctness bug left (inherited CHECK not enforced on child
   INSERT) but bundles ≥5 sub-bugs; needs its own research loop, not a slice.
3. "merging column" NOTICE family (~10 lines / 4 hunks) — mechanical, but the
   emitting call site for the plain-INHERITS redeclaration case is still unpinned.
**Not selectable** (ledgered, zero payoff): identity `NOT VALID` (blocked on the
unimplemented `ADD GENERATED`), `ATExecValidateConstraint` recursion, the CHECK
half of `MergeConstraintsIntoExisting`, the 15 lock `ee.Pos` sites, FK `:11600`,
the circle/GiST opclass cascade (67 lines, out of theme).

**Delegation:** `tmp/ralph-handoffs/m0134-0005x-pknn/` (researcher
`a9386270d8655f964`, 1 round, DONE — reclassified the root cause) and
`tmp/ralph-handoffs/m0134-0005x-usingidx/` (implementer `ad2706e15dec9e7f0`,
1 round, DONE, no deviations; tester `a41a7827dceb9dd12`, both gates PASS).

**In-flight:** none.
