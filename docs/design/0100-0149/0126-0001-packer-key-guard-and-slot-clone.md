# 0126-0001 — packer key-count guard, `Materialize` arena clone, and the pinned R0 baseline

| field | value |
| --- | --- |
| status | superseded |
| superseded by | [leftdeep-joins/](../leftdeep-joins/) — MHJ retired (M0127) |
| date | 2026-07-31 |
| task | M0126-0001 |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage −1 / −1b, **08** R1 / R3c — read them first; this doc does not restate them |
| depends on | nothing — this is the milestone's first task |

## 1. Scope

Three deliverables, in this order: (1) capture the **R0 baseline** — a timed
22-query TPC-H SF1 run at default config plus `make plan-snapshot-capture
LABEL=m0126-base` — committed **before** any code lands, because R0 is the
denominator of the milestone's acceptance bar and must predate every change;
(2) the packer's missing key-count assertion (fail-closed: declines to pack an
under-keyed plan that today silently NULL-pads unreached tables); (3) make
`VirtualSlot.Materialize()` clone arena-backed Datums like its three siblings
do. (2) and (3) are pre-existing correctness defects found while auditing the
bundle; they are independent of the proposal and must not wait for it.

## 2. Files and symbols touched

| file | symbol | change |
|---|---|---|
| `internal/planner/bushy.go` (`collectMultiHashTables`, immediately before probe selection) | packer | add `if len(keys) != len(scans)-1 { return nil, nil, 0, nil }` |
| `internal/executor/slot.go:167-169` | `VirtualSlot.Materialize()` | clone arena-backed Datums (`cloneRowOwned` path), matching `drainRowsBounded` (`spill.go:384-402`, the M0073-0004 retention boundary) |
| `analysis/cost-driven-second-try-200731/evidence/r0-baseline.txt` | — | new artefact (timed run, host/env recorded) |
| `plan_snapshots/m0126-base.txt` | — | new snapshot, `LABEL=m0126-base` |

## 3. Commit split

1. R0 baseline artefacts (docs/evidence only — must be the parent of the code
   commits in git history).
2. Packer guard (planner, 2 lines).
3. `Materialize()` clone (executor). Correctness change — never folded into a
   performance commit; each independently revertible.

## 4. Gates

Per code commit: UNITS, SMOKE, SPOT, PLAN, DS05.

- Commit 2 expectation: all green. **If PLAN shows a diff, that query was
  returning wrong rows before the guard** — record it prominently as a bug fix,
  not as noise.
- Commit 3 expectation: zero plan diffs (executor-only).

## 5. Stop / decision conditions

Unconditional. R0 capture protocol: quiet host verified (record `uptime` /
load), cgroup-capped server via `scripts/goopg-test-run.sh`, fresh server,
per-query times + total recorded with the HEAD SHA. If the host cannot be made
quiet, wait — a noisy R0 poisons every later bar verdict.

## 6. Rollback

Bundle 10 §1: the guard reverts as 2 lines (only with evidence — the revert
restores a silent-corruption path); the clone reverts as one commit. Any revert
preserves its failing artefact under `evidence/` (10 §5).

## 7. What this doc deliberately does not decide

The magnitude of the seam-copy cost (Stage 0's measurement, task -0005), and
whether the packer guard ever fires on real TPC-H/TPC-DS plans (the gates
answer it).
