# Milestone 0124 — TPC-DS round-2 closeout: measurement baseline, gate discharge & ledger debt

**Status:** planned
**Filed:** 2026-07-28
**Reference plan:** `.ralph/fix_plan.md` (M0124 section)
**Parent audit:** `docs/design/tpcds-round2-fixes/README.md` §13 — implements §13.5 actions **1, 5, 6, 7**
**Sibling:** `docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md` (§13.5 actions 2, 3, 4)
**Branch:** `tpcds-fix2` (audit point `5db0a067`; branch tip at filing `0f16c2e7`)

## Goal

Round 2 landed eight of twelve planned phases but did not land the **evidence**. This
milestone produces the instruments M0125 is judged with. It changes no file under
`internal/`.

Four debts, each recorded in §13:

- **§13.3 — the current status is a projection, not a measurement.** The last dual-engine
  SF=1 sweep predates RC-1b (`5db0a067`), and the two SF0.5 sweeps ran at **different budgets**
  (300 s vs 180 s), so they are not comparable for the timeout class. The later of the two is
  also mis-provenanced — its header names RC-1b's *parent* commit because it ran from an
  uncommitted tree (see `0124-0001`).
- **§13.4 item 4 — a regression-gate hole.** `9740fce9` (phases 1.2/1.3) rewrote
  column-index remapping after join reorder (`remapByPosMap`) and the bindings position map
  (`buildBindingsPosMap`) — the two passes whose failure mode is a silent wrong answer — while
  `scripts/tpch-spotcheck.sh`
  reported SKIPPED and `make plan-gate` was never run. `ef4a65a5` has since restored the
  cluster; the retroactive run has never happened, and `plan_snapshots/` still holds
  nothing from this round.
- **§13.2 — ledger debt.** Seven of nine planned deferral rows were never appended; one
  more is moot and needs an explicit disposition rather than silence.
- **§13.1 phase 4 — Q35 has never produced a goopg row count.**

Plus one instrument the audit identified but did not schedule: the SF0.5 oracle is
row-count only, and M0125's acceptance depends on catching value corruption.

## Why these are one milestone

They share a single property: **none of them can be trusted to report on themselves.** A
sweep cannot validate its own budget, a gate cannot discharge its own absence, and a
row-count oracle cannot see a value defect. Grouping them means M0125 inherits a complete,
self-consistent instrument set rather than assembling one task by task.

Repo precedent for this cut: M0120 (verification execution) / M0121 (remediation).

## Required Design Docs

| Task | Content | §13.5 | Design doc |
|---|---|---|---|
| **M0124-0001** | SF=1 dual-engine re-sweep at HEAD, uniform 600 s, fresh server after every timeout; ports the missing orphan reap. Pins the baseline commit for M0125. | 1 | `0124-0001-tpcds-sf1-head-resweep-protocol.md` |
| **M0124-0002** | Retroactive TPC-H A/B + plan-snapshot baseline for `9740fce9`'s ungated hunks; commits the first round-2 plan baseline. | 5 | `0124-0002-retroactive-tpch-plan-gate-discharge.md` |
| **M0124-0003** | Append the seven missing §10 ledger rows, an explicit *drop* disposition for the moot one, a `pq-P10` cross-reference, and five rows the audit itself produced. | 6 | `0124-0003-round2-deferral-ledger-completion.md` |
| **M0124-0004** | Recover or classify Q35's row count. | 7 | `0124-0004-q35-rowcount-resolution.md` |
| **M0124-0005** | Add a value checksum to the SF0.5 oracle, so the gate can see the defect class Q75 exposed. | (§13.4 item 3) | `0124-0005-sf05-oracle-checksum-column.md` |

**Order.** 0001, 0002 and 0003 are independent. 0005 should precede M0125-0002 and M0125-0004,
whose acceptance is by value rather than row count.

**0004 is not free-floating**, despite running one query: its deliverable is a row in 0001's
report, and `scripts/tpcds-sf05-regression.sh`'s `guard_sf1_sweep` physically refuses to start
while the SF=1 sweep harness is active. Run it **after** 0001, from a freshly started server
with no prior query in the process.

**No M0125 commit may land inside 0001's 8–10 h sweep window** — `0124-0001` D1 requires the
sweep commit to be an ancestor of every M0125 commit. M0125-0004 is the live risk here, since
M-NIGHTLY may force it; see that milestone's "scheduling conflict" section.

## Definition of Done

1. `analysis/tpcds-sf1-goopg-<date>.md` exists, recording the goopg commit, the uniform
   budget, the pre-sweep S-cold proof, the defect table, and a confirm/refute line for
   each projection listed in `0124-0001` §D7.
2. `analysis/tpch-tpcds-round2-retro-<date>.md` exists with a per-query TPC-H table for
   both arms on the same rebuilt cluster, plus the arm-to-arm plan-diff verdict.
3. `plan_snapshots/tpcds-round2-head.txt` is committed, and was captured **last** in the
   local working tree so `plan-gate` resolves to it there. Note this is a property of the
   machine, not of the commit — git does not preserve mtime, so on a fresh clone the ordering
   is arbitrary. That is precisely why every gate in M0125 uses
   `make plan-diff LABEL=tpcds-round2-head` rather than `plan-gate`
   (`Makefile`, `plan-gate:` → `ls -t "$(REPO_ROOT)/plan_snapshots"/*.txt | head -1`).
4. `.ralph/deferral_ledger.md` contains the seven §10 rows, the drop row, the `pq-P10`
   UPDATE, and the five audit rows; rendering verified via `gh api --method POST /markdown`.
5. Q35 is classified *performance-only*, *wrong answer*, or — if it does not complete at the
   extended budget — *timeout-class with the row count still unknown*, with the evidence
   recorded either way. The third outcome is a legitimate result, not a failed task, but it
   must be stated explicitly so "M0125-0003 fixed Q35" cannot later be asserted unfalsifiably.
6. `oracle.txt` carries a checksum column and the gate reports `CKMISMATCH` distinctly from
   `MISMATCH`; the re-captured row counts are proven identical to the current fixture; and the
   pre-RC-1b Q75 case is re-run and its outcome recorded (`CKMISMATCH` is the expected result,
   but see `0124-0005` D5.3 — it is a hypothesis about the `LIMIT` window, not a guarantee).
7. `docs/design/README.md` indexes all five design docs; `docs/milestones/README.md`'s index
   row is updated; this file's status is `accepted`.

## Out of scope

- **Any engine change.** A code change landing mid-sweep voids the sweep. If a task
  uncovers a defect it files a ledger row and an M0125 blocker; it does not fix it.
- Flipping any planner flag or default.
- `ci/batch/tpcds-row-anchors.csv`'s own checksum column — same blindness, but it is a
  separate pinned CI fixture; ledger row from M0124-0003.

## PostgreSQL References

None. This milestone measures goopg; the PG 18.3 reference cluster is used only as the
row-count and checksum oracle.
