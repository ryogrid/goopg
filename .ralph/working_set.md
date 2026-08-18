# Working set — M0134-0005z landed (INHERITS never copied parent CHECK constraints)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005z LANDED** (`aae015bb`).
Parent case stays `[ ]` (465 lines / 22 hunks still diverge). Selected per the
Current Priority banner (M0134 after M-NIGHTLY). M-NIGHTLY drained:
`ci/logs/action-items.md` still at run `20260818-005518`, **items: 0**.

**What landed (design §33):** plain `INHERITS` never folded the parent's CHECK
constraints onto the child, so they were **never enforced on child INSERT** — a
silent integrity hole, same class as 0005e/0005h/0005l. Enforcement
(`operators_fk.go:1700 checkConstraints`) was already correct and data-driven;
only population was missing. `execCreatePartitionChild` (`PARTITION OF`) had
always done it right and was the template. Bundled with the inseparable
column-level CHECK auto-name fix (`autoCheckName`, PG's distinct-referenced-column
rule) — the fold alone leaves the diff red on names. **516 → 465 lines / 23 → 22
hunks.**

**Three things worth not re-learning:**
- **A fold like this exposes latent flags.** Two flags had never mattered because
  nothing ever inherited a CHECK: `CHECK … NO INHERIT` was parsed but persisted as
  hardcoded `false`, and `AddCheckInherited` drops `NotEnforced`. Both would have
  shipped as NEW bugs caused by the fix. When wiring a propagation path for the
  first time, audit every flag on the propagated entity.
- **Fix at the call site, not the helper signature,** when the helper's other call
  sites are out of scope — then ledger the gap (done: `NotEnforced`/`NotValid` on
  the two partition call sites).
- The two affected hunks also carry an unrelated `SYS_COL_CHECK_TBL` bug, so they
  shrank without clearing. Expected, not a failure.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(~8 min; `internal/initdb` cold at 441s); `scripts/tpch-spotcheck.sh` PASS
(**Q12=2, Q13=35**); `go test ./internal/executor/ ./internal/parser/` PASS;
`TestPort_CreateTable` 10/10 PASS; pgbench smoke via hook (12.2k TPS select-only).

**Next step — baseline is 465 lines / 22 hunks** (never compare to a pre-2026-08-19
number). No fresh census exists at this baseline; the last one
(`tmp/ralph-handoffs/m0134-0005w-census/report.md`) is now FOUR slices stale on
causes — **re-census before briefing the next slice**. Known live candidates:
1. **`SYS_COL_CHECK_TBL`** — system-column (`tableoid`/`ctid`) references inside a
   CHECK expression are not validated; confirmed still present, shares the two
   hunks 0005z shrank. Own slice, unrelated to inheritance.
2. `pg_get_partition_constraintdef` missing builtin (diff ~337-368) —
   introspection, not enforcement.
3. Remaining known-but-unsliced: sequence-schema-qualification in defaults,
   deferred-PK index-option rendering, NOT-NULL-footer gaps on `PARTITION OF`,
   the "merging column" NOTICE family (~10 lines / 4 hunks; emitting call site for
   the plain-`INHERITS` redeclaration case still unpinned).
**Not selectable** (ledgered, zero payoff / zero fixture reachability): the
`ALTER TABLE … INHERIT` CHECK-merge and `ADD CONSTRAINT CHECK` inheritance-cascade
gaps (new 2026-08-19), `AddCheckInherited` `NotEnforced`/`NotValid` on the two
partition sites (new), identity `NOT VALID` / `ADD GENERATED`,
`ATExecValidateConstraint` recursion, the 15 lock `ee.Pos` sites, FK `:11600`,
the circle/GiST opclass cascade, the `execCreateTable` single-sync restructure.

**Delegation:** `tmp/ralph-handoffs/m0134-0005z-inhcheck-research/` (researcher
`a797449bdb3146834`, DONE — decomposed the family and correctly shrank the carried
82-line estimate to ~50-58), `tmp/ralph-handoffs/m0134-0005z-impl/` (implementer
`af2271a1e9cf3fb58`, DONE, 2 documented deviations; tester `a2e9b03d6a3ce09b6`,
1 round, all gates PASS).

**In-flight:** none.
