# M0145-0008ag — Q14's store_sales probe after the multiplier retirement (re-measure)

Status: done 2026-10-13 — resolved by M0146-0159; no code change of its own.
Parent: M0145-0008af. Re-opened by OWNER DECISIONS 2026-10-12 ("re-measure this
task's Q14 shape after M0146-0159 lands").

## Question

Under the x2 `indexProbeCostMultiplier`, goopg probed `store_sales` in TPC-DS
Q14 with a Bitmap Heap Scan where PostgreSQL 18.3 uses an Index Scan. The
first statement ran 24.9s against PG's 4.8s at SF0.25.

M0146-0159 retired the multiplier, so every random-page term is now PG's
`random_page_cost`. Does Q14 still diverge?

## Measurement

Plans come from M0146-0159's final fire-set captures: x2 HEAD `566d9721c`
vs the retirement, on private clones, with PG from the same run. Evidence is
in `analysis/m0145/m0145-0008ag/`.

- **Scan inventory** (`q14-scan-inventory.txt`):
  - **SF1 under x2.** Bitmap Heap Scans on `store_sales` (three, over
    `store_sales_pkey`) and on `item` (five, over `item_pkey`) — the task's
    symptom.
  - **Retired, both scales.** Index Scans using `store_sales_pkey` and
    `item_pkey`. The node inventory is identical to PG's at SF0.25 and at
    SF1.
- **Plan match:**
  - **SF0.25:** Q14 now matches PG and leaves the divergence list. This is
    part of M0146-0159's 56 → 68.
  - **SF1:** the probe choice matches. Q14 stays in the D3-partialpath class
    on a different divergence: two `item` hash builds are serial
    (`Hash` / `Seq Scan`) where PG's are `Parallel Hash` /
    `Parallel Seq Scan`. That is the executor-substrate class (M0146-0049e /
    M0140 partial-path family), not this task's probe election.
- **Wall-clock at SF0.25, both statements** (`q14-timing.txt`):
  - PG 18.3: 7.39s, 9.75s and 10.21s (median 9.75s).
  - goopg: 18.6s (sf025 sweep), down from 21.7s under x2.
  - goopg now runs PG's plan at about 1.9x PG's time. That gap is
    executor-side probe cost, the class M0146-0171 owns.

## Outcome

The premise is gone: Q14 elects PG's index probes at both scales. The task
closes without code.

- **M0146-0009p** was held "until 0008ag resolves" (owner-adjudicated) and
  is selectable again.
- **M0146-0060**'s named blockers, M0146-0061 and this task, are both
  resolved, so its hold lifts too. Its own first step re-checks the `dsc`
  fixture with the gate re-applied.
