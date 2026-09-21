(idle — nothing in flight)

# Loop #60 result — M0145-0003 CLOSED; M0145-0001 ESCALATED `[!]`

Banner selection: items 0/1/2 have no open tasks, so item 3's first
selectable task was **M0145-0003**. That also retires the banner-ordering
escalation carried since loop 48 — 0009-0018 are done, so 0003 was
unambiguously first and nothing was out of order.

No production code changed this loop. Docs/tracker only.

## M0145-0003 closed on a CLASSIFIED census
Knob-arm TPC-DS SF0.25 capture, both census channels
(`tmp/jtcap-m3-loop60/`, `tmp/jtcap-m3-l60t/`). Cumulative over the arms
landed in earlier loops: `(pulled)` 9->27, `InExpr` 34->1, seam
`leaf-count` 26->10, `D1-sublink` 8->5.
The reason to close is that all 60 fires are **classified**: 27 pulled /
15 CTEScan-leaf blocked on B-06 (M0145-0009) / 15 `SubqueryExpr@scalar`
+ 3 OR-position, which are NOT gaps because PG declines those shapes too
(prepjointree.c:877). `IN`/`NOT IN` is DONE.

## The important outcome: lineage budget exhausted
The lineage check REFUSED the two residue tasks I tried to file, because
M0145-0001's last five completed descendants (0006, 0014, 0015, 0016,
0017) all carry `Movement: none`. Per its instruction I:
- marked **M0145-0001 `[!]`** and wrote a full escalation block into it
  (what was attempted, what each step proved, the blocker, expected
  movement if unblocked, remaining size, plus the deferred scope that
  could not be filed as tasks);
- withdrew the two tasks and repointed every reference (ledger, design
  doc, design README, 0003's own note) at the escalation block;
- corrected M0145-0003's `Movement:` to **none** — this loop changed no
  code, and the census counters are not one of S3's three instruments.

**The claim the owner needs to rule on:** the jointree/pull-up front end
is no longer the binding constraint. The walls are (1) B-06 CTE-output
statistics and (2) the COST MODEL (what M0145-0018's no-go hit). Five
consecutive descendants improved the mechanism without moving plan parity.

## Gates
`go build ./...` OK; state guard OK; pgbench smoke via the commit hook.
No value gates required — zero production diff (verified with
`git status` over `internal/ cmd/ scripts/`).

## Next loop
M0145-0001 is `[!]`, so **every M0145 descendant is now non-selectable**
(0004, 0005, 0007, 0008, 0009, 0010 all descend from it). Banner item 3 is
therefore blocked pending the owner. Next selectable is item 4
(**M0141-S2a-fix2r**), then 5, 6, 7, 8, 9, and item 10's M-NIGHTLY items
(PgAmcheck003 x4, PgoutputInterop x10, TestPort_RegressSuite -016).

## Owner escalations OPEN — now TWO, both on M0145
1. M0145-0018's cost-model no-go.
2. **NEW:** M0145-0001 lineage budget exhausted (this loop).
