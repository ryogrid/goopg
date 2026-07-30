# M0125-0036 — EXISTS → ANY, evidence

Design: `docs/design/0125-0036-exists-to-any-hashed-subplan.md`.
All runs: SF=0.5, goopg `:65437` (binary `tmp/goopg-m0125-0036-bin`), PG 18.3
oracle `:65438` db `tpcds05` user `ryo`, quiet host, 2026-07-31.

## Acceptance

| query | before | after | rows | oracle rows |
|---|---|---|---|---|
| Q10 | TIMEOUT (>300 s) | 16.9 s | 0 | 0 (`10\|OK\|0\|1f18d650d205d71d\|11`) |
| Q35 | TIMEOUT (>300 s) | 14.0 s | 100 | 100 (`35\|OK\|100\|n/a\|0`) |
| Q69 (§C3's control) | 17 s | 17.3 s | 100 | 100 |
| Q30 | TIMEOUT | TIMEOUT (345 s) | — | 31 |
| Q81 | TIMEOUT | TIMEOUT (350 s) | — | 100 |

Q30/Q81 are §C3's correlated-scalar-aggregate variant; this pass declines both
(scalar sublink, aggregating body) and they are unchanged. Recorded so the
class's remaining half is not mistaken for closed.

## The isolating probe — why Q10 alone was not enough

Q10's oracle is **0 rows**, so a conversion that produces an empty value set
passes it. The first version of the pass did exactly that, and only Q35 (100
rows) exposed it. The probes below are the bisection that located it; each was
run against goopg and against PG and compared.

| file | shape | goopg (first version) | goopg (final) | PG |
|---|---|---|---|---|
| `probe35.sql` | `customer` + the OR-ed EXISTS pair alone | 11531 | 11531 | 11531 |
| `probe35b.sql` | `customer` + the AND-ed `store_sales` EXISTS alone | 12422 | 12422 | 12422 |
| `probe35c.sql` | `customer` + AND-ed EXISTS + OR-ed pair | 1354 | 1354 | 1354 |
| `probe35d.sql` | the 3-table join, no sublinks | 96562 | 96562 | 96562 |
| `probe35e.sql` | 3-table join + AND-ed EXISTS (→ SEMI) | 11996 | 11996 | 11996 |
| `p1.sql` | 3-table join (→ MHJ) + OR-ed pair, **no** SEMI | 11127 | 11127 | 11127 |
| `probe35f.sql` | Q35's full WHERE: MHJ + SEMI + OR-ed pair | **0** | **1294** | 1294 |

The fault needed MHJ **and** SEMI together: `MultiHashJoin` packing re-sorts
its output schema by OID and treats a sublink body as opaque, so the operand
index taken verbatim from the body's `OuterColumnRef` was stale. Fix:
`resolveHostOperandIdx` re-resolves it against the row the qual is evaluated
against (design §5.1).

## A separate, pre-existing defect (NOT introduced here)

| file | shape | goopg | PG |
|---|---|---|---|
| `p2.sql` | MHJ + SEMI + **one** hand-written uncorrelated `IN (subquery)` | 377 | 377 |
| `probe35g.sql` | MHJ + SEMI + **two** hand-written `IN (subquery)` OR-ed | **1329** | 1294 |

`probe35g.sql` is `probe35f.sql` with the two EXISTS hand-rewritten as `IN`.
The EXISTS→ANY pass never fires on it (its only EXISTS is a top-level
conjunct, which the pass declines), so this over-match is reachable at HEAD
without this change. The converted form answers 1294 correctly. Filed as its
own task; ledger row 2026-07-31.
