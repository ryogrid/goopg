Task: (loop #10 — design 0117-0010) Made the mandated TPC-H spot-check gate
ACTUALLY RUN. Loop #9 fixed startup-hang→readiness; this loop fixed the
silent-SKIP so Q12/Q13 are compared for real. Committed this loop.

What landed:
- `scripts/tpch-spotcheck.sh`: data-target fallback. The gate probed the
  `user=tpch / db=tpch` HammerDB load identity, but goopg registers
  CREATE ROLE/USER + CREATE DATABASE **in-memory only** (role_ddl.go), so the
  tpch role/db DON'T survive the gate's fresh restart. The tables persist in the
  **postgres** database (lineitem = 5,999,786). The `role "tpch" does not exist`
  probe matched the table-missing SKIP heuristic → silent SKIP on a loaded dir.
  Fix: on a `(role|database) ... does not exist` probe error, fall back to
  superuser@postgres and re-probe; run the runner against the resolved target.
- Files: scripts/tpch-spotcheck.sh, docs/design/0117-0010-*.md + README index,
  .ralph/fix_plan.md (M0117 enabler note; corrects 0117-0009's "reload role" note
  — data was never lost, only mis-probed).
- Empirical proof: full gate, fresh start → falls back to postgres@postgres →
  Q12: rows=2, Q13: rows=33 → RESULT=PASS (matches spotcheck_expected.env;
  confirms HEAD has no row-count regression).

Gates run: bash -n scripts/tpch-spotcheck.sh OK; FULL scripts/tpch-spotcheck.sh
RESULT=PASS (Q12=2/Q13=33); make ralph-state-guard OK (self-repaired); pgbench
pre-commit smoke (on commit). No engine code touched (script + docs only).

Next step (autonomous priority band remains genuinely exhausted):
1. With 0117-0009 + 0117-0010 the populated-data Q12/Q13 gate is now runnable,
   so the deferred M0117 live-path slices (0117-0006 Part B CLOG store swap per
   the blueprint in design 0117-0006 §"Part B implementation blueprint";
   0117-0007 Part B async commit) can be done in a DEDICATED full-gate session —
   they ALSO need heterogeneous PG-standby E2E + `-race` mvcc/wal + xlog_replay,
   which still SKIP in the autonomous WSL2 loop, so they are NOT autonomous.
2. M0118-0004 deadlock-parallel = infeasible (no lock-group abstraction).
   M0095-0003 = blocked on logical decoding. M0110 = PAUSED by directive.
3. Real goopg feature gap surfaced (not in any actionable band): durable
   role/database persistence (in-memory v0 handlers). Would let the bench reload
   land a persistent tpch role/db and let the gate use the configured identity.
