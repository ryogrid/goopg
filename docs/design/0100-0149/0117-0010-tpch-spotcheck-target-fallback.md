# 0117-0010 — TPC-H spot-check gate: persistent-data-target fallback

Status: **accepted (enabler; NOT an M0117 sub-task closure)**
Date: 2026-06-29
Relates: M0117 (CLOG live-path tail gate), [0117-0009](0117-0009-clog-slru-backfill-batched-startup.md)

## Problem

`scripts/tpch-spotcheck.sh` is the mandated pre-commit gate for every
executor/planner/codec change — "fresh server restart + Q12/Q13 canonical
row-count check" — the project's defence against silent row-count regressions
(Hard-won Rule #1). [0117-0009](0117-0009-clog-slru-backfill-batched-startup.md)
cleared a startup *fsync storm* so the server reaches the script's readiness
window. But the gate still never ran the actual Q12/Q13 check: it **silently
SKIPped** on a fully-loaded 2.2 GB data dir.

### Root cause (a goopg persistence property, surfaced by the gate)

The schema probe connects as `user=tpch / db=tpch / password=tpch`
(`bench/tpch/env_goopg.sh`, the HammerDB load identity). On the *fresh restart*
the gate performs, **none of those exist**:

- goopg registers `CREATE ROLE` / `CREATE USER` **in memory only** — they do not
  survive a server restart and are never written to `pg_authid`
  (`internal/server/role_ddl.go`, M0095-0006 v0 handler).
- The `tpch` *database* is likewise gone after restart (only
  `template1` / `template0` / `postgres` remain in `pg_database`).

The HammerDB load nonetheless persists: its tables are written into the
**`postgres`** database — the only user-visible database goopg keeps across
restart. Verified on the bench dir:
`select count(*) from lineitem` in `postgres` = **5,999,786**.

The probe failed with `role "tpch" does not exist`, whose substring
`does not exist` matched the script's *table*-missing heuristic
(`grep -qiE 'does not exist'`), so the gate reported "schema not loaded" and
SKIPped — masking a perfectly good, fully-populated data dir. Net effect: the
gate had **never** actually run since the in-memory role/db were lost, so
loops #7/#8 reported the whole M0117 live-path tail BLOCKED on an
"un-runnable gate" that was in fact only mis-probing.

## Fix

Make the gate resolve its data target instead of hard-coding the
(non-persistent) tpch identity. In `scripts/tpch-spotcheck.sh`:

1. Probe `lineitem` against the configured `tpch` target first (forward-
   compatible if goopg ever gains durable role/db persistence).
2. On a **`(role|database) ... does not exist`** error specifically (distinct
   from a relation-missing error), fall back to the **superuser + `postgres`
   database** — the persistent target — and re-probe.
3. Only a genuine `relation ... does not exist` (in the fallback target) is the
   real "not loaded" case → SKIP.
4. The Q12/Q13 runner is invoked against the *resolved* target
   (`GATE_DB`/`GATE_USER`/`GATE_PASS`), not the literal `TPCH_*`.

A `data target = <user>@<db>` line is echoed so a reader can see which database
the counts came from.

This is a **test-infra/script** change only — no engine, parser, planner,
executor, codec, or storage code is touched; blast radius is the gate script.

## Verification

Full gate, fresh start, on the bench data dir:

```
tpch-spotcheck: tpch role/db absent post-restart (in-memory only); falling back to postgres@postgres
tpch-spotcheck: data target = postgres@postgres
Q12: OK elapsed=27.30s rows=2
Q13: OK elapsed=91.61s rows=33
tpch-spotcheck: Q12 PASS — rows=2 (expected 2)
tpch-spotcheck: Q13 PASS — rows=33 (expected 33)
tpch-spotcheck: RESULT=PASS
```

Q12=2 / Q13=33 match `bench/tpch/spotcheck_expected.env` exactly, which also
confirms **HEAD has no row-count regression**. The gate is now functional:
an executor/planner change can finally be checked against real Q12/Q13 counts
rather than a masked SKIP.

## Consequence for the M0117 live-path tail

Combined with 0117-0009 (startup-hang) this removes the last *gate-runnability*
blocker. M0117-0006 Part B (CLOG store swap) / 0117-0007 Part B (async commit)
remain dedicated-full-gate-session work (they additionally need the
heterogeneous PG-standby E2E + `-race` mvcc/wal + crash-replay per
[0117-0006](0117-0006-clog-slru-buffer-pool.md) §"Mandatory gates"), but the
populated-data Q12/Q13 component of that gate is now demonstrably runnable.

## Follow-ups (not this loop)

- The underlying gap — goopg does not persist user-created **roles** or
  **databases** across restart — is a real feature gap (in-memory v0 handlers).
  A durable `pg_authid` role write + durable `CREATE DATABASE` replay would let
  the bench reload land in a dedicated `tpch` role/db (matching upstream PG) and
  let the gate use the configured identity directly. Tracked here as context;
  not in any current milestone's actionable band.
