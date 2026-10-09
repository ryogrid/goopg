# M0146-0002a evidence — Parallel-Hash-arm category regressions (Q12/Q21/Q4)

Recon task: `.ralph/fix_plan.md` M0146-0002a (Kind: recon).
Design doc: `docs/design/0100-0149/m0146-0002a-parallel-hash-arm-regressions.md`.

Captured 2026-09-27 at HEAD `d44f472a9`. Canonical plans come from
`tmp/m0146-0015d/tpch/m0146-0015d-tpch{,-pg}.plans.txt` (the M0146-0015d
acceptance capture). The DP trace ran on the private TPC-H lane
`:5533` (`tmp/goopg-spotcheck-tpch-data`, a clone of `:65433`,
cgroup-capped, `GOOPG_PGSHAPED_DP_TRACE=1`); its EXPLAIN output
reproduces the canonical plans byte-for-byte.

## Files

- `q4-goopg.txt` / `q4-pg.txt` — canonical Q4 plans. PG puts the
  NL Semi probe INSIDE `Gather Merge` (partial path, 68894); goopg runs
  a serial NL Semi over `Gather(orders)` (171540).
- `q4-goopg-clone.txt` — the same plan re-run on the traced clone
  (identical costs: the trace lane is faithful).
- `q4-dptrace.txt` — the joinrel `{orders,lineitem}` partial pathlist:
  the SEMI index-probe partial NL costs **76062** (≈ PG's 68894) and is
  `verdict=accepted`, dominating the PHSJ (174292); `cpgather
  partials=1`. `partialPathDrivingKind` refuses parameterized-inner
  non-INNER (`gatherpaths.go`), `makeGatherPath` reads head-only
  (`gatherpaths.go:200`) → no gather on the joinrel → serial fallback.
- `q21-goopg.txt` / `q21-pg.txt` — canonical Q21 plans. PG: NL Anti
  probe inside the Gather over `PHJ(orders ⋈ PH(l1⋈supplier))`; goopg:
  same nodes but NL Anti above the Gather probing 39277 rows.
- `q21-dptrace-anti-refusals.txt` — twelve `V1-nl-inner jt=ANTI`
  producer refusals, including the exact PG outer set
  `{l1+orders+supplier}+{l3}`.
- `q12-goopg.txt` / `q12-pg.txt` — canonical Q12 plans. goopg elects
  PHJ (184617 < its NL ~195k); PG elects NL (190868) — correct under
  each engine's real heap size.
- `relpages.txt` — pg_class relpages, goopg vs PG 18.3: orders
  26545/27814 (−4.6%), lineitem 115293/129346 (−10.9%). goopg's heap
  packs tighter; the ~14k seqscan cost gap on Q12's lineitem ≈
  `seq_page_cost × 14053 pages` exactly, and orders straddles the
  27648-page ×3 worker threshold (goopg 3 workers vs PG 4).

## Verdict

The Parallel Hash arm elects correctly on all three queries.
Q4/Q21 trace to the parameterized-probe partial nested loop being
INNER-only end to end (producer `joinpathsnli.go:465` refuses LEFT/ANTI;
classifier + executor probe arms are INNER-only). Filed as M0146-0002i
(SEMI) and M0146-0002j (ANTI). Q12 is a storage-packing boundary,
ledgered — not planner-actionable.
