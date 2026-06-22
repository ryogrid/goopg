Task: M0118-0005 — FK / referential-integrity concurrency group. PARTIAL win
landed this loop (5 of 9 specs promoted). Group stays OPEN. Committing.

WHAT LANDED (NO engine change — promotion only, design 0118-0023):
- Probe (throwaway zz_probe_test.go, RunAndCompare per spec, ranked by
  first-divergence) showed 5 specs already match PG 18.3 byte-for-byte:
  referential-integrity, temporal-range-integrity, fk-snapshot, fk-contention,
  fk-deadlock2. The FK FOR-KEY-SHARE-vs-non-key-UPDATE non-conflict rides the
  M0118-0003/0004 multixact lock-only + no-key-update producer; SSI specs ride
  the 40001 anomaly machinery.
- Promotion: switched 3 pre-existing dedicated tests soft runIsoSpec →
  runIsoSpecStrict (FkSnapshot, ReferentialIntegrity, TemporalRangeIntegrity) +
  added TestPort_IsolationFkContention / TestPort_IsolationFkDeadlock2.
- Files: internal/testport/isolation_port_test.go (3 switches + 2 new funcs),
  docs/test-port/postgres-oracle-port-status.{csv,md} (D-002 narrative + regen),
  docs/design/0118-0023-*.md + README row, .ralph/deferral_ledger.md (1 row),
  .ralph/fix_plan.md (M0118-0005 PARTIAL note, still unchecked).
- Removed throwaway zz_probe_test.go after use.

Gates (green): 5 promoted strict tests PASS (~12s); go vet clean;
make ralph-state-guard OK (auto-repaired prev clean-exit marker); pgbench smoke
via pre-commit hook at commit.

DEFERRED (4, ledger 2026-06-22) — group can't close until these land:
- fk-deadlock: goopg's FK-check FOR KEY SHARE on the *wait* path over-conflicts
  (s2i INSERT-into-child blocks+times-out where PG proceeds). Need a
  non-conflicting KEY-SHARE multixact join on the wait path (producer twin of
  fk-deadlock2's grant path); then the deadlock forms on the parent UPDATEs
  (detector already exists). CLOSEST of the four — engine work but bounded.
- ri-trigger: fire user RI constraint-triggers (spec installs explicit CREATE
  TRIGGER raising "child row exists"); goopg never fires it.
- fk-partitioned-1/2: ALTER TABLE … ATTACH PARTITION unimplemented
  (`table "pfk1" does not exist`) + partitioned-parent FK enforcement.

NEXT loop candidates: fk-deadlock (closest, ~1 engine slice) to advance this
group; OR M0118-0007 eval-plan-qual/drop-index-concurrently-1 (planner RETURNING
+ EXPLAIN EXECUTE plan-format); OR probe M0118-0008 DDL/VACUUM for any
already-passing strict-promotable specs.

GOTCHAS: isolation specs run goopg as a SUBPROCESS. CSV rationale must be
comma-free inside a field. tpch-spotcheck INFRA-BLOCKED; pgbench smoke is the
live guard. never gofmt -w (go1.25 repo vs local 1.26). Untracked postgres/ +
weekly_loc.* + requirements.txt are stray — leave them.
